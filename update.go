package main

// Обновление программы из релизов GitHub.
//
// При запуске и раз в 6 часов программа смотрит последний релиз. Если он новее,
// в окне появляется кнопка «Обновить»: exe качается рядом (.new), текущий
// переименовывается в .old (запущенный exe переименовать можно, удалить нельзя),
// новый встает на его место и запускается, старый выходит. Новый дожидается
// выхода старого и удаляет .old.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

const (
	updateRepo  = "Trashfallen/Zeno"
	updateAsset = appName + ".exe"
)

// ZENO_UPDATE_API - для проверки обновления на локальном сервере.
func updateAPI() string {
	if u := os.Getenv("ZENO_UPDATE_API"); u != "" {
		return u
	}
	return "https://api.github.com/repos/" + updateRepo + "/releases/latest"
}

type UpdateState struct {
	Checked    bool    `json:"checked"`
	Available  bool    `json:"available"`
	Version    string  `json:"version"`
	Notes      string  `json:"notes"`
	URL        string  `json:"url"`
	Busy       bool    `json:"busy"`
	Progress   float64 `json:"progress"`
	Message    string  `json:"message"`
	Error      string  `json:"error"`
	CanInstall bool    `json:"can_install"`
}

type Updater struct {
	mu    sync.Mutex
	st    UpdateState
	asset string
	size  int64
}

func newUpdater() *Updater {
	return &Updater{st: UpdateState{CanInstall: appVersion != "dev"}}
}

func (u *Updater) State() UpdateState {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.st
}

func (u *Updater) set(f func(*UpdateState)) {
	u.mu.Lock()
	f(&u.st)
	u.mu.Unlock()
}

// newerVersion: «v1.2.0» новее «1.1.9».
func newerVersion(tag, cur string) bool {
	parse := func(s string) []int {
		s = strings.TrimPrefix(strings.TrimSpace(s), "v")
		s = strings.SplitN(s, "-", 2)[0]
		var out []int
		for _, p := range strings.Split(s, ".") {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil
			}
			out = append(out, n)
		}
		return out
	}
	a, b := parse(tag), parse(cur)
	if a == nil || b == nil {
		return false
	}
	for i := 0; i < max(len(a), len(b)); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func (u *Updater) startBackground(enabled func() bool) {
	go func() {
		time.Sleep(5 * time.Second)
		for {
			if enabled() {
				u.Check()
			}
			time.Sleep(6 * time.Hour)
		}
	}()
}

// Check смотрит последний релиз. Нет релизов - не ошибка.
func (u *Updater) Check() {
	u.set(func(s *UpdateState) { s.Error, s.Message = "", "" })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", updateAPI(), nil)
	req.Header.Set("User-Agent", appName+"/"+appVersion)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		u.set(func(s *UpdateState) { s.Checked, s.Error = true, "GitHub недоступен: "+err.Error() })
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		u.set(func(s *UpdateState) {
			s.Checked, s.Available, s.Message = true, false, "Релизов пока нет"
		})
		return
	}
	if resp.StatusCode != 200 {
		u.set(func(s *UpdateState) { s.Checked, s.Error = true, "GitHub ответил "+resp.Status })
		return
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Body   string `json:"body"`
		Page   string `json:"html_url"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		u.set(func(s *UpdateState) {
			s.Checked, s.Error = true, "не разобрал ответ GitHub: "+err.Error()
		})
		return
	}
	asset, size := "", int64(0)
	for _, a := range rel.Assets {
		if strings.EqualFold(a.Name, updateAsset) {
			asset, size = a.URL, a.Size
		}
	}
	newer := newerVersion(rel.Tag, appVersion)
	u.mu.Lock()
	u.asset, u.size = asset, size
	u.st.Checked, u.st.Available, u.st.Version, u.st.Notes, u.st.URL = true, newer && asset != "", rel.Tag, rel.Body, rel.Page
	switch {
	case newer && asset == "":
		u.st.Message = "В релизе " + rel.Tag + " нет файла " + updateAsset
	case !newer:
		u.st.Message = "Установлена последняя версия"
	}
	u.mu.Unlock()
	if newer && asset != "" {
		logf("Доступна новая версия %s", rel.Tag)
	}
}

func (u *Updater) download(path string) error {
	u.mu.Lock()
	url, size := u.asset, u.size
	u.mu.Unlock()
	if url == "" {
		return errors.New("в релизе нет " + updateAsset)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", appName+"/"+appVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("не удалось скачать: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("скачивание: %s", resp.Status)
	}
	if resp.ContentLength > 0 {
		size = resp.ContentLength
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	var got int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				out.Close()
				return err
			}
			got += int64(n)
			if size > 0 {
				p := float64(got) / float64(size)
				u.set(func(s *UpdateState) { s.Progress = p })
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return fmt.Errorf("обрыв при скачивании: %w", rerr)
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	head := make([]byte, 2)
	if f, err := os.Open(path); err == nil {
		_, _ = io.ReadFull(f, head)
		f.Close()
	}
	if got < 1<<20 || string(head) != "MZ" {
		return errors.New("скачанный файл не похож на программу")
	}
	return nil
}

// installUpdate скачивает новую версию, ставит на место текущей и перезапускает программу.
func (a *App) installUpdate() {
	u := a.updater
	u.mu.Lock()
	if u.st.Busy {
		u.mu.Unlock()
		return
	}
	u.st.Busy, u.st.Error, u.st.Progress, u.st.Message = true, "", -1, "Скачиваю обновление"
	u.mu.Unlock()
	fail := func(err error) {
		logf("Обновление: %v", err)
		u.set(func(s *UpdateState) { s.Busy, s.Error, s.Message = false, err.Error(), "" })
	}
	if appVersion == "dev" {
		fail(errors.New("обновление работает только в собранной программе"))
		return
	}
	exe := exePath()
	newExe, oldExe := exe+".new", exe+".old"
	if err := u.download(newExe); err != nil {
		os.Remove(newExe)
		fail(err)
		return
	}
	u.set(func(s *UpdateState) { s.Message, s.Progress = "Устанавливаю", 1 })
	os.Remove(oldExe)
	if err := os.Rename(exe, oldExe); err != nil {
		os.Remove(newExe)
		fail(fmt.Errorf("не удалось заменить программу: %w", err))
		return
	}
	if err := os.Rename(newExe, exe); err != nil {
		_ = os.Rename(oldExe, exe)
		fail(fmt.Errorf("не удалось заменить программу: %w", err))
		return
	}
	args := []string{"--after-update", strconv.Itoa(os.Getpid())}
	if a.window.hwnd() == 0 {
		args = append(args, "--minimized")
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	if err := cmd.Start(); err != nil {
		_ = os.Rename(exe, newExe)
		_ = os.Rename(oldExe, exe)
		fail(fmt.Errorf("новая версия не запустилась: %w", err))
		return
	}
	logf("Обновление установлено (%s), перезапуск", u.State().Version)
	u.set(func(s *UpdateState) { s.Message = "Перезапускаюсь" })
	time.Sleep(300 * time.Millisecond)
	a.Quit()
}

// waitForExit ждет завершения старой копии после обновления.
func waitForExit(pid int, timeout time.Duration) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_, _ = windows.WaitForSingleObject(h, uint32(timeout.Milliseconds()))
}

// cleanupOldExe убирает старую копию (.old) и недокачанную (.new).
func cleanupOldExe() {
	exe := exePath()
	for range 20 {
		err := os.Remove(exe + ".old")
		if err == nil || errors.Is(err, os.ErrNotExist) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	_ = os.Remove(exe + ".new")
}
