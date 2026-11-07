package nativecap

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Options - что и как захватывать.
type Options struct {
	Window  uintptr // окно; 0 - монитор
	Monitor uintptr // HMONITOR, если не окно
	W, H    int     // наибольший размер картинки (пропорции источника сохраняются)
	FPS     int
	Kbps    int
	Cursor  bool
}

// Frame - сжатый кадр H.264 (Annex B).
type Frame struct {
	Data []byte
	Key  bool
	TS   time.Duration // время кадра от начала захвата
}

// Stats - счетчики за все время трансляции.
type Stats struct {
	Encoder          string
	W, H             int
	Captured, Frames int64 // кадров пришло с захвата / ушло из кодера
	Skipped          int64 // кадров пропущено, потому что кодер был занят
	Bytes            int64
	BaseW, BaseH     int     // размер картинки без снижения
	HDR              bool    // источник в HDR, переводим в обычный диапазон
	WhiteNit         float64 // белый SDR у отправителя
}

// Stream - идущий захват. Все методы можно звать из любой горутины.
type Stream struct {
	opts    Options
	out     func(Frame)
	stop    chan struct{}
	done    chan struct{}
	keyReq  atomic.Bool
	kbpsReq atomic.Int32
	sizeReq atomic.Uint64 // w<<32 | h
	presReq atomic.Pointer[preset]

	mu      sync.Mutex
	stats   Stats
	endErr  string // почему захват закончился сам
	srcGone bool   // закончился, потому что пропал источник (окно закрыли)
}

// EndReason - почему захват закончился сам ("" - остановили или еще идет) и
// пропал ли источник (тогда перезапускать нечего).
func (s *Stream) EndReason() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endErr, s.srcGone
}

func (s *Stream) fail(what string, err error) {
	msg := what
	if err != nil {
		msg += ": " + err.Error()
	}
	s.mu.Lock()
	s.endErr = msg
	st := s.stats
	s.mu.Unlock()
	Logf("Свой захват остановился: %s (%s %dx%d, кадров %d из %d)", msg, st.Encoder, st.W, st.H, st.Frames, st.Captured)
}

// forceHDR - для проверок: путь HDR даже на обычном мониторе.
var forceHDR bool

// Logf - журнал программы: почему захват остановился, сторож кодера, соединение.
var Logf = func(string, ...any) {}

// Сторож кодера: картинка стоит - кадр все равно повторяется раз в 0,5 с, так что
// 3 с без единого кадра из кодера значит, что он завис (не просит вход или
// отказывает). Кодер пересоздается; если это не помогает - захват заканчивается.
const (
	stallAfter  = 3 * time.Second
	stallMax    = 5 // пересозданий сторожем за stallWindow
	stallWindow = time.Minute
)

// MonitorAt - HMONITOR монитора, на котором лежит точка.
func MonitorAt(x, y int32) uintptr { return monitorAt(x, y) }

// Start запускает захват; кадры приходят в out из потока захвата.
func Start(o Options, out func(Frame)) (*Stream, error) {
	if o.FPS <= 0 {
		o.FPS = 60
	}
	if o.Kbps <= 0 {
		o.Kbps = 10000
	}
	s := &Stream{opts: o, out: out, stop: make(chan struct{}), done: make(chan struct{})}
	started := make(chan error, 1)
	go s.run(started)
	if err := <-started; err != nil {
		return nil, err
	}
	return s, nil
}

// Stop останавливает захват и ждет, пока все освободится.
func (s *Stream) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
}

// Done закрывается, когда захват закончился (в том числе сам: окно закрыли).
func (s *Stream) Done() <-chan struct{} { return s.done }

// KeyFrame просит ключевой кадр (друг потерял пакеты или только подключился).
func (s *Stream) KeyFrame() { s.keyReq.Store(true) }

// SetBitrate меняет битрейт на лету.
func (s *Stream) SetBitrate(kbps int) { s.kbpsReq.Store(int32(kbps)) }

type preset struct{ w, h, fps int }

// SetPreset - пользователь сменил качество во время трансляции: новая рамка
// размера и частота кадров. Кодер пересоздается, трансляция не прерывается.
func (s *Stream) SetPreset(maxW, maxH, fps int) { s.presReq.Store(&preset{maxW, maxH, fps}) }

// Resize меняет размер картинки: кодер пересоздается, первый кадр - ключевой.
func (s *Stream) Resize(w, h int) { s.sizeReq.Store(uint64(even(w))<<32 | uint64(even(h))) }

