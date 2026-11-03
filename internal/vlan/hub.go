package vlan

// Сторона Zeno: подключение к службе и соединения с друзьями. С каждым другом
// отдельное соединение pion с каналом данных без порядка и без повторов -
// потерянный пакет теряется, как в настоящей сети, и не тормозит остальные.
// Договариваются о нем страницы по уже открытому каналу данных: lan-offer ->
// lan-answer (как nscreen для трансляции).

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"
	"unsafe"

	"github.com/pion/webrtc/v4"
	"golang.org/x/sys/windows"
)

// ICEServer - STUN/TURN, как в настройках страницы.
type ICEServer struct {
	URLs                 []string
	Username, Credential string
}

// служебные сообщения в канале: первый байт не 4 (IPv4) и не 6 (IPv6)
const (
	msgPing = 0x01 // + 8 байт: время отправки по mono() отправителя
	msgPong = 0x02
)

const maxBuffered = 1 << 20 // больше в очереди к другу - новые пакеты выбрасываем

type Hub struct {
	me  netip.Addr
	log func(string, ...any)
	// toTun - куда отдавать пакеты от друзей (служба; в тестах - своя функция)
	toTun func([]byte)

	mu      sync.Mutex
	want    bool
	svc     *conn
	up      bool
	svcErr  string
	svcVer  string
	links   map[string]*link
	byAddr  map[netip.Addr]*link
	wake    chan struct{}
	stopped chan struct{}
}

// NewHub: myID - свой публичный ключ, от него зависит адрес.
func NewHub(myID string, logf func(string, ...any)) *Hub {
	h := &Hub{me: Addr(myID), log: logf, links: map[string]*link{}, byAddr: map[netip.Addr]*link{},
		wake: make(chan struct{}, 1), stopped: make(chan struct{})}
	h.toTun = h.sendToService
	go h.keep()
	return h
}

func (h *Hub) Me() netip.Addr { return h.me }

// SetWant: true - virtualZeno запущен хотя бы с одним другом, адрес нужен.
func (h *Hub) SetWant(on bool) {
	h.mu.Lock()
	changed := h.want != on
	h.want = on
	c := h.svc
	if !on {
		h.svc, h.up = nil, false
	}
	h.mu.Unlock()
	if !on && c != nil {
		c.Close() // служба сама опустит сеть
	}
	if changed {
		h.poke()
	}
}

func (h *Hub) poke() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// keep держит подключение к службе, пока сеть нужна.
func (h *Hub) keep() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-h.stopped:
			return
		case <-h.wake:
		case <-t.C:
		}
		h.mu.Lock()
		need := h.want && h.svc == nil
		h.mu.Unlock()
		if need {
			if err := h.connect(); err != nil {
				h.mu.Lock()
				if h.svcErr != err.Error() {
					h.log("virtualZeno: служба: %v", err)
				}
				h.svcErr = err.Error()
				h.mu.Unlock()
			}
		}
	}
}

func (h *Hub) connect() error {
	switch ServiceState() {
	case "missing":
		return errors.New("служба не установлена")
	case "stopped":
		if err := StartService(); err != nil {
			return fmt.Errorf("запуск службы: %w", err)
		}
	}
	c, err := dialPipe(5 * time.Second)
	if err != nil {
		return fmt.Errorf("канал службы: %w", err)
	}
	hello, _ := json.Marshal(map[string]any{"proto": Proto})
	if err := c.write(fHello, hello); err != nil {
		c.Close()
		return err
	}
	t, b, err := c.read()
	if err != nil || t != fHello {
		c.Close()
		return fmt.Errorf("служба не ответила: %v", err)
	}
	var hi struct {
		Proto   int
		Version string
	}
	_ = json.Unmarshal(b, &hi)
	if hi.Proto != Proto {
		c.Close()
		return fmt.Errorf("служба версии %s устарела, нужно обновить", hi.Version)
	}
	up, _ := json.Marshal(map[string]any{"ip": h.me.String()})
	if err := c.write(fUp, up); err != nil {
		c.Close()
		return err
	}
	h.mu.Lock()
	if !h.want {
		h.mu.Unlock()
		c.Close()
		return nil
	}
	h.svc, h.svcVer = c, hi.Version
	h.mu.Unlock()
	go h.readService(c)
	return nil
}

