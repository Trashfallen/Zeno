package vlan

// Связь Zeno со службой: именованный канал \\.\pipe\virtualZeno. Открыть его
// могут только SYSTEM, администраторы и пользователь, который ставил службу.
// Кадр: тип (1 байт) + длина (2 байта) + данные. Пакеты идут как есть,
// команды - JSON.

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	pipeName = `\\.\pipe\virtualZeno`
	Proto    = 1 // меняется при несовместимых правках службы - тогда ее переставляют

	fHello  = 'H' // Zeno -> служба: {proto}; ответ fHello: {proto, version}
	fUp     = 'U' // Zeno -> служба: {ip}; ответ fStatus
	fDown   = 'D' // Zeno -> служба; ответ fStatus
	fPacket = 'P' // пакет IPv4 в обе стороны
	fStatus = 'S' // служба -> Zeno: {up, ip, error}
)

type conn struct {
	f  *os.File
	mu sync.Mutex // запись кадров из разных горутин
}

func (c *conn) write(t byte, data []byte) error {
	if len(data) > 0xffff {
		return errors.New("кадр больше 64 КБ")
	}
	b := make([]byte, 3+len(data))
	b[0] = t
	binary.BigEndian.PutUint16(b[1:], uint16(len(data)))
	copy(b[3:], data)
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.f.Write(b)
	return err
}

func (c *conn) read() (byte, []byte, error) {
	var h [3]byte
	if _, err := io.ReadFull(c.f, h[:]); err != nil {
		return 0, nil, err
	}
	b := make([]byte, binary.BigEndian.Uint16(h[1:]))
	if _, err := io.ReadFull(c.f, b); err != nil {
		return 0, nil, err
	}
	return h[0], b, nil
}

func (c *conn) Close() error { return c.f.Close() }

// listenPipe создает экземпляр канала и ждет подключения. Ручка асинхронная:
// os.NewFile отдает ее планировщику Go, чтение и запись идут параллельно.
func listenPipe(sddl string) (*conn, error) {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, err
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	name, _ := windows.UTF16PtrFromString(pipeName)
	h, err := windows.CreateNamedPipe(name,
		windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		windows.PIPE_UNLIMITED_INSTANCES, 256<<10, 256<<10, 0, sa)
	if err != nil {
		return nil, err
	}
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	defer windows.CloseHandle(ev)
	ov := &windows.Overlapped{HEvent: ev}
	err = windows.ConnectNamedPipe(h, ov)
	if err == windows.ERROR_IO_PENDING {
		var n uint32
		err = windows.GetOverlappedResult(h, ov, &n, true)
	}
	if err != nil && err != windows.ERROR_PIPE_CONNECTED {
		windows.CloseHandle(h)
		return nil, err
	}
	return &conn{f: os.NewFile(uintptr(h), pipeName)}, nil
}

// dialPipe подключается к службе.
func dialPipe(timeout time.Duration) (*conn, error) {
	name, _ := windows.UTF16PtrFromString(pipeName)
	deadline := time.Now().Add(timeout)
	for {
		h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
			windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
		if err == nil {
			return &conn{f: os.NewFile(uintptr(h), pipeName)}, nil
		}
		if (err != windows.ERROR_PIPE_BUSY && err != windows.ERROR_FILE_NOT_FOUND) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
