package vlan

// Служба virtualZeno (SYSTEM): держит адаптер Wintun и гоняет пакеты между
// ним и Zeno по именованному каналу. Сама в сеть не ходит - соединения с
// друзьями делает Zeno (hub.go). Адаптер живет, пока работает служба, поэтому
// переживает перезапуск Zeno; адрес и маршрут есть, только пока Zeno
// подключен и просил их (fUp).

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

const (
	ServiceName = "virtualZeno"
	AdapterName = "virtualZeno"
)

// Version - версия Zeno, из которой поставлена служба (задает main).
var Version = "dev"

// RunService - вход службы (Zeno.exe -lan-service <SID владельца>).
func RunService(owner string) error {
	return svc.Run(ServiceName, &handler{owner: owner})
}

type handler struct{ owner string }

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	d := newDaemon(h.owner)
	go d.serve()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for c := range req {
		switch c.Cmd {
		case svc.Interrogate:
			st <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			st <- svc.Status{State: svc.StopPending}
			d.close()
			return false, 0
		}
	}
	return false, 0
}

type daemon struct {
	dir  string
	sddl string
	log  *log.Logger
	quit chan struct{}
	mu   sync.Mutex
	w    *wintunAPI
	ad   *adapter
	idx  uint32
	ses  *session
	stop windows.Handle // событие: читателю сессии пора выйти
	done chan struct{}  // читатель вышел
	ip   netip.Addr
	cli  *conn
	out  atomic.Pointer[conn] // тот же cli для pump: он не берет mu, его ждут под mu
}

func newDaemon(owner string) *daemon {
	exe, _ := os.Executable()
	d := &daemon{dir: filepath.Dir(exe), quit: make(chan struct{})}
	name := filepath.Join(d.dir, "virtualZeno.log")
	if fi, err := os.Stat(name); err == nil && fi.Size() > 1<<20 {
		_ = os.Rename(name, name+".1")
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		f = os.Stderr
	}
	d.log = log.New(f, "", log.LstdFlags)
	// SYSTEM и администраторы - все, владелец - чтение и запись
	d.sddl = "D:P(A;;GA;;;SY)(A;;GA;;;BA)"
	if _, err := windows.StringToSid(owner); err == nil {
		d.sddl += "(A;;GRGW;;;" + owner + ")"
	}
	d.log.Printf("служба запущена, владелец %s", owner)
	return d
}

func (d *daemon) serve() {
	for {
		c, err := listenPipe(d.sddl)
		select {
		case <-d.quit:
			if c != nil {
				c.Close()
			}
			return
		default:
		}
		if err != nil {
			d.log.Printf("канал: %v", err)
			select {
			case <-d.quit:
				return
			case <-time.After(time.Second):
			}
			continue
		}
		d.mu.Lock()
		old := d.cli
		d.cli = c
		d.out.Store(c)
		d.mu.Unlock()
		if old != nil {
			old.Close() // новый Zeno (перезапуск) вытесняет старое подключение
		}
		go d.client(c)
	}
}

func (d *daemon) client(c *conn) {
	defer func() {
		c.Close()
		d.mu.Lock()
		mine := d.cli == c
		if mine {
			d.cli = nil
			d.out.Store(nil)
			d.downLocked()
		}
		d.mu.Unlock()
	}()
	for {
		t, b, err := c.read()
		if err != nil {
			return
		}
		switch t {
		case fPacket:
			d.mu.Lock()
			if s := d.ses; s != nil && d.cli == c {
				s.Send(b)
			}
			d.mu.Unlock()
		case fHello:
			v, _ := json.Marshal(map[string]any{"proto": Proto, "version": Version})
			_ = c.write(fHello, v)
		case fUp:
			var req struct{ IP string }
			_ = json.Unmarshal(b, &req)
			d.mu.Lock()
			ip, err := netip.ParseAddr(req.IP)
			if err != nil || !subnet.Contains(ip) {
				err = fmt.Errorf("адрес %q не из %s", req.IP, netPrefix)
			} else if d.cli == c {
				err = d.upLocked(ip)
			}
			d.statusLocked(c, err)
			d.mu.Unlock()
		case fDown:
			d.mu.Lock()
			if d.cli == c {
				d.downLocked()
			}
			d.statusLocked(c, nil)
			d.mu.Unlock()
		}
	}
}

func (d *daemon) statusLocked(c *conn, err error) {
	st := map[string]any{"up": d.ses != nil, "ip": ""}
	if d.ses != nil {
		st["ip"] = d.ip.String()
	}
	if err != nil {
		st["error"] = err.Error()
	}
	b, _ := json.Marshal(st)
	_ = c.write(fStatus, b)
}

