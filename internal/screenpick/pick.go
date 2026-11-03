package screenpick

import (
	"errors"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	pRegClass   = user32.NewProc("RegisterClassExW")
	pCreateWin  = user32.NewProc("CreateWindowExW")
	pDestroyWin = user32.NewProc("DestroyWindow")
	pDefProc    = user32.NewProc("DefWindowProcW")
	pBeginPaint = user32.NewProc("BeginPaint")
	pEndPaint   = user32.NewProc("EndPaint")
	pUpdateWin  = user32.NewProc("UpdateWindow")
	pShowWin    = user32.NewProc("ShowWindow")
	pPeekMsg    = user32.NewProc("PeekMessageW")
	pTransMsg   = user32.NewProc("TranslateMessage")
	pDispMsg    = user32.NewProc("DispatchMessageW")
	pClientRect = user32.NewProc("GetClientRect")
	pClientToSc = user32.NewProc("ClientToScreen")
	pModule     = kernel.NewProc("GetModuleHandleW")

	// ErrTimeout - родной диалог не удалось пройти сам, он остался на экране.
	ErrTimeout = errors.New("родной диалог не прошел автоматически")
)

// Pick накрывает окно host снимком, ждет родной диалог выбора (страница в это
// время вызывает getDisplayMedia), выбирает в нем src, включает звук системы и
// жмет «Поделиться». ready вызывается, как только окно накрыто - тогда пора
// звать getDisplayMedia. done закрывается, когда getDisplayMedia в странице
// завершился (успешно или нет). Блокирует до конца, звать в отдельной горутине.
func Pick(host uintptr, src Source, done <-chan struct{}, ready func()) error {
	pickMu.Lock()
	defer pickMu.Unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	cv := newCover(host)
	ready()
	defer cv.close()
	u, free, err := newUIA()
	if err != nil {
		return err
	}
	defer free()

	content := contentWindow(host)
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		pump()
		select {
		case <-done:
			return nil // страница уже получила ответ: отказ или ошибка
		default:
		}
		if content == 0 {
			content = contentWindow(host)
			Debug("окно содержимого %x", content)
		} else if u.tryPick(content, src) {
			// диалог закрывается не мгновенно - снимок держим, пока страница не получит поток
			wait := time.After(1500 * time.Millisecond)
			for {
				pump()
				select {
				case <-done:
					return nil
				case <-wait:
					return nil
				case <-time.After(30 * time.Millisecond):
				}
			}
		}
		time.Sleep(40 * time.Millisecond)
	}
	return ErrTimeout
}

var pickMu sync.Mutex

// Debug - отладочный вывод шагов выбора (для пробы и журнала).
var Debug = func(string, ...any) {}

