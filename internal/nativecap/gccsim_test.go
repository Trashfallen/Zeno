package nativecap

// Стенд оценки канала: pion-отправитель с нашим GCC и пейсером (как в
// NewSender) и pion-получатель с TWCC (как браузер друга) в виртуальной сети
// vnet с узким местом, задержкой, потерями и «застреваниями» как у Wi-Fi.
// Вместо кодера - заглушка, которая шлет столько, сколько разрешает оценка.
// Сравнивает GCC как в pion и с правками Zeno. Идет в реальном времени
// (~3 мин), поэтому только по ZENO_SIM_TEST=1:
//
//	ZENO_SIM_TEST=1 go test ./internal/nativecap -run GCCSim -v -timeout 10m

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/logging"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/transport/v5/vnet"
	"github.com/pion/webrtc/v4"
)

type simLink struct {
	capKbps   int           // узкое место от отправителя к получателю
	delay     time.Duration // задержка в одну сторону
	lossPct   int           // случайные потери, %
	stallEach time.Duration // раз в столько задержка подскакивает (0 - никогда)
	stallFor  time.Duration // на столько
	stallAdd  time.Duration // на сколько
	// смена ширины по ходу: в секунду at ширина становится kbps
	changes []struct {
		at   time.Duration
		kbps int
	}
}

type simResult struct {
	avgKbps   float64 // средний битрейт кодера после разгона
	lowShare  float64 // доля времени ниже 70% потолка (или ширины канала)
	lossPct   float64 // потери у получателя
	recoverAt float64 // сек от возврата канала до 80% потолка (-1 - не было)
	series    []int   // кодер по секундам
}

const simQuality = 16000 // кбит/с, потолок качества 1440p60