// upLocked: адаптер (создается один раз), сессия, адрес, MTU и метрика.
func (d *daemon) upLocked(ip netip.Addr) error {
	if d.ses != nil && d.ip == ip {
		return nil
	}
	d.downLocked()
	if d.w == nil {
		w, err := loadWintun(d.dir)
		if err != nil {
			return err
		}
		d.w = w
	}
	if d.ad == nil {
		ad, err := d.w.openAdapter(AdapterName)
		if err != nil {
			return err
		}
		idx, err := ad.Index()
		if err != nil {
			ad.Close()
			return err
		}
		d.ad, d.idx = ad, idx
		if v, _, _ := d.w.version.Call(); v != 0 {
			d.log.Printf("адаптер готов, интерфейс %d, драйвер Wintun %d.%d", idx, v>>16, v&0xffff)
		}
	}
	ses, err := d.ad.Start()
	if err != nil {
		return err
	}
	stop, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		ses.End()
		return err
	}
	d.ses, d.stop, d.done, d.ip = ses, stop, make(chan struct{}), ip
	go d.pump(ses, stop, d.done)
	ifc := strconv.Itoa(int(d.idx))
	// метрика 1: общий широковещательный адрес 255.255.255.255 Windows шлет
	// через интерфейс с наименьшей метрикой - иначе игры не увидят друга.
	// dadtransmits=0: без проверки адреса на дубликаты (как у WireGuard) -
	// иначе адрес несколько секунд «предварительный» и не работает
	for _, args := range [][]string{
		{"interface", "ipv4", "set", "interface", ifc, "metric=1", "dadtransmits=0", "routerdiscovery=disabled"},
		{"interface", "ipv4", "set", "subinterface", ifc, "mtu=" + strconv.Itoa(MTU), "store=active"},
		{"interface", "ipv4", "set", "address", "name=" + ifc, "source=static", "address=" + ip.String(), "mask=" + netMask},
	} {
		if err := d.netsh(args...); err != nil {
			d.downLocked()
			return err
		}
	}
	if !addrReady(ip, 5*time.Second) {
		d.log.Printf("адрес %s не заработал за 5 с", ip)
	}
	d.log.Printf("сеть поднята: %s/16", ip)
	return nil
}

// addrReady ждет, пока на адрес можно повесить сокет (адрес рабочий).
func addrReady(ip netip.Addr, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if c, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, 0))); err == nil {
			c.Close()
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// downLocked закрывает сессию (адаптер без связи - маршрута нет) и снимает адрес.
func (d *daemon) downLocked() {
	if d.ses == nil {
		return
	}
	windows.SetEvent(d.stop)
	<-d.done
	windows.CloseHandle(d.stop)
	d.ses.End()
	d.ses = nil
	_ = d.netsh("interface", "ipv4", "delete", "address", "name="+strconv.Itoa(int(d.idx)), "address="+d.ip.String())
	d.log.Printf("сеть опущена")
}

// pump: пакеты из системы -> Zeno.
func (d *daemon) pump(ses *session, stop windows.Handle, done chan struct{}) {
	defer close(done)
	for {
		pkt, err := ses.Receive()
		if err == errNoPacket {
			ev, _ := windows.WaitForMultipleObjects([]windows.Handle{ses.wait, stop}, false, windows.INFINITE)
			if ev != windows.WAIT_OBJECT_0 {
				return
			}
			continue
		}
		if err != nil {
			d.log.Printf("чтение адаптера: %v", err)
			return
		}
		if _, _, ok := header(pkt); !ok {
			continue // IPv6 и мусор - сеть только IPv4
		}
		if c := d.out.Load(); c != nil {
			_ = c.write(fPacket, pkt)
		}
		select {
		case <-d.quit:
			return
		default:
		}
	}
}

func (d *daemon) netsh(args ...string) error {
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "netsh.exe"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.CombinedOutput()
	if err != nil {
		d.log.Printf("netsh %v: %v %s", args, err, out)
		return fmt.Errorf("netsh %s: %v", args[2], err)
	}
	return nil
}

func (d *daemon) close() {
	close(d.quit)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.downLocked()
	if d.cli != nil {
		d.cli.Close()
	}
	if d.ad != nil {
		d.ad.Close()
		d.ad = nil
	}
	// ждущий подключения канал будит наше же подключение
	if c, err := dialPipe(0); err == nil {
		c.Close()
	}
	d.log.Printf("служба остановлена")
}
