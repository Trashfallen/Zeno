package btshare

// Транспорт BitTorrent через Zeno: с каждым другом отдельное соединение pion
// (договариваются страницы по своему каналу данных: bt-link-offer ->
// bt-link-answer). Каждое соединение движка - свой канал данных «bt» в нем,
// надежный и по порядку, отвязанный от событий pion и обернутый в net.Conn.
// Адрес друга для движка - "zeno:<ID>". NAT этому пути не мешает: если друг
// в сети Zeno, торрент дойдет.

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v4"
)

const (
	addrPrefix = "zeno:"
	chunk      = 16 << 10 // кусок записи в канал: SCTP не любит большие сообщения
)

// ICEServer - STUN/TURN, как в настройках страницы.
type ICEServer struct {
	URLs                 []string
	Username, Credential string
}

// zAddr - адрес друга для движка.
type zAddr string

func (a zAddr) Network() string { return "zeno" }
func (a zAddr) String() string  { return addrPrefix + string(a) }

type links struct {
	mu     sync.Mutex
	m      map[string]*link
	accept chan net.Conn
	done   chan struct{}
	once   sync.Once
}

type link struct {
	friend string
	pc     *webrtc.PeerConnection
	mu     sync.Mutex
	state  string
}

func newLinks() *links {
	return &links{m: map[string]*link{}, accept: make(chan net.Conn, 16), done: make(chan struct{})}
}

func newAPI() *webrtc.API {
	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.DetachDataChannels()
	se.EnableDataChannelBlockWrite(true) // запись ждет, пока друг примет: память не растет
	se.SetSCTPMaxReceiveBufferSize(8 << 20)
	return webrtc.NewAPI(webrtc.WithSettingEngine(se))
}

func (ls *links) open(friend string, ice []ICEServer) (*link, error) {
	var servers []webrtc.ICEServer
	for _, x := range ice {
		if len(x.URLs) > 0 {
			servers = append(servers, webrtc.ICEServer{URLs: x.URLs, Username: x.Username, Credential: x.Credential})
		}
	}
	pc, err := newAPI().NewPeerConnection(webrtc.Configuration{ICEServers: servers})
	if err != nil {
		return nil, err
	}
	l := &link{friend: friend, pc: pc, state: "connecting"}
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		l.mu.Lock()
		l.state = st.String()
		l.mu.Unlock()
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != "bt" {
			return
		}
		dc.OnOpen(func() {
			raw, err := dc.DetachWithDeadline()
			if err != nil {
				return
			}
			select {
			case ls.accept <- newConn(raw, friend):
			case <-ls.done:
				raw.Close()
			}
		})
	})
	ls.close(friend)
	ls.mu.Lock()
	ls.m[friend] = l
	ls.mu.Unlock()
	return l, nil
}

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

func (ls *links) offer(friend string, ice []ICEServer) (string, error) {
	l, err := ls.open(friend, ice)
	if err != nil {
		return "", err
	}
	// служебный канал: без него в предложении нет SCTP, каналы «bt» не откроются
	if _, err = l.pc.CreateDataChannel("ctl", nil); err == nil {
		var o webrtc.SessionDescription
		if o, err = l.pc.CreateOffer(nil); err == nil {
			var sdp string
			if sdp, err = gather(l.pc, o); err == nil {
				return sdp, nil
			}
		}
	}
	ls.close(friend)
	return "", err
}

func (ls *links) acceptOffer(friend, sdp string, ice []ICEServer) (string, error) {
	l, err := ls.open(friend, ice)
	if err != nil {
		return "", err
	}
	if err = l.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err == nil {
		var a webrtc.SessionDescription
		if a, err = l.pc.CreateAnswer(nil); err == nil {
			var out string
			if out, err = gather(l.pc, a); err == nil {
				return out, nil
			}
		}
	}
	ls.close(friend)
	return "", err
}