func runSim(t *testing.T, link simLink, dur time.Duration, tuned bool) simResult {
	gcc.SetZenoTuned(tuned)
	defer gcc.SetZenoTuned(true)
	lf := logging.NewDefaultLoggerFactory()
	lf.DefaultLogLevel = logging.LogLevelError

	wan, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "10.7.0.0/24", LoggerFactory: lf})
	must(t, err)
	sendNet, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"10.7.0.1"}})
	must(t, err)
	recvNet, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"10.7.0.2"}})
	must(t, err)
	// к получателю: узкое место с очередью -> задержка -> потери
	loss, err := vnet.NewLossFilter(recvNet, link.lossPct, vnet.WithLossSeed(7))
	must(t, err)
	fwdDelay, err := vnet.NewDelayFilter(loss, vnet.WithDelay(link.delay))
	must(t, err)
	tbf := vnet.NewTBFQueue(link.capKbps*1000, 15000, int64(link.capKbps*1000/8/5)) // очередь на 200 мс
	bottleneck, err := vnet.NewQueue(fwdDelay, tbf)
	must(t, err)
	// обратно (отчеты TWCC): только задержка
	backDelay, err := vnet.NewDelayFilter(sendNet, vnet.WithDelay(link.delay))
	must(t, err)
	must(t, wan.AddNet(backDelay))
	must(t, wan.AddNet(bottleneck))
	must(t, wan.Start())
	defer func() {
		_ = wan.Stop()
		_ = bottleneck.Close()
		_ = fwdDelay.Close()
		_ = backDelay.Close()
	}()

	// отправитель - как NewSender
	m := &webrtc.MediaEngine{}
	fb := []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "transport-cc"}}
	must(t, m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", RTCPFeedback: fb},
		PayloadType: 102,
	}, webrtc.RTPCodecTypeVideo))
	ir := &interceptor.Registry{}
	estCh := make(chan cc.BandwidthEstimator, 1)
	ccf, err := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) {
		return gcc.NewSendSideBWE(gcc.SendSideBWEInitialBitrate(8000*1000),
			gcc.SendSideBWEMinBitrate(minKbps*1000), gcc.SendSideBWEMaxBitrate(maxKbps*1000))
	})
	must(t, err)
	ccf.OnNewPeerConnection(func(_ string, e cc.BandwidthEstimator) { estCh <- e })
	ir.Add(ccf)
	must(t, webrtc.ConfigureTWCCHeaderExtensionSender(m, ir))
	must(t, webrtc.RegisterDefaultInterceptors(m, ir))
	se := webrtc.SettingEngine{}
	se.SetNet(sendNet)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.LoggerFactory = lf
	snd, err := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir), webrtc.WithSettingEngine(se)).
		NewPeerConnection(webrtc.Configuration{})
	must(t, err)
	defer snd.Close()
	est := <-estCh
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "screen", "sim")
	must(t, err)
	tr, err := snd.AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	must(t, err)
	go func() { // RTCP надо читать, иначе интерцепторы не получат отчеты
		for {
			if _, _, err := tr.Sender().ReadRTCP(); err != nil {
				return
			}
		}
	}()

	// получатель - как браузер: NACK, отчеты, TWCC
	m2 := &webrtc.MediaEngine{}
	must(t, m2.RegisterDefaultCodecs())
	ir2 := &interceptor.Registry{}
	must(t, webrtc.RegisterDefaultInterceptors(m2, ir2))
	se2 := webrtc.SettingEngine{}
	se2.SetNet(recvNet)
	se2.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se2.LoggerFactory = lf
	rcv, err := webrtc.NewAPI(webrtc.WithMediaEngine(m2), webrtc.WithInterceptorRegistry(ir2), webrtc.WithSettingEngine(se2)).
		NewPeerConnection(webrtc.Configuration{})
	must(t, err)
	defer rcv.Close()
	var got, expected atomic.Int64
	rcv.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		var first, last uint16
		var cycles int64
		started := false
		for {
			p, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			got.Add(1)
			s := p.SequenceNumber
			if !started {
				first, last, started = s, s, true
			} else if d := int16(s - last); d > 0 {
				if s < last {
					cycles++
				}
				last = s
			}
			expected.Store(cycles*65536 + int64(last) - int64(first) + 1)
		}
	})

	offer, err := snd.CreateOffer(nil)
	must(t, err)
	g1 := webrtc.GatheringCompletePromise(snd)
	must(t, snd.SetLocalDescription(offer))
	<-g1
	must(t, rcv.SetRemoteDescription(*snd.LocalDescription()))
	answer, err := rcv.CreateAnswer(nil)
	must(t, err)
	g2 := webrtc.GatheringCompletePromise(rcv)
	must(t, rcv.SetLocalDescription(answer))
	<-g2
	must(t, snd.SetRemoteDescription(*rcv.LocalDescription()))

	// кодер-заглушка: 60 к/с, кадр = битрейт / 60 (+-20%), ключевой раз в 4 с x3
	var kbps atomic.Int64
	kbps.Store(8000)
	est.OnTargetBitrateChange(func(bps int) {
		kbps.Store(int64(max(minKbps, min(bps/1000, simQuality))))
	})
	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		pk := rtp.NewPacketizer(1200, 102, 1, &codecs.H264Payloader{}, rtp.NewRandomSequencer(), 90000)
		rnd := rand.New(rand.NewSource(1))
		tick := time.NewTicker(time.Second / 60)
		defer tick.Stop()
		for n := 0; ; n++ {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			size := int(kbps.Load()) * 1000 / 8 / 60
			size = size * (80 + rnd.Intn(41)) / 100
			if n%240 == 0 {
				size *= 3
			}
			frame := make([]byte, max(size, 100))
			frame[0] = 0x41
			for _, p := range pk.Packetize(frame, 1500) {
				_ = track.WriteRTP(p)
			}
		}
	}()
	// «застревания»: задержка к получателю подскакивает и возвращается,
	// пакеты за это время приходят пачкой - как Wi-Fi
	go func() {
		defer wg.Done()
		start := time.Now()
		ci := 0
		next := start.Add(link.stallEach)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
			if ci < len(link.changes) && time.Since(start) >= link.changes[ci].at {
				tbf.SetRate(link.changes[ci].kbps * 1000)
				ci++
			}
			if link.stallEach > 0 && time.Now().After(next) {
				fwdDelay.SetDelay(link.delay + link.stallAdd)
				time.Sleep(link.stallFor)
				fwdDelay.SetDelay(link.delay)
				next = time.Now().Add(link.stallEach)
			}
		}
	}()

	var res simResult
	start := time.Now()
	sec := time.NewTicker(time.Second)
	defer sec.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			wg.Wait()
			if e := expected.Load(); e > 0 {
				res.lossPct = 100 * float64(e-got.Load()) / float64(e)
			}
			sum, low, n := 0.0, 0, 0
			for j, v := range res.series {
				if j < 8 { // разгон
					continue
				}
				sum += float64(v)
				n++
				ceil := simQuality
				for _, c := range link.changes {
					if time.Duration(j)*time.Second >= c.at {
						ceil = min(simQuality, c.kbps)
					}
				}
				if link.changes == nil {
					ceil = min(simQuality, link.capKbps)
				}
				if float64(v) < 0.7*float64(ceil) {
					low++
				}
			}
			if n > 0 {
				res.avgKbps, res.lowShare = sum/float64(n), float64(low)/float64(n)
			}
			res.recoverAt = -1
			if len(link.changes) > 1 {
				back := int(link.changes[len(link.changes)-1].at / time.Second)
				for j := back; j < len(res.series); j++ {
					if float64(res.series[j]) >= 0.8*simQuality {
						res.recoverAt = float64(j - back)
						break
					}
				}
			}
			return res
		case <-sec.C:
			res.series = append(res.series, int(kbps.Load()))
			if os.Getenv("ZENO_SIM_DEBUG") != "" {
				st := est.GetStats()
				t.Logf("%2.0f с: кодер %5d, delay %v loss %v avgLoss %.3f rtt %v %v/%v, получено пакетов %d",
					time.Since(start).Seconds(), kbps.Load(), st["delayTargetBitrate"], st["lossTargetBitrate"],
					st["averageLoss"], st["rtt"], st["usage"], st["state"], got.Load())
			}
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestGCCSim(t *testing.T) {
	if os.Getenv("ZENO_SIM_TEST") == "" {
		t.Skip("стенд в реальном времени: ZENO_SIM_TEST=1")
	}
	type change = struct {
		at   time.Duration
		kbps int
	}
	cases := []struct {
		name string
		link simLink
		dur  time.Duration
	}{
		// канал с запасом, но Wi-Fi: раз в 2.5 с задержка +60 мс на 150 мс, 1% потерь
		{"wifi", simLink{capKbps: 40000, delay: 15 * time.Millisecond, lossPct: 1,
			stallEach: 2500 * time.Millisecond, stallFor: 150 * time.Millisecond, stallAdd: 60 * time.Millisecond}, 40 * time.Second},
		// честно узкий канал: 6 Мбит/с - не перегружать
		{"narrow", simLink{capKbps: 6000, delay: 15 * time.Millisecond}, 35 * time.Second},
		// канал просел до 5 Мбит/с и вернулся: как быстро обратно к потолку
		{"dip", simLink{capKbps: 40000, delay: 15 * time.Millisecond,
			changes: []change{{0, 40000}, {15 * time.Second, 5000}, {27 * time.Second, 40000}}}, 50 * time.Second},
	}
	var report []string
	for _, c := range cases {
		if f := os.Getenv("ZENO_SIM_CASE"); f != "" && f != c.name {
			continue
		}
		old := runSim(t, c.link, c.dur, false)
		neu := runSim(t, c.link, c.dur, true)
		line := fmt.Sprintf("%-6s pion: в среднем %5.1f Мбит/с, ниже 70%%: %3.0f%%, потери %4.1f%%, обратно за %3.0f с | Zeno: %5.1f Мбит/с, %3.0f%%, %4.1f%%, %3.0f с",
			c.name, old.avgKbps/1000, old.lowShare*100, old.lossPct, old.recoverAt,
			neu.avgKbps/1000, neu.lowShare*100, neu.lossPct, neu.recoverAt)
		report = append(report, line)
		t.Log(line)
		t.Logf("  pion: %v", old.series)
		t.Logf("  Zeno: %v", neu.series)
		switch c.name {
		case "wifi":
			if neu.avgKbps < old.avgKbps {
				t.Errorf("wifi: Zeno держит меньше pion")
			}
		case "narrow":
			if neu.lossPct > 5 || neu.avgKbps > 6000 {
				t.Errorf("narrow: Zeno перегружает узкий канал: %.1f Мбит/с, потери %.1f%%", neu.avgKbps/1000, neu.lossPct)
			}
		case "dip":
			if neu.recoverAt < 0 || (old.recoverAt >= 0 && neu.recoverAt > old.recoverAt) {
				t.Errorf("dip: Zeno возвращается не быстрее pion")
			}
		}
	}
	sort.Strings(report)
	for _, l := range report {
		fmt.Println(l)
	}
}
