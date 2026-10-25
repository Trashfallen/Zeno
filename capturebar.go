package main

// Панель Chromium «http://127.0.0.1:… предоставляет доступ к вашему экрану»
// внизу экрана при трансляции. Настройки у WebView2 для нее нет: событие
// ScreenCaptureStarting умеет только разрешить или отменить захват, а флаг
// --use-fake-ui-for-media-stream ломает звук трансляции. Поэтому, пока идет
// трансляция, ищем небольшие окна процессов WebView2 нашей программы и прячем
// их. Остановить трансляцию можно кнопками в самом Zeno.

import (
	"os"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetWindowRect = modUser32.NewProc("GetWindowRect")

type captureBar struct {
	mu   sync.Mutex
	stop chan struct{}
}

var theCaptureBar captureBar

// SetCaptureActive: true - трансляция началась, прятать панель; false - перестать.
func (a *App) SetCaptureActive(active bool) {
	b := &theCaptureBar
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stop != nil {
		close(b.stop)
		b.stop = nil
	}
	if !active {
		return
	}
	stop := make(chan struct{})
	b.stop = stop
	go func() {
		// панель появляется не сразу и может показаться снова - смотрим часто первые
		// секунды, потом реже, пока трансляция не закончится
		start := time.Now()
		for {
			hideCaptureBars()
			wait := time.Second
			if time.Since(start) < 15*time.Second {
				wait = 150 * time.Millisecond
			}
			select {
			case <-stop:
				return
			case <-a.quitCh:
				return
			case <-time.After(wait):
			}
		}
	}()
}

// webviewPIDs - процессы WebView2, запущенные нашей программой.
func webviewPIDs() map[uint32]bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	parent := map[uint32]uint32{}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if windows.UTF16ToString(e.ExeFile[:]) == "msedgewebview2.exe" {
			parent[e.ProcessID] = e.ParentProcessID
		}
	}
	me := uint32(os.Getpid())
	out := map[uint32]bool{}
	for pid := range parent {
		// процесс браузера - наш ребенок, остальные - дети процесса браузера
		for p, depth := pid, 0; depth < 4; p, depth = parent[p], depth+1 {
			if parent[p] == me {
				out[pid] = true
				break
			}
			if _, ok := parent[parent[p]]; !ok {
				break
			}
		}
	}
	return out
}

var (
	barMu   sync.Mutex
	barPIDs map[uint32]bool
	barCb   = windows.NewCallback(func(h, _ uintptr) uintptr {
		var pid uint32
		procGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&pid)))
		if !barPIDs[pid] {
			return 1
		}
		if v, _, _ := procIsWindowVisible.Call(h); v == 0 {
			return 1
		}
		var r rect
		procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
		// панель - низкая полоса (до 140 px с учетом масштаба 200%); окно выбора экрана выше
		if hgt, w := r.Bottom-r.Top, r.Right-r.Left; hgt > 0 && hgt <= 140 && w >= 300 {
			procShowWindow.Call(h, swHide)
		}
		return 1
	})
)

func hideCaptureBars() {
	pids := webviewPIDs()
	if len(pids) == 0 {
		return
	}
	barMu.Lock()
	defer barMu.Unlock()
	barPIDs = pids
	procEnumWindows.Call(barCb, 0)
}