func (s *Stream) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// even: кодерам нужны четные размеры.
func even(v int) uint32 { return uint32(max(2, v&^1)) }

// fit вписывает источник w x h в рамку maxW x maxH с сохранением пропорций.
func fit(w, h, maxW, maxH int) (uint32, uint32) {
	if maxW <= 0 || maxH <= 0 || (w <= maxW && h <= maxH) {
		return even(w), even(h)
	}
	k := min(float64(maxW)/float64(w), float64(maxH)/float64(h))
	return even(int(float64(w)*k + 0.5)), even(int(float64(h)*k + 0.5))
}

func (s *Stream) run(started chan<- error) {
	defer close(s.done)
	// D3D11 и кодер живут в одном потоке ОС
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	d, err := newDevice()
	if err != nil {
		started <- err
		return
	}
	defer d.close()
	// HDR на мониторе источника - захват FP16 и перевод шейдером; шейдер не
	// собрался - захват как обычно (будет пересвет, но трансляция идет)
	mon := s.opts.Monitor
	if s.opts.Window != 0 {
		mon = monitorOfWindow(s.opts.Window)
	}
	var tm *toneMapper
	hi, _ := hdrInfo(mon)
	if hi.On || forceHDR {
		if tm, err = newToneMapper(d, hi.WhiteNit); err != nil {
			tm = nil
		}
	}
	if tm != nil {
		defer tm.close()
		s.mu.Lock()
		s.stats.HDR, s.stats.WhiteNit = true, tm.white
		s.mu.Unlock()
	}
	c, err := newCapture(d, s.opts.Window, s.opts.Monitor, s.opts.Cursor, tm != nil)
	if err != nil {
		started <- err
		return
	}
	defer c.close()
	ow, oh := fit(int(c.size.W), int(c.size.H), s.opts.W, s.opts.H)
	fps := uint32(s.opts.FPS)
	frameDur := time.Second / time.Duration(fps)
	kbps := uint32(s.opts.Kbps)
	var lastOut time.Time // последний кадр из кодера (для сторожа)
	onOut := func(data []byte, key bool, ts int64) {
		lastOut = time.Now()
		s.mu.Lock()
		s.stats.Frames++
		s.stats.Bytes += int64(len(data))
		s.mu.Unlock()
		s.out(Frame{Data: data, Key: key, TS: time.Duration(ts) * 100})
	}
	// build (пере)создает видеопроцессор и кодер под размер картинки: при узком
	// канале отправитель просит разрешение пониже, чтобы не было квадратов
	var conv *converter
	var enc *encoder
	build := func(w, h uint32) error {
		if enc != nil {
			enc.close()
			enc = nil
		}
		if conv != nil {
			conv.close()
			conv = nil
		}
		var err error
		if conv, err = newConverter(d, w, h, fps, tm); err != nil {
			return err
		}
		if enc, err = newEncoder(d, w, h, fps, kbps, onOut); err != nil {
			return err
		}
		lastOut = time.Now()
		s.mu.Lock()
		s.stats.Encoder, s.stats.W, s.stats.H = enc.Name, int(w), int(h)
		s.mu.Unlock()
		return nil
	}
	defer func() {
		if enc != nil {
			enc.close()
		}
		if conv != nil {
			conv.close()
		}
	}()
	if err := build(ow, oh); err != nil {
		started <- err
		return
	}
	s.mu.Lock()
	s.stats.BaseW, s.stats.BaseH = int(ow), int(oh)
	s.mu.Unlock()
	started <- nil

	// Кадр кодируем сразу, как он пришел с захвата (не ждем «такта» - иначе
	// часы игры и наши расходятся и кадры уходят то через 16, то через 33 мс),
	// но не чаще fps: при 144 Гц берем самый свежий. Время кадра - момент
	// захвата, чтобы друг показывал кадры с теми же промежутками, что в игре.
	// Картинка стоит - раз в полсекунды повторяем последний кадр.
	// Какой кадр брать, решает расписание по меткам захвата: у монитора 144-165 Гц
	// кадры идут сеткой по 6-7 мс, и простое «не чаще 16,7 мс» брало каждый
	// третий (55 к/с). По расписанию в среднем выходит ровно fps.
	fd := int64(frameDur / 100)
	sch := schedule{fd: fd}
	var fresh bool
	var freshTS, firstTS, lastTS int64 // время захвата, единицы 100 нс
	var lastEnc time.Time
	var stalls []time.Time
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		if !lastEnc.IsZero() && time.Since(lastOut) > stallAfter {
			now := time.Now()
			for len(stalls) > 0 && now.Sub(stalls[0]) > stallWindow {
				stalls = stalls[1:]
			}
			if len(stalls) >= stallMax {
				s.fail("кодер зависает снова и снова", nil)
				return
			}
			stalls = append(stalls, now)
			s.mu.Lock()
			w, h := uint32(s.stats.W), uint32(s.stats.H)
			s.mu.Unlock()
			Logf("Свой захват: кодер молчит %.0f с (просит вход: %v) - пересоздаю", now.Sub(lastOut).Seconds(), enc.ready())
			if err := build(w, h); err != nil {
				s.fail("кодер не пересоздался", err)
				return
			}
			s.keyReq.Store(true)
		}
		_, err := c.grab(func(tex com, w, h, tw, th uint32, t int64) {
			if conv.put(tex, w, h, tw, th) == nil {
				if fresh && !lastEnc.IsZero() && sch.due(freshTS) {
					s.mu.Lock()
					s.stats.Skipped++ // кадр был нужен по расписанию, но кодер не успел его взять
					s.mu.Unlock()
				}
				if firstTS == 0 {
					firstTS = t
				}
				fresh, freshTS = true, t-firstTS
				s.mu.Lock()
				s.stats.Captured++
				s.mu.Unlock()
			}
		})
		if err != nil {
			s.mu.Lock()
			s.srcGone = true
			s.mu.Unlock()
			s.fail("источник пропал или видеокарта сбросилась", err) // окно закрыто, устройство потеряно
			return
		}
		if err := enc.pump(); err != nil {
			s.fail("ошибка кодера", err)
			return
		}
		if p := s.presReq.Swap(nil); p != nil {
			if p.fps > 0 {
				fps = uint32(p.fps)
				frameDur = time.Second / time.Duration(fps)
				fd = int64(frameDur / 100)
				sch = schedule{fd: fd}
			}
			ow, oh = fit(int(c.size.W), int(c.size.H), p.w, p.h)
			s.sizeReq.Store(0) // ступень от прошлого качества больше не нужна
			if err := build(ow, oh); err != nil {
				s.fail("смена качества", err)
				return
			}
			s.mu.Lock()
			s.stats.BaseW, s.stats.BaseH = int(ow), int(oh)
			s.mu.Unlock()
		}
		if r := s.sizeReq.Swap(0); r != 0 {
			if err := build(uint32(r>>32), uint32(r)); err != nil {
				s.fail("смена разрешения", err)
				return
			}
		}
		if k := s.kbpsReq.Swap(0); k > 0 {
			kbps = uint32(k)
			enc.setBitrate(kbps)
		}
		now := time.Now()
		repeat := !fresh && !lastEnc.IsZero() && now.Sub(lastEnc) > 500*time.Millisecond
		onTime := fresh && (lastEnc.IsZero() || sch.due(freshTS))
		if (onTime || repeat) && enc.ready() {
			if s.keyReq.Swap(false) {
				enc.forceKeyFrame()
			}
			ts := freshTS
			if repeat {
				ts = lastTS + int64(now.Sub(lastEnc)/100)
			}
			if ts <= lastTS {
				ts = lastTS + 1
			}
			if tex, err := conv.convert(); err == nil && enc.input(tex, ts, fd) == nil {
				fresh, lastEnc, lastTS = false, now, ts
				sch.sent(ts)
			}
		}
		time.Sleep(time.Millisecond) // чаще не надо: кадры приходят раз в 6-7 мс даже на 165 Гц
	}
}

var ErrUnsupported = errors.New("свой захват недоступен на этом ПК")

// schedule выбирает кадры с захвата так, чтобы в среднем выходило ровно fps,
// даже когда кадры приходят сеткой монитора 144-165 Гц (по 6-7 мс). Время -
// метки захвата в единицах 100 нс.
type schedule struct {
	fd, next int64 // длительность кадра; когда нужен следующий
}

// due: кадр с меткой ts пора отправлять (допуск - четверть кадра раньше срока).
func (c *schedule) due(ts int64) bool { return ts >= c.next-c.fd/4 }

// sent: кадр ts отправлен. Следующий - через fd по расписанию, но не раньше
// чем через 3/4 fd от этого: после подвисания игры не «догоняем» пачкой кадров
// по 6 мс (было до 64 к/с и рывки), а начинаем отсчет заново.
func (c *schedule) sent(ts int64) {
	c.next += c.fd
	if c.next < ts+c.fd*3/4 {
		c.next = ts + c.fd*3/4
	}
}
