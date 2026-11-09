package screenpick

// Минимальный UI Automation через COM без CGO: только то, что нужно, чтобы
// найти родной диалог выбора и нажать в нем кнопки. Номера методов - по
// порядку в UIAutomationClient.h (после трех методов IUnknown).

import (
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	clsidCUIAutomation   = windows.GUID{Data1: 0xff48dba4, Data2: 0x60ef, Data3: 0x4201, Data4: [8]byte{0xaa, 0x87, 0x54, 0x10, 0x3e, 0xef, 0x59, 0x4e}}
	iidIUIAutomation     = windows.GUID{Data1: 0x30cbe57d, Data2: 0xd9d0, Data3: 0x452a, Data4: [8]byte{0xab, 0x13, 0x7a, 0xc5, 0xac, 0x48, 0x25, 0xee}}
	iidInvokePattern     = windows.GUID{Data1: 0xfb377fbe, Data2: 0x8ea6, Data3: 0x46d5, Data4: [8]byte{0x9c, 0x73, 0x64, 0x99, 0x64, 0x2d, 0x30, 0x59}}
	iidSelectionItemPatt = windows.GUID{Data1: 0xa8efa66a, Data2: 0x0fda, Data3: 0x421a, Data4: [8]byte{0x91, 0x94, 0x38, 0x02, 0x1f, 0x35, 0x78, 0xea}}
	iidTogglePattern     = windows.GUID{Data1: 0x94cf8058, Data2: 0x9b8d, Data3: 0x4ab9, Data4: [8]byte{0x8b, 0xfd, 0x4c, 0xd0, 0xa3, 0x3c, 0x8c, 0x70}}
	pCoCreateInstance    = ole32.NewProc("CoCreateInstance")
	pCoInitializeEx      = ole32.NewProc("CoInitializeEx")
	pCoUninitialize      = ole32.NewProc("CoUninitialize")
	pSysAllocString      = oleaut.NewProc("SysAllocString")
	pSysFreeString       = oleaut.NewProc("SysFreeString")
	pSysStringLen        = oleaut.NewProc("SysStringLen")
	errNoElement         = errors.New("элемент не найден")
	errCOM               = errors.New("ошибка COM")
)

const (
	treeChildren    = 2
	treeDescendants = 4

	propClassName = 30012
	typeWindow    = 50032

	patInvoke        = 10000
	patSelectionItem = 10010
	patToggle        = 10015
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
	vtbl := *(**[64]uintptr)(o.p)
	r, _, _ := syscall.SyscallN(vtbl[i], append([]uintptr{uintptr(o.p)}, args...)...)
	return r
}

func (o com) release() {
	if o.p != nil {
		o.call(2)
	}
}

func (o com) ok() bool { return o.p != nil }

type uia struct{ com }

// newUIA: вызывать в потоке, закрепленном runtime.LockOSThread.
func newUIA() (*uia, func(), error) {
	pCoInitializeEx.Call(0, 0) // COINIT_MULTITHREADED
	var p unsafe.Pointer
	r, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidCUIAutomation)), 0, 1|4, // INPROC_SERVER|LOCAL_SERVER
		uintptr(unsafe.Pointer(&iidIUIAutomation)), uintptr(unsafe.Pointer(&p)))
	if r != 0 || p == nil {
		pCoUninitialize.Call()
		return nil, nil, errCOM
	}
	u := &uia{com{p}}
	return u, func() { u.release(); pCoUninitialize.Call() }, nil
}

type element struct{ com }

func (u *uia) fromHandle(h uintptr) (element, error) {
	var p unsafe.Pointer
	if r := u.call(6, h, uintptr(unsafe.Pointer(&p))); r != 0 || p == nil {
		return element{}, errNoElement
	}
	return element{com{p}}, nil
}

// variant - VARIANT с BSTR (24 байта на x64).
type variant struct {
	vt         uint16
	_, _, _    uint16
	val, extra uintptr
}

