// Package screenpick - свое окно выбора, что транслировать.
//
// Родной диалог Chromium «Выберите, чем поделиться» не настраивается и
// рисуется прямо внутри WebView2. Поэтому страница показывает свой список окон
// и экранов (список и превью дает этот пакет), а после выбора вызывает
// getDisplayMedia. На это время Pick накрывает окно программы снимком, через
// UI Automation выбирает в родном диалоге то же самое и жмет «Поделиться».
// Не вышло за несколько секунд - снимок убирается и остается обычный диалог.
package screenpick

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32     = windows.NewLazySystemDLL("user32.dll")
	gdi32      = windows.NewLazySystemDLL("gdi32.dll")
	dwmapi     = windows.NewLazySystemDLL("dwmapi.dll")
	ole32      = windows.NewLazySystemDLL("ole32.dll")
	oleaut     = windows.NewLazySystemDLL("oleaut32.dll")
	kernel     = windows.NewLazySystemDLL("kernel32.dll")
	pEnumW     = user32.NewProc("EnumWindows")
	pEnumCW    = user32.NewProc("EnumChildWindows")
	pVis       = user32.NewProc("IsWindowVisible")
	pIconic    = user32.NewProc("IsIconic")
	pGetWin    = user32.NewProc("GetWindow")
	pText      = user32.NewProc("GetWindowTextW")
	pClass     = user32.NewProc("GetClassNameW")
	pLong      = user32.NewProc("GetWindowLongPtrW")
	pRect      = user32.NewProc("GetWindowRect")
	pPID       = user32.NewProc("GetWindowThreadProcessId")
	pPrint     = user32.NewProc("PrintWindow")
	pGetDC     = user32.NewProc("GetDC")
	pRelDC     = user32.NewProc("ReleaseDC")
	pEnumM     = user32.NewProc("EnumDisplayMonitors")
	pMonInf    = user32.NewProc("GetMonitorInfoW")
	pEnumDD    = user32.NewProc("EnumDisplayDevicesW")
	pCloak     = dwmapi.NewProc("DwmGetWindowAttribute")
	pMemDC     = gdi32.NewProc("CreateCompatibleDC")
	pMemBmp    = gdi32.NewProc("CreateCompatibleBitmap")
	pSelect    = gdi32.NewProc("SelectObject")
	pDelDC     = gdi32.NewProc("DeleteDC")
	pDelObj    = gdi32.NewProc("DeleteObject")
	pBlt       = gdi32.NewProc("BitBlt")
	pStr       = gdi32.NewProc("StretchBlt")
	pStrMod    = gdi32.NewProc("SetStretchBltMode")
	pBrush     = gdi32.NewProc("SetBrushOrgEx")
	pBits      = gdi32.NewProc("GetDIBits")
	pDIBSect   = gdi32.NewProc("CreateDIBSection")
	pDrawIcon  = user32.NewProc("DrawIconEx")
	pClassLong = user32.NewProc("GetClassLongPtrW")
	pSendMsgTO = user32.NewProc("SendMessageTimeoutW")
)

const (
	srcCopy     = 0x00CC0020
	captureBlt  = 0x40000000
	halftone    = 4
	thumbW      = 384
	thumbH      = 216
	gwOwner     = 4
	gwlExStyle  = ^uintptr(19) // -20
	wsExTool    = 0x00000080
	wsExAppWin  = 0x00040000
	wsExNoAct   = 0x08000000
	wsExTransp  = 0x00000020
	wsExLayered = 0x00080000
)

type rect struct{ Left, Top, Right, Bottom int32 }

func (r rect) w() int32 { return r.Right - r.Left }
func (r rect) h() int32 { return r.Bottom - r.Top }

// Source - окно или экран, который можно транслировать.
type Source struct {
	ID    string `json:"id"`   // w<hwnd> или s<номер>
	Kind  string `json:"kind"` // window | screen
	Name  string `json:"name"`
	Thumb string `json:"thumb,omitempty"` // data:image/jpeg;base64,...
	Icon  string `json:"icon,omitempty"`  // data:image/png;base64,... (только окна)
	W     int    `json:"w"`
	H     int    `json:"h"`

	hwnd uintptr
	rc   rect
	nth  int // окно: какое по счету с таким же заголовком; экран: номер в списке
}