func (h *Hub) readService(c *conn) {
	for {
		t, b, err := c.read()
		if err != nil {
			break
		}
		switch t {
		case fPacket:
			h.fromTun(b)
		case fStatus:
			var st struct {
				Up    bool
				IP    string
				Error string
			}
			_ = json.Unmarshal(b, &st)
			h.mu.Lock()
			h.up, h.svcErr = st.Up, st.Error
			h.mu.Unlock()
			if st.Error != "" {
				h.log("virtualZeno: служба: %s", st.Error)
			} else if st.Up {
				h.log("virtualZeno: сеть поднята, адрес %s", st.IP)
			}
		}
	}
	c.Close()
	h.mu.Lock()
	if h.svc == c {
		h.svc, h.up = nil, false
	}
	want := h.want
	h.mu.Unlock()
	if want {
		h.log("virtualZeno: связь со службой потеряна")
		h.poke()
	}
}

func (h *Hub) sendToService(pkt []byte) {
	h.mu.Lock()
	c := h.svc
	h.mu.Unlock()
	if c != nil {
		_ = c.write(fPacket, pkt)
	}
}

// fromTun: пакет из системы - другу по адресу назначения или всем сразу.
func (h *Hub) fromTun(pkt []byte) {
	src, dst, ok := header(pkt)
	if !ok || src != h.me {
		return
	}
	h.mu.Lock()
	var to []*link
	if toAll(dst) {
		for _, l := range h.byAddr {
			to = append(to, l)
		}
	} else if l := h.byAddr[dst]; l != nil {
		to = append(to, l)
	}
	h.mu.Unlock()
	for _, l := range to {
		l.send(pkt)
	}
}

// fromPeer: пакет от друга - в систему, если он честный.
func (h *Hub) fromPeer(l *link, pkt []byte) {
	src, dst, ok := header(pkt)
	if !ok || src != l.addr || (dst != h.me && !toAll(dst)) {
		return
	}
	l.mu.Lock()
	l.rx += uint64(len(pkt))
	l.mu.Unlock()
	h.toTun(pkt)
}

// ---------- соединения с друзьями ----------

type link struct {
	hub    *Hub
	friend string
	addr   netip.Addr
	pc     *webrtc.PeerConnection

	mu     sync.Mutex
	dc     *webrtc.DataChannel
	state  string
	rtt    time.Duration
	pongAt time.Time
	rx, tx uint64
	closed bool
}

func newPC(ice []ICEServer) (*webrtc.PeerConnection, error) {
	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	var servers []webrtc.ICEServer
	for _, x := range ice {
		if len(x.URLs) > 0 {
			servers = append(servers, webrtc.ICEServer{URLs: x.URLs, Username: x.Username, Credential: x.Credential})
		}
	}
	return api.NewPeerConnection(webrtc.Configuration{ICEServers: servers})
}

func (h *Hub) newLink(friend string, ice []ICEServer) (*link, error) {
	addr := Addr(friend)
	if addr == h.me {
		return nil, errors.New("у друга тот же адрес virtualZeno, что у вас - так бывает раз на 65 тысяч пар")
	}
	pc, err := newPC(ice)
	if err != nil {
		return nil, err
	}
	l := &link{hub: h, friend: friend, addr: addr, pc: pc, state: "connecting"}
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		l.mu.Lock()
		s := st.String()
		if st == webrtc.PeerConnectionStateConnected && (l.dc == nil || l.dc.ReadyState() != webrtc.DataChannelStateOpen) {
			s = "connecting" // канал еще открывается - connected скажет OnOpen
		}
		if !l.closed {
			l.state = s
		}
		l.mu.Unlock()
	})
	h.Close(friend)
	h.mu.Lock()
	h.links[friend] = l
	h.mu.Unlock()
	return l, nil
}

