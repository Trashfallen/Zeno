package vlan

// Установка службы. Zeno стоит без прав администратора, поэтому один раз
// запускаем себя же с повышением (окно UAC): копия программы и wintun.dll
// ложатся в Program Files\Zeno (туда пишет только администратор), служба
// virtualZeno запускается сама вместе с Windows. Запускать и опрашивать ее
// дальше может обычный пользователь - повышение больше не нужно.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const firewallRule = "virtualZeno"

// installDir - Program Files\Zeno.
func installDir() string {
	pf := os.Getenv("ProgramW6432")
	if pf == "" {
		pf = os.Getenv("ProgramFiles")
	}
	return filepath.Join(pf, "Zeno")
}

// Install (с правами администратора): файлы, служба, правило брандмауэра, запуск.
// owner - SID пользователя, которому служба откроет свой канал.
func Install(owner string) error {
	if _, err := windows.StringToSid(owner); err != nil {
		return fmt.Errorf("кривой SID %q", owner)
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err == nil {
		stopService(s)
	}

	dir := installDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	exe := filepath.Join(dir, "virtualZeno.exe")
	if err := copySelf(exe); err != nil {
		return err
	}
	if err := writeWintunDLL(dir); err != nil {
		return err
	}

	cfg := mgr.Config{
		DisplayName:  "virtualZeno",
		Description:  "Виртуальная локальная сеть Zeno: адаптер Wintun для LAN-игр и общих папок с друзьями.",
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	}
	if s == nil {
		s, err = m.CreateService(ServiceName, exe, cfg, "-lan-service", owner)
		if err != nil {
			return err
		}
	} else {
		old, err := s.Config()
		if err != nil {
			return err
		}
		old.DisplayName, old.Description, old.StartType = cfg.DisplayName, cfg.Description, cfg.StartType
		old.BinaryPathName = syscall.EscapeArg(exe) + " -lan-service " + owner
		if err := s.UpdateConfig(old); err != nil {
			return err
		}
	}
	defer s.Close()
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 24*3600)
	// обычный пользователь (IU) может запускать и опрашивать службу
	sd, err := windows.SecurityDescriptorFromString("D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWRPLOCRRC;;;IU)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetSecurityInfo(s.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return err
	}

	// входящие с адресов virtualZeno на наш адрес: открыт любой порт, как в настоящей LAN
	_ = netsh("advfirewall", "firewall", "delete", "rule", "name="+firewallRule)
	if err := netsh("advfirewall", "firewall", "add", "rule", "name="+firewallRule, "dir=in", "action=allow",
		"localip="+netPrefix, "remoteip="+netPrefix, "profile=any"); err != nil {
		return err
	}
	return s.Start()
}

// Uninstall (с правами администратора): служба (с ней уходит адаптер), правило, файлы.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	if s, err := m.OpenService(ServiceName); err == nil {
		stopService(s)
		err = s.Delete()
		s.Close()
		if err != nil {
			return err
		}
	}
	_ = netsh("advfirewall", "firewall", "delete", "rule", "name="+firewallRule)
	dir := installDir()
	for _, n := range []string{"virtualZeno.exe", "wintun.dll", "virtualZeno.log", "virtualZeno.log.1"} {
		_ = os.Remove(filepath.Join(dir, n))
	}
	_ = os.Remove(dir) // только если пустая
	return nil
}

func stopService(s *mgr.Service) {
	st, err := s.Control(svc.Stop)
	for i := 0; err == nil && st.State != svc.Stopped && i < 100; i++ {
		time.Sleep(100 * time.Millisecond)
		st, err = s.Query()
	}
}

func copySelf(dst string) error {
	src, err := os.Executable()
	if err != nil {
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
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func netsh(args ...string) error {
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "netsh.exe"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("netsh %s %s: %v %s", args[0], args[2], err, out)
	}
	return nil
}

// ---------- сторона Zeno (без прав) ----------

// ServiceState: "missing" - не поставлена, иначе "running", "stopped", "starting"...
func ServiceState() string {
	s, err := openService(windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return "missing"
	}
	defer windows.CloseServiceHandle(s)
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(s, &st); err != nil {
		return "missing"
	}
	switch st.CurrentState {
	case windows.SERVICE_RUNNING:
		return "running"
	case windows.SERVICE_START_PENDING:
		return "starting"
	case windows.SERVICE_STOP_PENDING:
		return "stopping"
	}
	return "stopped"
}

// StartService запускает поставленную, но остановленную службу.
func StartService() error {
	s, err := openService(windows.SERVICE_START)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(s)
	err = windows.StartService(s, 0, nil)
	if err == windows.ERROR_SERVICE_ALREADY_RUNNING {
		return nil
	}
	return err
}

func openService(access uint32) (windows.Handle, error) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, err
	}
	defer windows.CloseServiceHandle(m)
	name, _ := windows.UTF16PtrFromString(ServiceName)
	return windows.OpenService(m, name, access)
}

// CurrentUserSID - SID того, кто запустил Zeno (его передаем установщику).
func CurrentUserSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

// ErrCancelled - пользователь закрыл окно UAC.
var ErrCancelled = errors.New("разрешение администратора не дано")

type shellExecuteInfo struct {
	size      uint32
	mask      uint32
	hwnd      uintptr
	verb      *uint16
	file      *uint16
	params    *uint16
	dir       *uint16
	show      int32
	instApp   uintptr
	idList    uintptr
	class     *uint16
	keyClass  uintptr
	hotKey    uint32
	iconOrMon uintptr
	process   windows.Handle
}

var procShellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// Elevated запускает этот же exe с аргументами от имени администратора (окно
// UAC поверх hwnd) и ждет его выхода. Код выхода не 0 - ошибка.
func Elevated(hwnd uintptr, args string, wait time.Duration) (uint32, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(args)
	info := shellExecuteInfo{mask: 0x40 | 0x100, hwnd: hwnd, verb: verb, file: file, params: params} // NOCLOSEPROCESS | NOASYNC
	info.size = uint32(unsafe.Sizeof(info))
	if r, _, err := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if err == windows.ERROR_CANCELLED {
			return 0, ErrCancelled
		}
		return 0, err
	}
	defer windows.CloseHandle(info.process)
	if ev, _ := windows.WaitForSingleObject(info.process, uint32(wait/time.Millisecond)); ev != windows.WAIT_OBJECT_0 {
		return 0, errors.New("установщик не ответил вовремя")
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return 0, err
	}
	return code, nil
}
