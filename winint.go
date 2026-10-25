package main

// Связь с Windows: установка из архива, ярлыки, автозапуск, уведомления, DPAPI.
// Все пишется в профиль пользователя, права администратора не нужны.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// ---------- установка ----------

// installDir: %LOCALAPPDATA%\Programs\Zeno - туда ставятся программы «для одного пользователя».
func installDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return ""
	}
	return filepath.Join(base, "Programs", appName)
}

// installIfNeeded: запуск exe не из своей папки (из архива, из «Загрузок»)
// копирует программу в installDir, ставит ярлыки на рабочий стол и в «Пуск» и
// запускает установленную копию. true - эта копия должна выйти.
func installIfNeeded() bool {
	if appVersion == "dev" || os.Getenv("ZENO_HOME") != "" || os.Getenv("ZENO_PORTABLE") != "" {
		return false
	}
	exe, dir := exePath(), installDir()
	if exe == "" || dir == "" {
		return false
	}
	target := filepath.Join(dir, appName+".exe")
	if strings.EqualFold(filepath.Clean(exe), filepath.Clean(target)) {
		return false
	}
	if err := copyFile(exe, target); err != nil && !fileExists(target) {
		messageBox(appName, "Не удалось установить программу в "+dir+": "+err.Error()+"\n\nZeno запустится прямо отсюда.")
		return false
	}
	// если установленная копия уже запущена, файл занят - тогда просто открываем ее
	_ = createShortcut(desktopShortcut(), target)
	_ = createShortcut(startMenuShortcut(), target)
	cmd := exec.Command(target)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		messageBox(appName, "Установленная копия не запустилась: "+err.Error())
		return false
	}
	return true
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ---------- автозапуск ----------

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, _ := k.GetStringValue(appName)
	return v != ""
}

func setAutostart(on bool) error {
	if appVersion == "dev" {
		return errors.New("автозапуск доступен в собранной программе")
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		return k.SetStringValue(appName, `"`+exePath()+`" --minimized`)
	}
	if err := k.DeleteValue(appName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// ---------- ярлыки ----------

func knownFolder(id *windows.KNOWNFOLDERID) string {
	p, err := windows.KnownFolderPath(id, 0)
	if err != nil {
		return ""
	}
	return p
}

func desktopShortcut() string {
	if d := knownFolder(windows.FOLDERID_Desktop); d != "" {
		return filepath.Join(d, appName+".lnk")
	}
	return ""
}

func startMenuShortcut() string {
	if d := knownFolder(windows.FOLDERID_Programs); d != "" {
		return filepath.Join(d, appName+".lnk")
	}
	return ""
}

// ensureStartMenu: ярлык в меню «Пуск» ставится сам, если его еще нет.
func ensureStartMenu(exe string) {
	if lnk := startMenuShortcut(); lnk != "" && !fileExists(lnk) {
		if err := createShortcut(lnk, exe); err != nil {
			logf("Ярлык в меню Пуск: %v", err)
		}
	}
}

// createShortcut делает .lnk через WScript.Shell (есть в любой Windows).
func createShortcut(lnk, target string) error {
	if lnk == "" {
		return errors.New("папка для ярлыка не найдена")
	}
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	script := "$s=(New-Object -ComObject WScript.Shell).CreateShortcut(" + q(lnk) + ");" +
		"$s.TargetPath=" + q(target) + ";$s.WorkingDirectory=" + q(filepath.Dir(target)) + ";" +
		"$s.IconLocation=" + q(target+",0") + ";$s.Description=" + q("Общаться проще, чем кажется") + ";$s.Save()"
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ярлык не создан: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func openURL(u string) {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	_ = cmd.Start()
	go cmd.Wait()
}

// ---------- DPAPI ----------

// protect шифрует данные ключом текущего пользователя Windows.
func protect(data []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, 0x1, &out); err != nil { // UI_FORBIDDEN
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func unprotect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("пусто")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 0x1, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

// ---------- уведомления ----------

// notifyBalloon показывает уведомление от значка программы в трее
// (Windows 10/11 превращает его в обычное уведомление).
func notifyBalloon(title, text string) {
	h := findOwnWindow("SystrayClass")
	if h == 0 {
		return
	}
	type notifyIconData struct {
		Size                       uint32
		Wnd                        windows.Handle
		ID, Flags, CallbackMessage uint32
		Icon                       windows.Handle
		Tip                        [128]uint16
		State, StateMask           uint32
		Info                       [256]uint16
		Timeout, Version           uint32
		InfoTitle                  [64]uint16
		InfoFlags                  uint32
		GuidItem                   windows.GUID
		BalloonIcon                windows.Handle
	}
	const nimModify, nifInfo, niifUser, niifLarge = 1, 0x10, 4, 0x20
	nid := notifyIconData{Wnd: windows.Handle(h), ID: 100, Flags: nifInfo, InfoFlags: niifUser | niifLarge}
	nid.Size = uint32(unsafe.Sizeof(nid))
	copy(nid.Info[:len(nid.Info)-1], windows.StringToUTF16(truncRunes(text, 250)))
	copy(nid.InfoTitle[:len(nid.InfoTitle)-1], windows.StringToUTF16(truncRunes(title, 60)))
	modShell32.NewProc("Shell_NotifyIconW").Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// findOwnWindow ищет окно нашего процесса с данным классом.
var (
	findMu    sync.Mutex
	findClass string
	findFound uintptr
	findCb    = windows.NewCallback(func(h, _ uintptr) uintptr {
		var pid uint32
		procGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&pid)))
		if pid != uint32(os.Getpid()) {
			return 1
		}
		buf := make([]uint16, 64)
		procGetClassNameW.Call(h, uintptr(unsafe.Pointer(&buf[0])), 64)
		if windows.UTF16ToString(buf) == findClass {
			findFound = h
			return 0
		}
		return 1
	})
	procGetWindowThreadProcessId = modUser32.NewProc("GetWindowThreadProcessId")
	procGetClassNameW            = modUser32.NewProc("GetClassNameW")
	procEnumWindows              = modUser32.NewProc("EnumWindows")
)

func findOwnWindow(class string) uintptr {
	findMu.Lock()
	defer findMu.Unlock()
	findClass, findFound = class, 0
	procEnumWindows.Call(findCb, 0)
	return findFound
}

func allowForeground(pid int) {
	procAllowSetForeground.Call(uintptr(pid))
}
