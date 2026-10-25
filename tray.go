package main

// Значок в трее: открыть окно, выход. Цикл значка крутится в главном потоке
// до выхода из программы.

import (
	"time"
	"unsafe"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
)

var (
	procFindWindowW               = modUser32.NewProc("FindWindowW")
	procFindWindowExW             = modUser32.NewProc("FindWindowExW")
	procRegisterWindowMessageW    = modUser32.NewProc("RegisterWindowMessageW")
	procChangeWindowMessageFilter = modUser32.NewProc("ChangeWindowMessageFilter")
)

// taskbarReady - есть ли панель задач с областью уведомлений. При запуске из
// автозагрузки проводник нередко стартует позже программы, а systray после
// неудачного Shell_NotifyIcon молча остается без значка.
func taskbarReady() bool {
	cls, _ := windows.UTF16PtrFromString("Shell_TrayWnd")
	tray, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0)
	if tray == 0 {
		return false
	}
	notify, _ := windows.UTF16PtrFromString("TrayNotifyWnd")
	n, _, _ := procFindWindowExW.Call(tray, 0, uintptr(unsafe.Pointer(notify)), 0)
	return n != 0
}

// allowTaskbarCreated: чтобы значок вернулся после перезапуска проводника.
func allowTaskbarCreated() {
	name, _ := windows.UTF16PtrFromString("TaskbarCreated")
	msg, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(name)))
	if msg != 0 {
		procChangeWindowMessageFilter.Call(msg, 1)
	}
}

func (a *App) runTray() {
	allowTaskbarCreated()
	deadline := time.Now().Add(3 * time.Minute)
	for !taskbarReady() && time.Now().Before(deadline) {
		select {
		case <-a.quitCh:
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
	systray.Run(func() {
		systray.SetIcon(trayIcon)
		systray.SetTooltip(appName)
		mOpen := systray.AddMenuItem("Открыть "+appName, "")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Выход", "Завершить звонки и закрыть программу")
		systray.SetOnTapped(func() { go a.window.Show() })
		a.trayReady.Store(true)

		go func() {
			for {
				select {
				case <-mOpen.ClickedCh:
					go a.window.Show()
				case <-mQuit.ClickedCh:
					go a.Quit()
				case <-a.quitCh:
					return
				}
			}
		}()
	}, func() {})
}
