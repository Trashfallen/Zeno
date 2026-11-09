// Package nativecap - свой захват экрана и окон для трансляции: кадр не уходит
// из видеокарты. Windows Graphics Capture дает текстуру, видеопроцессор D3D11
// сжимает ее до нужного размера и переводит в NV12, аппаратный кодер H.264
// через Media Foundation (на NVIDIA это NVENC, на AMD - AMF, на Intel -
// QuickSync) сжимает. Все через COM без CGO. Номера методов в таблицах -
// по заголовкам Windows SDK (порядок методов в интерфейсе).
package nativecap

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// com - указатель на COM-объект (первое поле объекта - таблица методов).
type com struct{ p unsafe.Pointer }

// uintptrescapes: аргументы - часто uintptr(unsafe.Pointer(&x)) из вызывающей
// функции. Без директивы x живет на стеке горутины, стек может переехать
// до вызова, и Windows пишет результат в старое место (x остается nil -
// падение в encoder.pump, 0.7.2-0.7.3).
//
//go:uintptrescapes
func (o com) call(i int, args ...uintptr) uintptr {
	vtbl := *(**[128]uintptr)(o.p)
	r, _, _ := syscall.SyscallN(vtbl[i], append([]uintptr{uintptr(o.p)}, args...)...)
	return r
}

func (o com) ok() bool { return o.p != nil }

func (o *com) release() {
	if o.p != nil {
		o.call(2)
		o.p = nil
	}
}

func (o com) addRef() { o.call(1) }

func (o com) ptr() uintptr { return uintptr(o.p) }

// qi - QueryInterface.
func (o com) qi(iid *windows.GUID) (com, error) {
	var p unsafe.Pointer
	if err := hr(o.call(0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&p)))); err != nil {
		return com{}, err
	}
	return com{p}, nil
}

// hr превращает HRESULT в ошибку.
func hr(r uintptr) error {
	if int32(uint32(r)) < 0 {
		return hrError(uint32(r))
	}
	return nil
}

type hrError uint32

func (e hrError) Error() string { return fmt.Sprintf("HRESULT 0x%08X", uint32(e)) }

func guid(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(err)
	}
	return g
}

var (
	d3d11   = windows.NewLazySystemDLL("d3d11.dll")
	mfplat  = windows.NewLazySystemDLL("mfplat.dll")
	combase = windows.NewLazySystemDLL("combase.dll")
	ole32   = windows.NewLazySystemDLL("ole32.dll")
	user32  = windows.NewLazySystemDLL("user32.dll")

	pD3D11CreateDevice     = d3d11.NewProc("D3D11CreateDevice")
	pCreateDirect3D11FromD = d3d11.NewProc("CreateDirect3D11DeviceFromDXGIDevice")
	pRoInitialize          = combase.NewProc("RoInitialize")
	pRoGetActivationFact   = combase.NewProc("RoGetActivationFactory")
	pWindowsCreateString   = combase.NewProc("WindowsCreateString")
	pWindowsDeleteString   = combase.NewProc("WindowsDeleteString")
	pCoTaskMemFree         = ole32.NewProc("CoTaskMemFree")
	pMonitorFromPoint      = user32.NewProc("MonitorFromPoint")
)