var (
	lastMu sync.Mutex
	last   = map[string]Source{}
)

// List собирает экраны и окна с превью. Окна своего процесса не попадают.
func List(thumbs bool) []Source {
	out := append(screens(), windowsList()...)
	if thumbs {
		// PrintWindow у зависшей программы может не вернуться - ждем не дольше 2 с
		type res struct {
			i          int
			thumb, ico string
		}
		ch := make(chan res, len(out))
		for i := range out {
			s := out[i]
			go func() {
				r := res{i: i, thumb: thumb(&s)}
				if s.Kind == "window" {
					r.ico = windowIcon(s.hwnd)
				}
				ch <- r
			}()
		}
		timeout := time.After(2 * time.Second)
	wait:
		for range out {
			select {
			case r := <-ch:
				out[r.i].Thumb, out[r.i].Icon = r.thumb, r.ico
			case <-timeout:
				break wait
			}
		}
	}
	lastMu.Lock()
	last = map[string]Source{}
	for _, s := range out {
		last[s.ID] = s
	}
	lastMu.Unlock()
	return out
}

// Find возвращает источник из последнего списка.
func Find(id string) (Source, bool) {
	lastMu.Lock()
	defer lastMu.Unlock()
	s, ok := last[id]
	return s, ok
}

// ---------- экраны ----------

// screens: порядок как у Chromium - по номеру видеовыхода (EnumDisplayDevices).
func screens() []Source {
	type displayDevice struct {
		Cb           uint32
		DeviceName   [32]uint16
		DeviceString [128]uint16
		StateFlags   uint32
		DeviceID     [128]uint16
		DeviceKey    [128]uint16
	}
	order := map[string]int{}
	for i := 0; ; i++ {
		var dd displayDevice
		dd.Cb = uint32(unsafe.Sizeof(dd))
		if r, _, _ := pEnumDD.Call(0, uintptr(i), uintptr(unsafe.Pointer(&dd)), 0); r == 0 {
			break
		}
		if dd.StateFlags&1 != 0 { // DISPLAY_DEVICE_ACTIVE
			order[windows.UTF16ToString(dd.DeviceName[:])] = len(order)
		}
	}
	monMu.Lock()
	monList = nil
	pEnumM.Call(0, 0, monCb, 0)
	mons := monList
	monMu.Unlock()
	sort.SliceStable(mons, func(i, j int) bool {
		a, okA := order[mons[i].dev]
		b, okB := order[mons[j].dev]
		if okA != okB {
			return okA
		}
		return a < b
	})
	out := make([]Source, 0, len(mons))
	for i, m := range mons {
		name := "Весь экран"
		if len(mons) > 1 {
			name = "Экран " + strconv.Itoa(i+1)
			if m.primary {
				name += " (основной)"
			}
		}
		out = append(out, Source{ID: "s" + strconv.Itoa(i), Kind: "screen", Name: name,
			W: int(m.rc.w()), H: int(m.rc.h()), rc: m.rc, nth: i})
	}
	return out
}

// windows.NewCallback дает ограниченное число колбэков - создаем их один раз,
// а данные передаем через переменные под мьютексом.
type mon struct {
	rc      rect
	primary bool
	dev     string
}

var (
	monMu   sync.Mutex
	monList []mon
	monCb   = windows.NewCallback(func(hm, _, _, _ uintptr) uintptr {
		type monitorInfoEx struct {
			Size          uint32
			Monitor, Work rect
			Flags         uint32
			Device        [32]uint16
		}
		mi := monitorInfoEx{}
		mi.Size = uint32(unsafe.Sizeof(mi))
		if r, _, _ := pMonInf.Call(hm, uintptr(unsafe.Pointer(&mi))); r != 0 {
			monList = append(monList, mon{mi.Monitor, mi.Flags&1 != 0, windows.UTF16ToString(mi.Device[:])})
		}
		return 1
	})
)

