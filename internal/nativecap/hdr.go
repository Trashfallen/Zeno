package nativecap

// HDR у отправителя. Если на мониторе включен HDR, захват в BGRA 8 бит просто
// обрезает все, что ярче белого, и у друга картинка пересвечена. Тогда
// захватываем в 16 бит с плавающей точкой (scRGB, 1.0 = 80 нит) и на
// видеокарте переводим в обычный диапазон: белый SDR из настроек Windows
// становится белым, что ярче - плавно сжимается, а не обрезается.

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	pGetDisplayConfigBufferSizes = user32.NewProc("GetDisplayConfigBufferSizes")
	pQueryDisplayConfig          = user32.NewProc("QueryDisplayConfig")
	pDisplayConfigGetDeviceInfo  = user32.NewProc("DisplayConfigGetDeviceInfo")
	pGetMonitorInfoW             = user32.NewProc("GetMonitorInfoW")
	pMonitorFromWindow           = user32.NewProc("MonitorFromWindow")
)

type luid struct {
	Low  uint32
	High int32
}

type pathInfo struct {
	SrcAdapter                       luid
	SrcID, SrcModeIdx, SrcFlags      uint32
	TgtAdapter                       luid
	TgtID, TgtModeIdx, Tech, Rot     uint32
	Scaling, RefreshNum, RefreshDen  uint32
	ScanLine, TgtAvailable, TgtFlags uint32
	Flags                            uint32
}

type devInfoHeader struct {
	Type, Size uint32
	Adapter    luid
	ID         uint32
}

// HDRInfo - включен ли HDR на мониторе и яркость белого SDR (нит).
type HDRInfo struct {
	On       bool
	WhiteNit float64
}

// hdrInfo: монитор (HMONITOR) -> путь вывода Windows -> HDR и белый SDR.
func hdrInfo(monitor uintptr) (HDRInfo, error) {
	type monitorInfoEx struct {
		Size          uint32
		Monitor, Work [4]int32
		Flags         uint32
		Device        [32]uint16
	}
	mi := monitorInfoEx{}
	mi.Size = uint32(unsafe.Sizeof(mi))
	if r, _, _ := pGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return HDRInfo{}, errors.New("монитор не найден")
	}
	device := windows.UTF16ToString(mi.Device[:])

	const qdcOnlyActive = 2
	var np, nm uint32
	if r, _, _ := pGetDisplayConfigBufferSizes.Call(qdcOnlyActive, uintptr(unsafe.Pointer(&np)), uintptr(unsafe.Pointer(&nm))); r != 0 || np == 0 {
		return HDRInfo{}, errors.New("нет данных о выводах")
	}
	paths := make([]pathInfo, np)
	modes := make([]byte, 64*max(nm, 1)) // DISPLAYCONFIG_MODE_INFO - 64 байта
	if r, _, _ := pQueryDisplayConfig.Call(qdcOnlyActive, uintptr(unsafe.Pointer(&np)), uintptr(unsafe.Pointer(&paths[0])),
		uintptr(unsafe.Pointer(&nm)), uintptr(unsafe.Pointer(&modes[0])), 0); r != 0 {
		return HDRInfo{}, errors.New("QueryDisplayConfig не сработал")
	}
	for _, p := range paths[:np] {
		src := struct {
			devInfoHeader
			Name [32]uint16
		}{devInfoHeader: devInfoHeader{Type: 1, Adapter: p.SrcAdapter, ID: p.SrcID}} // GET_SOURCE_NAME
		src.Size = uint32(unsafe.Sizeof(src))
		if r, _, _ := pDisplayConfigGetDeviceInfo.Call(uintptr(unsafe.Pointer(&src))); r != 0 || windows.UTF16ToString(src.Name[:]) != device {
			continue
		}
		info := HDRInfo{WhiteNit: 80}
		ac := struct {
			devInfoHeader
			Value, Encoding, Bits uint32
		}{devInfoHeader: devInfoHeader{Type: 9, Adapter: p.TgtAdapter, ID: p.TgtID}} // GET_ADVANCED_COLOR_INFO
		ac.Size = uint32(unsafe.Sizeof(ac))
		if r, _, _ := pDisplayConfigGetDeviceInfo.Call(uintptr(unsafe.Pointer(&ac))); r == 0 {
			// включено «расширенное цветовое» и это не принудительный широкий цвет
			// (автоуправление цветом на SDR-мониторе) - значит HDR
			info.On = ac.Value&2 != 0 && ac.Value&4 == 0
		}
		wl := struct {
			devInfoHeader
			Level uint32
		}{devInfoHeader: devInfoHeader{Type: 11, Adapter: p.TgtAdapter, ID: p.TgtID}} // GET_SDR_WHITE_LEVEL
		wl.Size = uint32(unsafe.Sizeof(wl))
		if r, _, _ := pDisplayConfigGetDeviceInfo.Call(uintptr(unsafe.Pointer(&wl))); r == 0 && wl.Level > 0 {
			info.WhiteNit = float64(wl.Level) / 1000 * 80 // единицы: 1000 = 80 нит
		}
		return info, nil
	}
	return HDRInfo{}, errors.New("вывод монитора не найден")
}

// monitorOfWindow - монитор, на котором окно (для захвата окна).
func monitorOfWindow(h uintptr) uintptr {
	m, _, _ := pMonitorFromWindow.Call(h, 2) // MONITOR_DEFAULTTONEAREST
	return m
}

// MonitorHDR - для проверки и статистики: HDR на мониторе, где точка (x, y).
func MonitorHDR(x, y int32) (HDRInfo, error) { return hdrInfo(monitorAt(x, y)) }
