package vlan

// Wintun - сетевой адаптер третьего уровня от WireGuard (wintun.net).
// wintun.dll подписан WireGuard LLC и ставит свой драйвер сам; вызываем его
// напрямую через syscall, без CGO. Работать с адаптером может только
// администратор или SYSTEM - поэтому все это живет в службе (service.go).

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// wintun.dll 0.14.1 (amd64) без изменений, из архива wintun.net/builds
//
//go:embed wintun/wintun.dll
var wintunDLL []byte

// adapterGUID постоянный: Windows узнает ту же сеть после перезапуска службы
var adapterGUID = windows.GUID{Data1: 0x7a3e2c91, Data2: 0x5d04, Data3: 0x4b6f, Data4: [8]byte{0x9e, 0x11, 0x2a, 0x77, 0x0c, 0x5b, 0xd3, 0x48}}

type wintunAPI struct {
	create, open, close, luid, start, end, readEvent, receive, release, allocate, send, version *windows.Proc
}

func loadWintun(dir string) (*wintunAPI, error) {
	dll, err := windows.LoadDLL(filepath.Join(dir, "wintun.dll"))
	if err != nil {
		return nil, fmt.Errorf("wintun.dll: %w", err)
	}
	w := &wintunAPI{}
	for _, p := range []struct {
		dst  **windows.Proc
		name string
	}{
		{&w.create, "WintunCreateAdapter"}, {&w.open, "WintunOpenAdapter"}, {&w.close, "WintunCloseAdapter"},
		{&w.luid, "WintunGetAdapterLUID"}, {&w.start, "WintunStartSession"}, {&w.end, "WintunEndSession"},
		{&w.readEvent, "WintunGetReadWaitEvent"}, {&w.receive, "WintunReceivePacket"},
		{&w.release, "WintunReleaseReceivePacket"}, {&w.allocate, "WintunAllocateSendPacket"},
		{&w.send, "WintunSendPacket"}, {&w.version, "WintunGetRunningDriverVersion"},
	} {
		if *p.dst, err = dll.FindProc(p.name); err != nil {
			return nil, err
		}
	}
	return w, nil
}

// writeWintunDLL кладет вшитый wintun.dll рядом со службой.
func writeWintunDLL(dir string) error {
	return os.WriteFile(filepath.Join(dir, "wintun.dll"), wintunDLL, 0o644)
}

// ptr превращает адрес из syscall в указатель так, что go vet не ругается
func ptr(r uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&r)) }

type adapter struct {
	w *wintunAPI
	h uintptr
}

// openAdapter берет адаптер, оставшийся от прошлого запуска, или создает новый.
func (w *wintunAPI) openAdapter(name string) (*adapter, error) {
	n, _ := windows.UTF16PtrFromString(name)
	if h, _, _ := w.open.Call(uintptr(unsafe.Pointer(n))); h != 0 {
		return &adapter{w: w, h: h}, nil
	}
	tt, _ := windows.UTF16PtrFromString("Zeno")
	h, _, err := w.create.Call(uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(tt)), uintptr(unsafe.Pointer(&adapterGUID)))
	if h == 0 {
		return nil, fmt.Errorf("создание адаптера: %w", err)
	}
	return &adapter{w: w, h: h}, nil
}

// Close убирает адаптер из системы (Wintun удаляет созданные им адаптеры).
func (a *adapter) Close() { a.w.close.Call(a.h) }

// Index - номер интерфейса для netsh.
func (a *adapter) Index() (uint32, error) {
	var luid uint64
	a.w.luid.Call(a.h, uintptr(unsafe.Pointer(&luid)))
	var idx uint32
	r, _, _ := procLuidToIndex.Call(uintptr(unsafe.Pointer(&luid)), uintptr(unsafe.Pointer(&idx)))
	if r != 0 {
		return 0, fmt.Errorf("номер интерфейса: %w", syscall.Errno(r))
	}
	return idx, nil
}

var procLuidToIndex = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("ConvertInterfaceLuidToIndex")

type session struct {
	w    *wintunAPI
	h    uintptr
	wait windows.Handle
}

func (a *adapter) Start() (*session, error) {
	h, _, err := a.w.start.Call(a.h, 0x400000) // кольцо 4 МБ
	if h == 0 {
		return nil, fmt.Errorf("сессия адаптера: %w", err)
	}
	ev, _, _ := a.w.readEvent.Call(h)
	return &session{w: a.w, h: h, wait: windows.Handle(ev)}, nil
}

func (s *session) End() { s.w.end.Call(s.h) }

var errNoPacket = errors.New("пакетов нет")

// Receive отдает копию очередного пакета из системы; errNoPacket - ждать wait.
func (s *session) Receive() ([]byte, error) {
	var n uint32
	r, _, err := s.w.receive.Call(s.h, uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		if err == windows.ERROR_NO_MORE_ITEMS {
			return nil, errNoPacket
		}
		return nil, err
	}
	pkt := make([]byte, n)
	copy(pkt, unsafe.Slice((*byte)(ptr(r)), n))
	s.w.release.Call(s.h, r)
	return pkt, nil
}

// Send отдает пакет системе; кольцо переполнено - пакет теряется, как в сети.
func (s *session) Send(pkt []byte) {
	r, _, _ := s.w.allocate.Call(s.h, uintptr(len(pkt)))
	if r == 0 {
		return
	}
	copy(unsafe.Slice((*byte)(ptr(r)), len(pkt)), pkt)
	s.w.send.Call(s.h, r)
}
