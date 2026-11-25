package nativecap

// Отправка своего захвата другу: отдельное соединение WebRTC (pion) прямо
// в браузер друга. Сигналы (offer/answer) страница передает по уже открытому
// каналу данных основного соединения. Звук трансляции идет как раньше -
// через основное соединение.

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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

	ladderReset atomic.Bool
	est         cc.BandwidthEstimator

	mu      sync.Mutex
	fps     int
	detail  bool   // «Четкость»: ступени сначала по частоте
	preset  [3]int // w, h, fps выбранного качества
	kbps    int    // битрейт кодера сейчас
	target  int    // оценка канала (GCC), кбит/с
	rtpBase uint32
	state   string
	closed  bool

	// для журнала: заметные падения и восстановления оценки канала
	peak, peakLogged int
	peakAt, dropLog  time.Time
	dropsQuiet       int
	recovLogged      int
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
	// связь пропала: «failed» через 10 с, а не через 25 по умолчанию - страница
	// раньше переподключит трансляцию, картинка у друга стоит меньше
	se.SetICETimeouts(5*time.Second, 10*time.Second, 2*time.Second)
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
	s := &Sender{pc: pc, maxKbps: o.Kbps, kbps: startKbps, target: startKbps, fps: o.FPS, detail: o.Detail, state: "new"}
	if s.fps <= 0 {
		s.fps = 60
	}
	s.preset = [3]int{o.W, o.H, s.fps}
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
		pair := ""
		if st == webrtc.PeerConnectionStateConnected {
			if t := tr.Sender().Transport(); t != nil {
				if p, err := t.ICETransport().GetSelectedCandidatePair(); err == nil && p != nil {
					pair = fmt.Sprintf(" (%s:%d -> %s:%d)", p.Local.Typ, p.Local.Port, p.Remote.Typ, p.Remote.Port)
				}
			}
		}
		Logf("Свой захват: соединение %s%s", st, pair)
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
		s.est = est
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
	s.logTarget(time.Now())
}

// logTarget пишет в журнал заметные падения оценки канала (с причиной) и
// быстрые восстановления - по журналу видно, почему падало качество.
// Падения - не чаще раза в 10 с, остальные считаются и попадают в следующую строку.
func (s *Sender) logTarget(now time.Time) {
	t := min(s.target, maxKbps)
	if t >= s.peak || now.Sub(s.peakAt) > 5*time.Second {
		s.peak, s.peakAt = t, now
	}
	if s.est == nil || s.peak == 0 {
		return
	}
	st := s.est.GetStats()
	num := func(k string) float64 {
		switch v := st[k].(type) {
		case int:
			return float64(v)
		case int64:
			return float64(v)
		case float64:
			return v
		}
		return 0
	}
	if t < s.peak*4/5 && s.peak >= 2*minKbps {
		if now.Sub(s.dropLog) < 10*time.Second {
			s.dropsQuiet++
			return
		}
		why := map[string]string{"delay": "скачок задержки", "delay+loss": "задержка и потери", "delay+rtt": "растет пинг"}[fmt.Sprint(st["lastCut"])]
		if lt, dt := num("lossTargetBitrate"), num("delayTargetBitrate"); lt > 0 && lt < dt {
			why = "потери"
		}
		if why == "" {
			why = "?"
		}
		more := ""
		if s.dropsQuiet > 0 {
			more = fmt.Sprintf(", еще снижений до этого: %d", s.dropsQuiet)
		}
		Logf("Свой захват: канал до друга %.1f -> %.1f Мбит/с (%s, потери %.1f%%, пинг %.0f мс%s)",
			float64(s.peak)/1000, float64(t)/1000, why, num("averageLoss")*100, num("rtt"), more)
		s.dropLog, s.dropsQuiet, s.peakLogged = now, 0, s.peak
		s.peak, s.peakAt = t, now
		return
	}
	if r := int(num("recoveries")); r > s.recovLogged {
		s.recovLogged = r
		Logf("Свой захват: канал восстановлен до %.1f Мбит/с (до падения было %.1f)", float64(t)/1000, float64(s.peakLogged)/1000)
	}
}

// Ступени при узком канале: при нехватке бит лучше меньше пикселей или
// кадров, чем квадраты. Мера - бит на пиксель кадра. Вниз - только по
// средней оценке за ladderAvg секунд и только если бит мало downSecs
// подряд (короткий провал канала качество не трогает). Вверх - по текущей
// оценке, быстро. Ниже 720p (по рамке 1280x720) не опускаемся.
// «Плавность» держит частоту и снижает размер, «Четкость» держит размер
// и сначала снижает частоту (60 -> 48 -> 40), потом размер.
type rung struct{ k, f float64 } // доля размера, доля частоты

