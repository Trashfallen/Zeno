package main

// Файлы и картинки в чате. Сами байты идут между страницами по каналу данных
// (ui/app.js), а программа только пишет принятое на диск в «Загрузки\Zeno»,
// читает картинки для показа в истории и открывает файлы. Пути наружу не
// принимаются: только то, что лежит в папке Zeno.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

const maxShowImage = 25 << 20 // картинки больше этого в чате не показываем, только файлом

type incoming struct {
	f    *os.File
	part string
	path string
	size int64
}

var (
	filesMu sync.Mutex
	inFiles = map[string]*incoming{}
)

// filesDir - «Загрузки\Zeno», создается при первом файле.
func filesDir() (string, error) {
	base := knownFolder(windows.FOLDERID_Downloads)
	if base == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(h, "Downloads")
	}
	dir := filepath.Join(base, appName)
	return dir, os.MkdirAll(dir, 0o755)
}

// safeName убирает из имени от друга все, что может увести файл из папки.
func safeName(name string) string {
	// только последняя часть пути; filepath.Base не годится - «a:b» он считает диском
	name = strings.ReplaceAll(name, "\\", "/")
	name = name[strings.LastIndex(name, "/")+1:]
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if name == "" {
		name = "файл"
	}
	if r := []rune(name); len(r) > 120 {
		ext := filepath.Ext(name)
		name = string(r[:120-len([]rune(ext))]) + ext
	}
	return name
}

// freePath: «имя.ext», а если занято - «имя (2).ext» и так далее.
func freePath(dir, name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	p := filepath.Join(dir, name)
	for i := 2; fileExists(p) || fileExists(p+".part"); i++ {
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
	}
	return p
}

// FileBegin готовит прием файла id: пишем в .part, по концу переименуем.
func (a *App) FileBegin(id, name string) error {
	if id == "" {
		return errors.New("нет id файла")
	}
	dir, err := filesDir()
	if err != nil {
		return err
	}
	filesMu.Lock()
	defer filesMu.Unlock()
	if _, ok := inFiles[id]; ok {
		return nil
	}
	path := freePath(dir, safeName(name))
	f, err := os.Create(path + ".part")
	if err != nil {
		return err
	}
	inFiles[id] = &incoming{f: f, part: path + ".part", path: path}
	return nil
}

func (a *App) FileChunk(id, data string) error {
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	filesMu.Lock()
	in := inFiles[id]
	filesMu.Unlock()
	if in == nil {
		return errors.New("файл не начат")
	}
	n, err := in.f.Write(b)
	in.size += int64(n)
	return err
}

// FileEnd закрывает файл и отдает путь, где он лежит.
func (a *App) FileEnd(id string) (string, error) {
	filesMu.Lock()
	in := inFiles[id]
	delete(inFiles, id)
	filesMu.Unlock()
	if in == nil {
		return "", errors.New("файл не начат")
	}
	if err := in.f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(in.part, in.path); err != nil {
		return "", err
	}
	logf("Файл принят: %s (%d байт)", filepath.Base(in.path), in.size)
	return in.path, nil
}

// FileCancel - передача сорвалась: недописанное удаляем.
func (a *App) FileCancel(id string) {
	filesMu.Lock()
	in := inFiles[id]
	delete(inFiles, id)
	filesMu.Unlock()
	if in != nil {
		in.f.Close()
		os.Remove(in.part)
	}
}

// inFilesDir: путь лежит в папке Zeno (из истории его мог подсунуть только сам
// пользователь, но открывать и читать что попало все равно не будем).
func inFilesDir(path string) (string, error) {
	dir, err := filesDir()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(dir, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", errors.New("файл не из папки Zeno")
	}
	return abs, nil
}

// FileRead - картинка для показа в чате (из истории после перезапуска).
func (a *App) FileRead(path string) (string, error) {
	p, err := inFilesDir(path)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", errors.New("файл удален или перемещен")
	}
	if st.Size() > maxShowImage {
		return "", errors.New("слишком большой для показа")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// FileOpen открывает файл программой по умолчанию или показывает в папке.
func (a *App) FileOpen(path string, reveal bool) error {
	p, err := inFilesDir(path)
	if err != nil {
		return err
	}
	if !fileExists(p) {
		return errors.New("файл удален или перемещен")
	}
	var cmd *exec.Cmd
	if reveal {
		cmd = exec.Command("explorer.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + p + `"`}
	} else {
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", p)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