func (l *link) bind(dc *webrtc.DataChannel) {
	l.mu.Lock()
	l.dc = dc
	l.mu.Unlock()
	dc.OnOpen(func() {
		l.mu.Lock()
		l.state = "connected"
		l.mu.Unlock()
		h := l.hub
		h.mu.Lock()
		if h.links[l.friend] == l {
			h.byAddr[l.addr] = l
		}
		h.mu.Unlock()
		h.log("virtualZeno: связь с %s установлена", l.addr)
		go l.pinger()
	})
	dc.OnClose(func() {
		l.mu.Lock()
		if !l.closed {
			l.state = "closed"
		}
		l.mu.Unlock()
		l.hub.unroute(l)
	})
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		b := m.Data
		if len(b) == 0 {
			return
		}
		switch b[0] {
		case msgPing:
			if len(b) == 9 {
				r := append([]byte{msgPong}, b[1:]...)
				_ = dc.Send(r)
			}
		case msgPong:
			if len(b) == 9 {
				sent := time.Duration(binary.BigEndian.Uint64(b[1:]))
				l.mu.Lock()
				l.rtt, l.pongAt = mono()-sent, time.Now()
				l.mu.Unlock()
			}
		default:
			l.hub.fromPeer(l, b)
		}
	})
}

var (
	procQPC = windows.NewLazySystemDLL("kernel32.dll").NewProc("QueryPerformanceCounter")
	procQPF = windows.NewLazySystemDLL("kernel32.dll").NewProc("QueryPerformanceFrequency")
	qpcFreq int64
	qpcOnce sync.Once
)

// mono - точные часы для пинга: часы Go на Windows шагают по 0,5-15 мс,
// а пинг в локальной сети бывает меньше миллисекунды.
func mono() time.Duration {
	qpcOnce.Do(func() { procQPF.Call(uintptr(unsafe.Pointer(&qpcFreq))) })
	var c int64
	procQPC.Call(uintptr(unsafe.Pointer(&c)))
	if qpcFreq == 0 {
		return time.Duration(time.Now().UnixNano())
	}
	return time.Duration(c/qpcFreq*1e9 + c%qpcFreq*1e9/qpcFreq)
}

func (l *link) send(pkt []byte) {
	l.mu.Lock()
	dc := l.dc
	l.mu.Unlock()
	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen || dc.BufferedAmount() > maxBuffered {
		return
	}
	if dc.Send(pkt) == nil {
		l.mu.Lock()
		l.tx += uint64(len(pkt))
		l.mu.Unlock()
	}
}

// pinger: свой пинг раз в 2 с; 10 с без ответа - связь считаем потерянной.
func (l *link) pinger() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	l.mu.Lock()
	l.pongAt = time.Now()
	l.mu.Unlock()
	for range t.C {
		l.mu.Lock()
		dc, closed, silent := l.dc, l.closed, time.Since(l.pongAt)
		if silent > 10*time.Second && l.state == "connected" {
			l.state = "failed"
		}
		l.mu.Unlock()
		if closed || dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
			return
		}
		b := make([]byte, 9)
		b[0] = msgPing
		binary.BigEndian.PutUint64(b[1:], uint64(mono()))
		_ = dc.Send(b)
	}
}

func (h *Hub) unroute(l *link) {
	h.mu.Lock()
	if h.byAddr[l.addr] == l {
		delete(h.byAddr, l.addr)
	}
	h.mu.Unlock()
}

func dcInit() *webrtc.DataChannelInit {
	ordered, retr := false, uint16(0)
	return &webrtc.DataChannelInit{Ordered: &ordered, MaxRetransmits: &retr}
}

// gather ждет сбора кандидатов: страница передает SDP целиком, без trickle.
func gather(pc *webrtc.PeerConnection, d webrtc.SessionDescription) (string, error) {
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(d); err != nil {
		return "", err
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second): // отдаем, что успели собрать
	}
	return pc.LocalDescription().SDP, nil
}

