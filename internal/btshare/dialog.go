package btshare

// Системный диалог выбора файла или папки (IFileOpenDialog) - тот же, что в
// проводнике (из TarBitTorrent). Свой вызов COM: в готовой библиотеке методы
// получали неполные аргументы и программа падала.

import (
	"errors"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modOle32             = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx   = modOle32.NewProc("CoInitializeEx")
	procCoUninitialize   = modOle32.NewProc("CoUninitialize")
	procCoCreateInstance = modOle32.NewProc("CoCreateInstance")
	procCoTaskMemFree    = modOle32.NewProc("CoTaskMemFree")

	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidFileOpenDialog   = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
)

// Номера методов в таблице COM-интерфейсов.
const (
	vRelease        = 2
	vShow           = 3
	vSetOptions     = 9
	vGetOptions     = 10
	vSetTitle       = 17
	vGetResult      = 20
	vGetDisplayName = 5 // IShellItem

	fosPickFolders     = 0x20
	fosForceFileSystem = 0x40
	fosNoChangeDir     = 0x8
	sigdnFileSysPath   = 0x80058000
	hresultCancelled   = 0x800704C7
	clsctxInprocServer = 1
	coinitApartment    = 0x2
	coinitDisableOle1  = 0x4
)

// comCall вызывает метод n COM-объекта obj (первый аргумент - сам объект).
func comCall(obj unsafe.Pointer, n int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, uintptr(n)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return r
}

func hrErr(what string, hr uintptr) error {
	return errors.New(what + ": " + windows.Errno(hr&0xFFFF).Error())
}

// Pick: folder - выбрать папку, иначе файл. Пустая строка без ошибки -
// пользователь нажал «Отмена».
func Pick(owner uintptr, folder bool, title string) (string, error) {
	type result struct {
		path string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		// диалог живет в одном потоке от создания до закрытия
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		p, err := pickOnThread(owner, folder, title)
		ch <- result{p, err}
	}()
	r := <-ch
	return r.path, r.err
}

func pickOnThread(owner uintptr, folder bool, title string) (string, error) {
	if hr, _, _ := procCoInitializeEx.Call(0, coinitApartment|coinitDisableOle1); int32(hr) >= 0 {
		defer procCoUninitialize.Call()
	}
	var dlg unsafe.Pointer
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidFileOpenDialog)), uintptr(unsafe.Pointer(&dlg)))
	if int32(hr) < 0 || dlg == nil {
		return "", hrErr("диалог выбора", hr)
	}
	defer comCall(dlg, vRelease)

	var opts uint32
	comCall(dlg, vGetOptions, uintptr(unsafe.Pointer(&opts)))
	opts |= fosForceFileSystem | fosNoChangeDir
	if folder {
		opts |= fosPickFolders
	}
	comCall(dlg, vSetOptions, uintptr(opts))
	t16, _ := windows.UTF16PtrFromString(title)
	comCall(dlg, vSetTitle, uintptr(unsafe.Pointer(t16)))
	runtime.KeepAlive(t16)

	hr = comCall(dlg, vShow, owner)
	if uint32(hr) == hresultCancelled {
		return "", nil
	}
	if int32(hr) < 0 {
		return "", hrErr("диалог выбора", hr)
	}
	var item unsafe.Pointer
	if hr := comCall(dlg, vGetResult, uintptr(unsafe.Pointer(&item))); int32(hr) < 0 || item == nil {
		return "", hrErr("выбор", hr)
	}
	defer comCall(item, vRelease)
	var name *uint16
	if hr := comCall(item, vGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&name))); int32(hr) < 0 || name == nil {
		return "", errors.New("выбрано не на диске")
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(name)))
	return windows.UTF16PtrToString(name), nil
}