// ---------- окна ----------

var (
	winMu   sync.Mutex
	winList []Source
	winSeen map[string]int
	winCb   = windows.NewCallback(func(h, _ uintptr) uintptr {
		if s, ok := windowSource(h); ok {
			s.nth = winSeen[s.Name]
			winSeen[s.Name]++
			winList = append(winList, s)
		}
		return 1
	})
)

// windowsList: окна в порядке Z, примерно те же, что показывает Chromium.
func windowsList() []Source {
	winMu.Lock()
	defer winMu.Unlock()
	winList, winSeen = nil, map[string]int{}
	pEnumW.Call(winCb, 0)
	return winList
}

func windowSource(h uintptr) (Source, bool) {
	if v, _, _ := pVis.Call(h); v == 0 {
		return Source{}, false
	}
	if ic, _, _ := pIconic.Call(h); ic != 0 {
		return Source{}, false
	}
	if o, _, _ := pGetWin.Call(h, gwOwner); o != 0 {
		return Source{}, false
	}
	ex, _, _ := pLong.Call(h, gwlExStyle)
	if ex&wsExTool != 0 && ex&wsExAppWin == 0 {
		return Source{}, false
	}
	// прозрачные для мыши слои поверх всего (оверлеи видеокарты и т.п.)
	if ex&wsExTransp != 0 && ex&wsExLayered != 0 {
		return Source{}, false
	}
	var cloaked uint32
	if r, _, _ := pCloak.Call(h, 14, uintptr(unsafe.Pointer(&cloaked)), 4); r == 0 && cloaked != 0 { // DWMWA_CLOAKED
		return Source{}, false
	}
	var pid uint32
	pPID.Call(h, uintptr(unsafe.Pointer(&pid)))
	if pid == uint32(os.Getpid()) {
		return Source{}, false
	}
	if cls := className(h); cls == "Progman" || cls == "WorkerW" || cls == "Shell_TrayWnd" {
		return Source{}, false
	}
	title := windowText(h)
	if title == "" {
		return Source{}, false
	}
	var rc rect
	pRect.Call(h, uintptr(unsafe.Pointer(&rc)))
	if rc.w() < 80 || rc.h() < 50 {
		return Source{}, false
	}
	return Source{ID: "w" + strconv.FormatUint(uint64(h), 10), Kind: "window", Name: title,
		W: int(rc.w()), H: int(rc.h()), hwnd: h, rc: rc}, true
}

