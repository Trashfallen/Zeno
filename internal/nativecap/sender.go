package nativecap

// Отправка своего захвата другу: отдельное соединение WebRTC (pion) прямо
// в браузер друга. Сигналы (offer/answer) страница передает по уже открытому
// каналу данных основного соединения. Звук трансляции идет как раньше -
// через основное соединение.

import (
	"errors"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// Sender - свой захват + соединение к другу.
type Sender struct {
	pc      *webrtc.PeerConnection
	track   *webrtc.TrackLocalStaticSample
	stream  *Stream
	maxKbps int

	mu      sync.Mutex
	kbps    int
	lastTS  time.Duration
	state   string
	cleanAt time.Time // когда потери последний раз были выше нормы
	closed  bool
}

// SenderStats - для статистики в окне трансляции.
type SenderStats struct {
	Stats
	Kbps  int    `json:"kbps"`  // текущий битрейт кодера
	State string `json:"state"` // состояние соединения
}

// ICEServer - STUN/TURN, как в настройках страницы.
type ICEServer struct {
	URLs                 []string
	Username, Credential string
}

// NewSender запускает захват и готовит предложение для браузера друга.
func NewSender(o Options, ice []ICEServer) (*Sender, string, error) {
	m := &webrtc.MediaEngine{}
	fb := []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "ccm", Parameter: "fir"}, {Type: "goog-remb"}}
	for i, fmtp := range []string{
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640c1f",
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	} {
		if err := m.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: fmtp, RTCPFeedback: fb},
			PayloadType:        webrtc.PayloadType(102 + 2*i),
		}, webrtc.RTPCodecTypeVideo); err != nil {
			return nil, "", err
		}
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		return nil, "", err
	}
	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir), webrtc.WithSettingEngine(se))
	var servers []webrtc.ICEServer
	for _, x := range ice {
		if len(x.URLs) > 0 {
			servers = append(servers, webrtc.ICEServer{URLs: x.URLs, Username: x.Username, Credential: x.Credential})
		}
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: servers})
	if err != nil {
		return nil, "", err
	}
	s := &Sender{pc: pc, maxKbps: o.Kbps, kbps: o.Kbps, state: "new"}
	fail := func(err error) (*Sender, string, error) {
		s.Stop()
		return nil, "", err
	}
	s.track, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "screen", "zeno-screen")
	if err != nil {
		return fail(err)
	}
	tr, err := pc.AddTransceiverFromTrack(s.track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	if err != nil {
		return fail(err)
	}
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		s.mu.Lock()
		s.state = st.String()
		str := s.stream
		s.mu.Unlock()
		if st == webrtc.PeerConnectionStateConnected && str != nil {
			str.KeyFrame()
		}
	})

	s.stream, err = Start(o, s.write)
	if err != nil {
		return fail(err)
	}
	go s.readRTCP(tr.Sender())

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return fail(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return fail(err)
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second): // отдаем, что успели собрать
	}
	return s, pc.LocalDescription().SDP, nil
}

func (s *Sender) write(f Frame) {
	s.mu.Lock()
	dur := f.TS - s.lastTS
	if s.lastTS == 0 || dur <= 0 || dur > time.Second {
		dur = time.Second / 60
	}
	s.lastTS = f.TS
	s.mu.Unlock()
	_ = s.track.WriteSample(media.Sample{Data: f.Data, Duration: dur})
}

// readRTCP: запросы ключевого кадра и отчеты о потерях от браузера друга.
func (s *Sender) readRTCP(rs *webrtc.RTPSender) {
	for {
		pkts, _, err := rs.ReadRTCP()
		if err != nil {
			return
		}
		for _, p := range pkts {
			switch r := p.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				s.stream.KeyFrame()
			case *rtcp.ReceiverReport:
				for _, rep := range r.Reports {
					s.adapt(float64(rep.FractionLost) / 256)
				}
			}
		}
	}
}

// adapt - простая подстройка битрейта по потерям: заметные потери - снижаем
// на 15%, чисто 5 секунд - прибавляем 8%, но не выше выбранного качества.
func (s *Sender) adapt(lost float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	k := s.kbps
	switch {
	case lost > 0.05:
		k = max(s.maxKbps/5, k*85/100)
		s.cleanAt = now
	case lost < 0.01 && now.Sub(s.cleanAt) > 5*time.Second:
		k = min(s.maxKbps, k*108/100+100)
		s.cleanAt = now
	}
	if k != s.kbps {
		s.kbps = k
		s.stream.SetBitrate(k)
	}
}

// SetAnswer принимает ответ браузера друга.
func (s *Sender) SetAnswer(sdp string) error {
	if s == nil || s.pc == nil {
		return errors.New("трансляция уже остановлена")
	}
	return s.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp})
}

// SetMaxBitrate - пользователь сменил качество.
func (s *Sender) SetMaxBitrate(kbps int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxKbps = kbps
	s.kbps = min(s.kbps, kbps)
	s.stream.SetBitrate(s.kbps)
}

func (s *Sender) Stats() SenderStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := SenderStats{Kbps: s.kbps, State: s.state}
	if s.stream != nil {
		out.Stats = s.stream.Stats()
	}
	return out
}

// Done закрывается, когда захват закончился (например, окно закрыли).
func (s *Sender) Done() <-chan struct{} { return s.stream.Done() }

func (s *Sender) Stop() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	str := s.stream
	s.mu.Unlock()
	if str != nil {
		str.Stop()
	}
	if s.pc != nil {
		_ = s.pc.Close()
	}
}