func (ls *links) answer(friend, sdp string) error {
	ls.mu.Lock()
	l := ls.m[friend]
	ls.mu.Unlock()
	if l == nil {
		return errors.New("предложение уже закрыто")
	}
	return l.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp})
}

func (ls *links) close(friend string) {
	ls.mu.Lock()
	l := ls.m[friend]
	delete(ls.m, friend)
	ls.mu.Unlock()
	if l != nil {
		_ = l.pc.Close()
	}
}

func (ls *links) state(friend string) string {
	ls.mu.Lock()
	l := ls.m[friend]
	ls.mu.Unlock()
	if l == nil {
		return "none"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

func (ls *links) stop() {
	ls.mu.Lock()
	ids := make([]string, 0, len(ls.m))
	for id := range ls.m {
		ids = append(ids, id)
	}
	ls.mu.Unlock()
	for _, id := range ids {
		ls.close(id)
	}
	ls.once.Do(func() { close(ls.done) })
}

// ---------- для движка: Dialer и Listener ----------

func (ls *links) DialerNetwork() string { return "zeno" }

// Dial открывает новый канал «bt» в соединении с другом.
func (ls *links) Dial(ctx context.Context, addr string) (net.Conn, error) {
	friend, ok := strings.CutPrefix(addr, addrPrefix)
	if !ok {
		return nil, errors.New("не адрес Zeno")
	}
	ls.mu.Lock()
	l := ls.m[friend]
	ls.mu.Unlock()
	if l == nil || l.pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
		return nil, errors.New("нет соединения с другом")
	}
	dc, err := l.pc.CreateDataChannel("bt", nil)
	if err != nil {
		return nil, err
	}
	opened := make(chan net.Conn, 1)
	dc.OnOpen(func() {
		if raw, err := dc.DetachWithDeadline(); err == nil {
			opened <- newConn(raw, friend)
		}
	})
	select {
	case c := <-opened:
		return c, nil
	case <-ctx.Done():
		_ = dc.Close()
		return nil, ctx.Err()
	}
}

func (ls *links) Accept() (net.Conn, error) {
	select {
	case c := <-ls.accept:
		return c, nil
	case <-ls.done:
		return nil, net.ErrClosed
	}
}

func (ls *links) Addr() net.Addr { return zAddr("") }

// ---------- канал данных как net.Conn ----------

type conn struct {
	raw    datachannel.ReadWriteCloserDeadliner
	friend string
	buf    []byte // остаток прочитанного сообщения
	rbuf   []byte
	mu     sync.Mutex // запись кусками целиком
}

func newConn(raw datachannel.ReadWriteCloserDeadliner, friend string) *conn {
	return &conn{raw: raw, friend: friend, rbuf: make([]byte, 64<<10)}
}

// Read: сообщение канала читается целиком в свой буфер, отдается частями.
func (c *conn) Read(p []byte) (int, error) {
	if len(c.buf) == 0 {
		n, err := c.raw.Read(c.rbuf)
		if err != nil {
			return 0, err
		}
		c.buf = c.rbuf[:n]
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

func (c *conn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for len(p) > 0 {
		k := min(len(p), chunk)
		w, err := c.raw.Write(p[:k])
		n += w
		if err != nil {
			return n, err
		}
		p = p[k:]
	}
	return n, nil
}

func (c *conn) Close() error                       { return c.raw.Close() }
func (c *conn) LocalAddr() net.Addr                { return zAddr("") }
func (c *conn) RemoteAddr() net.Addr               { return zAddr(c.friend) }
func (c *conn) SetReadDeadline(t time.Time) error  { return c.raw.SetReadDeadline(t) }
func (c *conn) SetWriteDeadline(t time.Time) error { return c.raw.SetWriteDeadline(t) }
func (c *conn) SetDeadline(t time.Time) error {
	_ = c.raw.SetWriteDeadline(t)
	return c.raw.SetReadDeadline(t)
}