// tryPick проходит диалог, если он уже открыт. true - «Поделиться» нажата.
func (u *uia) tryPick(content uintptr, src Source) bool {
	root, ok := u.dialogRoot(content)
	if !ok {
		Debug("диалога нет")
		return false
	}
	defer root.release()

	// вкладка: «Окно» первая, «Весь экран» последняя
	if c, err := u.condition(propClassName, "TabbedPaneTab"); err == nil {
		tabs := root.findAll(treeDescendants, c)
		c.release()
		if len(tabs) > 0 {
			t := tabs[0]
			if src.Kind == "screen" {
				t = tabs[len(tabs)-1]
			}
			for _, x := range tabs {
				n := strings.ToLower(x.name())
				if (src.Kind == "screen") == (strings.Contains(n, "экран") || strings.Contains(n, "screen")) {
					t = x
					break
				}
			}
			_ = t.selectItem()
		}
		releaseAll(tabs)
	}

	c, err := u.condition(propClassName, "DesktopMediaSourceView")
	if err != nil {
		return false
	}
	items := root.findAll(treeDescendants, c)
	c.release()
	Debug("источников %d", len(items))
	defer releaseAll(items)
	var target element
	if src.Kind == "screen" {
		if src.nth < len(items) {
			target = items[src.nth]
		}
	} else {
		seen := 0
		for _, it := range items {
			if it.name() == src.Name {
				if seen == src.nth {
					target = it
					break
				}
				seen++
			}
		}
	}
	if !target.ok() || target.invoke() != nil {
		Debug("источник не найден или не нажат: %v", target.ok())
		return false
	}

	// звук системы - всегда включен; отправлять его или нет, решает страница
	for _, cls := range []string{"EdgeToggleButton", "ToggleButton"} {
		if c, err := u.condition(propClassName, cls); err == nil {
			tg, err := root.findFirst(treeDescendants, c)
			c.release()
			if err == nil {
				tg.toggleOn()
				tg.release()
				break
			}
		}
	}

	c, err = u.condition(propClassName, "MdTextButton")
	if err != nil {
		return false
	}
	btns := root.findAll(treeDescendants, c)
	c.release()
	defer releaseAll(btns)
	var share element
	for _, b := range btns {
		n := strings.ToLower(b.name())
		if n == "поделиться" || n == "share" {
			share = b
			break
		}
	}
	if !share.ok() {
		for _, b := range btns {
			if n := strings.ToLower(b.name()); n != "отмена" && n != "cancel" {
				share = b
				break
			}
		}
	}
	if !share.ok() || !share.enabled() {
		Debug("кнопка: %v, доступна: %v", share.ok(), share.ok() && share.enabled())
		return false
	}
	return share.invoke() == nil
}

// dialogRoot ищет окно диалога Chromium: RootView с типом «окно» среди детей
// и внуков Chrome_WidgetWin_1 (смотря по версии, между ними бывает
// BrowserRootView или RootView страницы). По всем потомкам не идем: дерево
// страницы большое.
func (u *uia) dialogRoot(content uintptr) (element, bool) {
	top, err := u.fromHandle(content)
	if err != nil {
		return element{}, false
	}
	defer top.release()
	all, err := u.trueCondition()
	if err != nil {
		return element{}, false
	}
	defer all.release()
	cPick, err := u.condition(propClassName, "DesktopMediaPickerDialogView")
	if err != nil {
		return element{}, false
	}
	defer cPick.release()

	var found element
	check := func(e element) bool {
		if e.class() != "RootView" || e.controlType() != typeWindow {
			return false
		}
		pv, err := e.findFirst(treeDescendants, cPick)
		if err != nil {
			return false
		}
		pv.release()
		return true
	}
	level1 := top.findAll(treeChildren, all)
	defer releaseAll(level1)
	for _, l1 := range level1 {
		if check(l1) {
			l1.call(1) // AddRef: level1 освободится, а найденный отдаем наружу
			return l1, true
		}
		if c := l1.class(); c != "RootView" && c != "BrowserRootView" {
			continue
		}
		level2 := l1.findAll(treeChildren, all)
		for _, l2 := range level2 {
			if !found.ok() && check(l2) {
				found = l2
				continue
			}
			l2.release()
		}
		if found.ok() {
			return found, true
		}
	}
	return element{}, false
}

// contentWindow - окно Chromium с содержимым WebView2 внутри окна программы.
var (
	cwMu    sync.Mutex
	cwFound uintptr
	cwCb    = windows.NewCallback(func(h, _ uintptr) uintptr {
		if className(h) == "Chrome_WidgetWin_1" {
			cwFound = h
			return 0
		}
		return 1
	})
)

func contentWindow(host uintptr) uintptr {
	cwMu.Lock()
	defer cwMu.Unlock()
	cwFound = 0
	pEnumCW.Call(host, cwCb, 0)
	return cwFound
}

func pump() {
	var msg [48]byte
	for {
		if r, _, _ := pPeekMsg.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0, 1); r == 0 { // PM_REMOVE
			return
		}
		pTransMsg.Call(uintptr(unsafe.Pointer(&msg[0])))
		pDispMsg.Call(uintptr(unsafe.Pointer(&msg[0])))
	}
}

