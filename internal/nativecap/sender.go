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
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
)

// Sender - свой захват + соединение к другу.
type Sender struct {
	pc      *webrtc.PeerConnection
	track   *webrtc.TrackLocalStaticRTP
	pkt     rtp.Packetizer
	stream  *Stream
	maxKbps int

	fps int

	mu      sync.Mutex
	kbps    int // битрейт кодера сейчас
	target  int // оценка канала (GCC), кбит/с
	rtpBase uint32
	state   string
	closed  bool
}

// SenderStats - для статистики в окне трансляции.
type SenderStats struct {
	Stats
	Kbps   int    `json:"kbps"`   // текущий битрейт кодера
	Target int    `json:"target"` // оценка канала до друга
	State  string `json:"state"`  // состояние соединения
}

// ICEServer - STUN/TURN, как в настройках страницы.
type ICEServer struct {
	URLs                 []string
	Username, Credential string
}

// NewSender запускает захват и готовит предложение для браузера друга.
func NewSender(o Options, ice []ICEServer) (*Sender, string, error) {
	m := &webrtc.MediaEngine{}
	fb := []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "ccm", Parameter: "fir"}, {Type: "transport-cc"}}
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
	// Оценка канала как в браузере (GCC по задержкам и потерям) и пейсер:
	// пакеты кадра уходят равномерно каждые 5 мс, а не пачкой. Без этого
	// кадр 1440p пачкой в сотню пакетов переполнял очередь у друга - потери 10-15%.
	ir := &interceptor.Registry{}
	o.Kbps = max(o.Kbps, 1000)
	startKbps := min(o.Kbps, 8000)
	estCh := make(chan cc.BandwidthEstimator, 1)
	ccf, err := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) {
		return gcc.NewSendSideBWE(gcc.SendSideBWEInitialBitrate(startKbps*1000),
			gcc.SendSideBWEMinBitrate(minKbps*1000), gcc.SendSideBWEMaxBitrate(maxKbps*1000))
	})
	if err != nil {
		return nil, "", err
	}
	ccf.OnNewPeerConnection(func(_ string, e cc.BandwidthEstimator) {
		select {
		case estCh <- e:
		default:
		}
	})
	ir.Add(ccf)
	if err := webrtc.ConfigureTWCCHeaderExtensionSender(m, ir); err != nil {
		return nil, "", err
	}
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
	s := &Sender{pc: pc, maxKbps: o.Kbps, kbps: startKbps, target: startKbps, fps: o.FPS, state: "new"}
	if s.fps <= 0 {
		s.fps = 60
	}
	s.pkt = rtp.NewPacketizer(1200, 0, 0, &codecs.H264Payloader{}, rtp.NewRandomSequencer(), 90000)
	s.rtpBase = uint32(time.Now().UnixNano())
	fail := func(err error) (*Sender, string, error) {
		s.Stop()
		return nil, "", err
	}
	s.track, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "screen", "zeno-screen")
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

	so := o
	so.Kbps = startKbps
	s.stream, err = Start(so, s.write)
	if err != nil {
		return fail(err)
	}
	go s.readRTCP(tr.Sender())
	select {
	case est := <-estCh:
		est.OnTargetBitrateChange(s.onTarget)
	default:
	}
	go s.ladder()

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

// write режет кадр на RTP-пакеты сам: метка времени каждого кадра - ровно
// момент захвата (90 кГц). WriteSample в pion ставит метку по сумме
// длительностей прошлых кадров, и неровности сдвигались на кадр - друг видел
// подергивания.
func (s *Sender) write(f Frame) {
	ts := s.rtpBase + uint32(f.TS*90000/time.Second)
	for _, p := range s.pkt.Packetize(f.Data, 0) {
		p.Timestamp = ts
		_ = s.track.WriteRTP(p)
	}
}

// readRTCP: запросы ключевого кадра от браузера друга (потери, подключение).
// Отчеты о потерях и задержках разбирает GCC.
func (s *Sender) readRTCP(rs *webrtc.RTPSender) {
	for {
		pkts, _, err := rs.ReadRTCP()
		if err != nil {
			return
		}
		for _, p := range pkts {
			switch p.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				s.stream.KeyFrame()
			}
		}
	}
}

const (
	minKbps = 800
	maxKbps = 25000
)

// onTarget - новая оценка канала: битрейт кодера = оценка, но не выше выбранного качества.
func (s *Sender) onTarget(bps int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.target = bps / 1000
	k := max(minKbps, min(s.target, s.maxKbps))
	// мелкие колебания оценки кодеру не передаем, но до потолка качества доходим
	if d := k - s.kbps; d > s.kbps/20 || -d > s.kbps/20 || (k == s.maxKbps && d != 0) {
		s.kbps = k
		s.stream.SetBitrate(k)
	}
}

// Ступени разрешения от исходного: при узком канале лучше меньше пикселей,
// чем квадраты. Мера - бит на пиксель кадра: ниже 0,04 картинка сыплется.
var ladderSteps = []float64{1, 0.75, 0.6, 0.5}

const (
	bppLow    = 0.04 // ниже - снижаем разрешение (3 с подряд)
	bppHigh   = 0.07 // на ступень выше хватает - возвращаем (10 с подряд)
	ladderMin = 480  // ниже этой высоты не опускаемся
)

func (s *Sender) ladder() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	step, low, high := 0, 0, 0
	for {
		select {
		case <-s.stream.Done():
			return
		case <-t.C:
		}
		st := s.stream.Stats()
		s.mu.Lock()
		kbps := s.kbps
		s.mu.Unlock()
		if st.BaseW == 0 {
			continue
		}
		bpp := func(k float64) float64 {
			w, h := float64(st.BaseW)*k, float64(st.BaseH)*k
			return float64(kbps) * 1000 / (w * h * float64(s.fps))
		}
		if bpp(ladderSteps[step]) < bppLow {
			low++
		} else {
			low = 0
		}
		if step > 0 && bpp(ladderSteps[step-1]) > bppHigh {
			high++
		} else {
			high = 0
		}
		next := step
		switch {
		case low >= 3 && step+1 < len(ladderSteps) && float64(st.BaseH)*ladderSteps[step+1] >= ladderMin:
			next = step + 1
		case high >= 10:
			next = step - 1
		}
		if next != step {
			step, low, high = next, 0, 0
			k := ladderSteps[step]
			s.stream.Resize(int(float64(st.BaseW)*k), int(float64(st.BaseH)*k))
		}
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
	s.kbps = max(minKbps, min(s.target, kbps))
	s.stream.SetBitrate(s.kbps)
}

func (s *Sender) Stats() SenderStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := SenderStats{Kbps: s.kbps, Target: s.target, State: s.state}
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