var (
	motionRungs = []rung{{1, 1}, {0.9, 1}, {0.8, 1}, {0.7, 1}, {0.6, 1}, {0.5, 1}}
	detailRungs = []rung{{1, 1}, {1, 0.8}, {1, 0.67}, {0.9, 0.67}, {0.8, 0.67}, {0.7, 0.67}, {0.6, 0.67}, {0.5, 0.67}}
)

const (
	bppLow    = 0.02  // ниже - ступень вниз (по средней за ladderAvg, downSecs подряд)
	bppHigh   = 0.035 // на ступень выше хватает - ступень вверх (upSecs подряд)
	ladderAvg = 8
	downSecs  = 4
	upSecs    = 4
)

func (r rung) fps(base int) int { return max(1, int(float64(base)*r.f+0.5)) }

// ladderStep решает, куда сдвинуться со ступени step. avg - средний битрейт
// кодера за ladderAvg с, now - текущий, low/high - сколько секунд подряд бит
// мало для этой ступени / хватает на ступень выше (обновляются).
func ladderStep(rungs []rung, step int, baseW, baseH, fps int, avg, now float64, low, high *int) int {
	bpp := func(r rung, kbps float64) float64 {
		w, h := float64(baseW)*r.k, float64(baseH)*r.k
		return kbps * 1000 / (w * h * float64(r.fps(fps)))
	}
	if bpp(rungs[step], avg) < bppLow && step+1 < len(rungs) && !below720(baseW, baseH, rungs[step+1].k) {
		*low++
	} else {
		*low = 0
	}
	if step > 0 && bpp(rungs[step-1], now) > bppHigh {
		*high++
	} else {
		*high = 0
	}
	switch {
	case *low >= downSecs:
		*low, *high = 0, 0
		return step + 1
	case *high >= upSecs:
		*low, *high = 0, 0
		return step - 1
	}
	return step
}

func (s *Sender) ladder() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	step, low, high := 0, 0, 0
	var hist []float64 // битрейт кодера по секундам, для средней
	var detail bool
	for {
		select {
		case <-s.stream.Done():
			return
		case <-t.C:
		}
		st := s.stream.Stats()
		s.mu.Lock()
		kbps, fps, det, target := s.kbps, s.fps, s.detail, s.target
		s.mu.Unlock()
		if reset := s.ladderReset.Swap(false); reset || det != detail {
			// новое качество кодер и так берет целиком; при смене только режима
			// возвращаем полную ступень сами
			if !reset && step > 0 && st.BaseW > 0 {
				s.stream.Step(st.BaseW, st.BaseH, fps) // другой режим - другие ступени, начинаем с полной
			}
			step, low, high, detail = 0, 0, 0, det
			hist = hist[:0]
		}
		if st.BaseW == 0 {
			continue
		}
		rungs := motionRungs
		if detail {
			rungs = detailRungs
		}
		hist = append(hist, float64(kbps))
		if len(hist) > ladderAvg {
			hist = hist[1:]
		}
		avg := 0.0
		for _, v := range hist {
			avg += v
		}
		avg /= float64(len(hist))
		next := ladderStep(rungs, step, st.BaseW, st.BaseH, fps, avg, float64(kbps), &low, &high)
		if next == step {
			continue
		}
		step = next
		r := rungs[step]
		w, h, f := int(float64(st.BaseW)*r.k), int(float64(st.BaseH)*r.k), r.fps(fps)
		Logf("Свой захват: ступень %d: %dx%d %d к/с (кодер %.1f Мбит/с, в среднем %.1f, оценка канала %.1f)",
			step, w&^1, h&^1, f, float64(kbps)/1000, avg/1000, float64(target)/1000)
		s.stream.Step(w, h, f)
	}
}

// SetAnswer принимает ответ браузера друга.
func (s *Sender) SetAnswer(sdp string) error {
	if s == nil || s.pc == nil {
		return errors.New("трансляция уже остановлена")
	}
	return s.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp})
}

// SetQuality - пользователь сменил качество во время трансляции: размер и
// частота применяются сразу (кодер пересоздается), битрейт - потолок.
// detail - режим «Четкость» (порядок ступеней при узком канале).
func (s *Sender) SetQuality(w, h, fps, kbps int, detail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detail = detail
	if p := [3]int{w, h, fps}; w > 0 && h > 0 && fps > 0 && p != s.preset {
		s.preset, s.fps = p, fps
		s.stream.SetPreset(w, h, fps)
		s.ladderReset.Store(true)
	}
	if kbps <= 0 {
		return
	}
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

// EndReason - почему захват закончился сам и пропал ли источник.
func (s *Sender) EndReason() (string, bool) { return s.stream.EndReason() }

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

// below720: ступень k даст картинку меньше 720p (рамка 1280x720 с теми же пропорциями).
func below720(baseW, baseH int, k float64) bool {
	minW, _ := fit(baseW, baseH, 1280, 720)
	return float64(baseW)*k < float64(minW)-2
}