// ---------- шторка: снимок окна поверх него, пока идет выбор ----------

type cover struct {
	h, dc, bmp uintptr
}

var (
	coverOnce  sync.Once
	coverClass *uint16
	coverDC    uintptr // рисует WM_PAINT; Pick идет по одному, под pickMu
	coverProc  = windows.NewCallback(func(h, msg, wp, lp uintptr) uintptr {
		switch msg {
		case 0x000F: // WM_PAINT
			var ps struct {
				dc                 uintptr
				erase              int32
				rc                 rect
				restore, incUpdate int32
				reserved           [32]byte
			}
			dc, _, _ := pBeginPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
			var rc rect
			pClientRect.Call(h, uintptr(unsafe.Pointer(&rc)))
			pBlt.Call(dc, 0, 0, uintptr(rc.w()), uintptr(rc.h()), coverDC, 0, 0, srcCopy)
			pEndPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
			return 0
		case 0x0014: // WM_ERASEBKGND
			return 1
		case 0x0021: // WM_MOUSEACTIVATE
			return 3 // MA_NOACTIVATE
		}
		r, _, _ := pDefProc.Call(h, msg, wp, lp)
		return r
	})
)

// newCover снимает клиентскую часть окна с экрана и кладет снимок поверх
// окна. Не вышло - возвращает пустую шторку, диалог тогда просто мелькнет.
func newCover(host uintptr) *cover {
	cv := &cover{}
	var rc rect
	if r, _, _ := pClientRect.Call(host, uintptr(unsafe.Pointer(&rc))); r == 0 || rc.w() <= 0 || rc.h() <= 0 {
		return cv
	}
	var pt struct{ X, Y int32 }
	pClientToSc.Call(host, uintptr(unsafe.Pointer(&pt)))
	coverOnce.Do(func() {
		coverClass, _ = windows.UTF16PtrFromString("ZenoPickCover")
		mod, _, _ := pModule.Call(0)
		wc := struct {
			size, style            uint32
			proc                   uintptr
			clsExtra, wndExtra     int32
			inst, icon, cursor, bg uintptr
			menu, class            *uint16
			iconSm                 uintptr
		}{style: 0, proc: coverProc, inst: mod, class: coverClass}
		wc.size = uint32(unsafe.Sizeof(wc))
		pRegClass.Call(uintptr(unsafe.Pointer(&wc)))
	})
	screen, _, _ := pGetDC.Call(0)
	if screen == 0 {
		return cv
	}
	cv.dc, _, _ = pMemDC.Call(screen)
	cv.bmp, _, _ = pMemBmp.Call(screen, uintptr(rc.w()), uintptr(rc.h()))
	pSelect.Call(cv.dc, cv.bmp)
	pBlt.Call(cv.dc, 0, 0, uintptr(rc.w()), uintptr(rc.h()), screen, uintptr(pt.X), uintptr(pt.Y), srcCopy|captureBlt)
	pRelDC.Call(0, screen)
	coverDC = cv.dc
	mod, _, _ := pModule.Call(0)
	const wsPopup, wsExNoActivate, wsExToolWindow = 0x80000000, 0x08000000, 0x00000080
	cv.h, _, _ = pCreateWin.Call(wsExNoActivate|wsExToolWindow, uintptr(unsafe.Pointer(coverClass)), 0, wsPopup,
		uintptr(pt.X), uintptr(pt.Y), uintptr(rc.w()), uintptr(rc.h()), host, 0, mod, 0)
	if cv.h != 0 {
		pShowWin.Call(cv.h, 4) // SW_SHOWNOACTIVATE
		pUpdateWin.Call(cv.h)
	}
	return cv
}

func (cv *cover) close() {
	if cv.h != 0 {
		pDestroyWin.Call(cv.h)
	}
	if cv.dc != 0 {
		pDelDC.Call(cv.dc)
	}
	if cv.bmp != 0 {
		pDelObj.Call(cv.bmp)
	}
	coverDC = 0
	pump()
}