// condition: свойство (имя или класс) равно строке.
func (u *uia) condition(prop int, value string) (com, error) {
	s, _ := windows.UTF16PtrFromString(value)
	bstr, _, _ := pSysAllocString.Call(uintptr(unsafe.Pointer(s)))
	defer pSysFreeString.Call(bstr)
	v := variant{vt: 8, val: bstr} // VT_BSTR
	var p unsafe.Pointer
	// VARIANT больше 8 байт - по соглашению x64 передается указателем на копию
	if r := u.call(23, uintptr(prop), uintptr(unsafe.Pointer(&v)), uintptr(unsafe.Pointer(&p))); r != 0 || p == nil {
		return com{}, errCOM
	}
	return com{p}, nil
}

func (u *uia) trueCondition() (com, error) {
	var p unsafe.Pointer
	if r := u.call(21, uintptr(unsafe.Pointer(&p))); r != 0 || p == nil {
		return com{}, errCOM
	}
	return com{p}, nil
}

func (e element) controlType() int {
	var v int32
	e.call(21, uintptr(unsafe.Pointer(&v)))
	return int(v)
}

func (e element) findFirst(scope int, cond com) (element, error) {
	var p unsafe.Pointer
	if r := e.call(5, uintptr(scope), uintptr(cond.p), uintptr(unsafe.Pointer(&p))); r != 0 || p == nil {
		return element{}, errNoElement
	}
	return element{com{p}}, nil
}

func (e element) findAll(scope int, cond com) []element {
	var arr unsafe.Pointer
	if r := e.call(6, uintptr(scope), uintptr(cond.p), uintptr(unsafe.Pointer(&arr))); r != 0 || arr == nil {
		return nil
	}
	a := com{arr}
	defer a.release()
	var n int32
	a.call(3, uintptr(unsafe.Pointer(&n)))
	out := make([]element, 0, n)
	for i := int32(0); i < n; i++ {
		var p unsafe.Pointer
		if a.call(4, uintptr(i), uintptr(unsafe.Pointer(&p))) == 0 && p != nil {
			out = append(out, element{com{p}})
		}
	}
	return out
}

func releaseAll(es []element) {
	for _, e := range es {
		e.release()
	}
}

// str читает строковое свойство (BSTR) и освобождает его.
func (e element) str(method int) string {
	var b *uint16
	if e.call(method, uintptr(unsafe.Pointer(&b))) != 0 || b == nil {
		return ""
	}
	defer pSysFreeString.Call(uintptr(unsafe.Pointer(b)))
	n, _, _ := pSysStringLen.Call(uintptr(unsafe.Pointer(b)))
	return windows.UTF16ToString(unsafe.Slice(b, n))
}

func (e element) name() string  { return e.str(23) }
func (e element) class() string { return e.str(30) }

func (e element) enabled() bool {
	var v int32
	e.call(28, uintptr(unsafe.Pointer(&v)))
	return v != 0
}

func (e element) pattern(id int, iid *windows.GUID) (com, error) {
	var p unsafe.Pointer
	if r := e.call(14, uintptr(id), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&p))); r != 0 || p == nil {
		return com{}, errNoElement
	}
	return com{p}, nil
}

func (e element) invoke() error {
	p, err := e.pattern(patInvoke, &iidInvokePattern)
	if err != nil {
		return err
	}
	defer p.release()
	if p.call(3) != 0 {
		return errCOM
	}
	return nil
}

func (e element) selectItem() error {
	p, err := e.pattern(patSelectionItem, &iidSelectionItemPatt)
	if err != nil {
		return err
	}
	defer p.release()
	if p.call(3) != 0 {
		return errCOM
	}
	return nil
}

// toggleOn включает переключатель, если он выключен. false - у элемента нет переключателя.
func (e element) toggleOn() bool {
	p, err := e.pattern(patToggle, &iidTogglePattern)
	if err != nil {
		return false
	}
	defer p.release()
	var st int32
	p.call(4, uintptr(unsafe.Pointer(&st)))
	if st != 1 { // ToggleState_On
		p.call(3)
	}
	return true
}