// Offer - начало соединения с другом (делает тот, у кого ключ меньше).
func (h *Hub) Offer(friend string, ice []ICEServer) (string, error) {
	l, err := h.newLink(friend, ice)
	if err != nil {
		return "", err
	}
	dc, err := l.pc.CreateDataChannel("vlan", dcInit())
	if err != nil {
		h.Close(friend)
		return "", err
	}
	l.bind(dc)
	o, err := l.pc.CreateOffer(nil)
	if err == nil {
		var sdp string
		if sdp, err = gather(l.pc, o); err == nil {
			return sdp, nil
		}
	}
	h.Close(friend)
	return "", err
}

// Accept - ответ на предложение друга.
func (h *Hub) Accept(friend, sdp string, ice []ICEServer) (string, error) {
	l, err := h.newLink(friend, ice)
	if err != nil {
		return "", err
	}
	l.pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() == "vlan" {
			l.bind(dc)
		}
	})
	err = l.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp})
	if err == nil {
		var a webrtc.SessionDescription
		if a, err = l.pc.CreateAnswer(nil); err == nil {
			var out string
			if out, err = gather(l.pc, a); err == nil {
				return out, nil
			}
		}
	}
	h.Close(friend)
	return "", err
}

// Answer принимает ответ друга на наше предложение.
func (h *Hub) Answer(friend, sdp string) error {
	h.mu.Lock()
	l := h.links[friend]
	h.mu.Unlock()
	if l == nil {
		return errors.New("предложение virtualZeno уже закрыто")
	}
	return l.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp})
}

// Close рвет соединение с другом.
func (h *Hub) Close(friend string) {
	h.mu.Lock()
	l := h.links[friend]
	delete(h.links, friend)
	if l != nil && h.byAddr[l.addr] == l {
		delete(h.byAddr, l.addr)
	}
	h.mu.Unlock()
	if l != nil {
		l.mu.Lock()
		l.closed, l.state = true, "closed"
		l.mu.Unlock()
		_ = l.pc.Close()
	}
}

// Stop - выход из Zeno: все соединения и подключение к службе.
func (h *Hub) Stop() {
	h.mu.Lock()
	ids := make([]string, 0, len(h.links))
	for id := range h.links {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.Close(id)
	}
	h.SetWant(false)
	select {
	case <-h.stopped:
	default:
		close(h.stopped)
	}
}

// LinkStatus - для страницы.
type LinkStatus struct {
	Addr  string  `json:"addr"`
	State string  `json:"state"` // none, connecting, connected, failed, closed...
	RTT   float64 `json:"rtt"`   // мс, 0 - еще не мерили
	RX    uint64  `json:"rx"`
	TX    uint64  `json:"tx"`
}

type Status struct {
	Service string                `json:"service"` // missing, running, stopped...
	Up      bool                  `json:"up"`      // адрес на адаптере есть
	Error   string                `json:"error"`
	Me      string                `json:"me"`
	Version string                `json:"version"` // версия Zeno, из которой ставили службу
	Links   map[string]LinkStatus `json:"links"`
}

// Status: friends - чьи адреса показать, даже если соединения нет.
func (h *Hub) Status(friends []string) Status {
	st := Status{Service: ServiceState(), Me: h.me.String(), Links: map[string]LinkStatus{}}
	h.mu.Lock()
	st.Up, st.Error, st.Version = h.up, h.svcErr, h.svcVer
	links := make(map[string]*link, len(h.links))
	for id, l := range h.links {
		links[id] = l
	}
	h.mu.Unlock()
	for _, id := range friends {
		st.Links[id] = LinkStatus{Addr: Addr(id).String(), State: "none"}
	}
	for id, l := range links {
		l.mu.Lock()
		st.Links[id] = LinkStatus{Addr: l.addr.String(), State: l.state, RTT: float64(l.rtt.Microseconds()) / 1000, RX: l.rx, TX: l.tx}
		l.mu.Unlock()
	}
	return st
}