func windowText(h uintptr) string {
	buf := make([]uint16, 512)
	n, _, _ := pText.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

func className(h uintptr) string {
	buf := make([]uint16, 128)
	n, _, _ := pClass.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

// ---------- превью ----------

// thumb: окно рисуем через PrintWindow (видно и перекрытое), экран - копией с экрана.
func thumb(s *Source) string {
	w, h := int32(thumbW), int32(float64(thumbW)*float64(s.rc.h())/float64(max(1, s.rc.w())))
	if h > thumbH {
		h, w = thumbH, int32(float64(thumbH)*float64(s.rc.w())/float64(max(1, s.rc.h())))
	}
	if w < 1 || h < 1 {
		return ""
	}
	screen, _, _ := pGetDC.Call(0)
	if screen == 0 {
		return ""
	}
	defer pRelDC.Call(0, screen)
	dst, _, _ := pMemDC.Call(screen)
	bmp, _, _ := pMemBmp.Call(screen, uintptr(w), uintptr(h))
	defer pDelDC.Call(dst)
	defer pDelObj.Call(bmp)
	pSelect.Call(dst, bmp)
	pStrMod.Call(dst, halftone)
	pBrush.Call(dst, 0, 0, 0)

	if s.Kind == "screen" {
		pStr.Call(dst, 0, 0, uintptr(w), uintptr(h), screen, uintptr(s.rc.Left), uintptr(s.rc.Top),
			uintptr(s.rc.w()), uintptr(s.rc.h()), srcCopy|captureBlt)
	} else {
		full, _, _ := pMemDC.Call(screen)
		fb, _, _ := pMemBmp.Call(screen, uintptr(s.rc.w()), uintptr(s.rc.h()))
		pSelect.Call(full, fb)
		pPrint.Call(s.hwnd, full, 2) // PW_RENDERFULLCONTENT - и для окон с DirectX
		pStr.Call(dst, 0, 0, uintptr(w), uintptr(h), full, 0, 0, uintptr(s.rc.w()), uintptr(s.rc.h()), srcCopy)
		pDelDC.Call(full)
		pDelObj.Call(fb)
	}
	img := readBitmap(dst, bmp, int(w), int(h))
	if img == nil {
		return ""
	}
	var buf bytes.Buffer
	if jpeg.Encode(&buf, img, &jpeg.Options{Quality: 72}) != nil {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// windowIcon: значок окна 32x32 в PNG с прозрачностью.
func windowIcon(h uintptr) string {
	const wmGetIcon, iconBig, iconSmall2, smtoAbortIfHung = 0x007F, 1, 2, 0x0002
	var ico uintptr
	for _, kind := range []uintptr{iconBig, iconSmall2} {
		if r, _, _ := pSendMsgTO.Call(h, wmGetIcon, kind, 0, smtoAbortIfHung, 150, uintptr(unsafe.Pointer(&ico))); r != 0 && ico != 0 {
			break
		}
	}
	if ico == 0 {
		ico, _, _ = pClassLong.Call(h, ^uintptr(13)) // GCLP_HICON = -14
	}
	if ico == 0 {
		return ""
	}
	const size = 32
	bi := bitmapInfo{Width: size, Height: -size, Planes: 1, Bits: 32}
	bi.Size = 40
	var bits unsafe.Pointer
	dib, _, _ := pDIBSect.Call(0, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if dib == 0 || bits == nil {
		return ""
	}
	defer pDelObj.Call(dib)
	dc, _, _ := pMemDC.Call(0)
	defer pDelDC.Call(dc)
	pSelect.Call(dc, dib)
	pDrawIcon.Call(dc, 0, 0, ico, size, size, 0, 0, 3) // DI_NORMAL
	src := unsafe.Slice((*byte)(bits), size*size*4)
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	alpha := false
	for i := 0; i < len(src); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = src[i+2], src[i+1], src[i], src[i+3]
		alpha = alpha || src[i+3] != 0
	}
	if !alpha { // старые значки без альфа-канала
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255
		}
	} else { // DrawIconEx дает премультиплицированные цвета
		for i := 0; i < len(img.Pix); i += 4 {
			if a := int(img.Pix[i+3]); a > 0 && a < 255 {
				for k := 0; k < 3; k++ {
					img.Pix[i+k] = byte(min(255, int(img.Pix[i+k])*255/a))
				}
			}
		}
	}
	var buf bytes.Buffer
	if png.Encode(&buf, img) != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

type bitmapInfo struct {
	Size          uint32
	Width, Height int32
	Planes, Bits  uint16
	Compression   uint32
	SizeImage     uint32
	XPels, YPels  int32
	ClrUsed       uint32
	ClrImportant  uint32
	Colors        [4]byte
}

// readBitmap читает 32-битную картинку из GDI (BGRA) в image.RGBA.
func readBitmap(dc, bmp uintptr, w, h int) *image.RGBA {
	bi := bitmapInfo{Width: int32(w), Height: -int32(h), Planes: 1, Bits: 32}
	bi.Size = 40
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if r, _, _ := pBits.Call(dc, bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&img.Pix[0])), uintptr(unsafe.Pointer(&bi)), 0); r == 0 {
		return nil
	}
	p := img.Pix
	for i := 0; i < len(p); i += 4 {
		p[i], p[i+2], p[i+3] = p[i+2], p[i], 255
	}
	return img
}
