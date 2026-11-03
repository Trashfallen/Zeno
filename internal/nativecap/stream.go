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
}

// Stream - идущий захват. Все методы можно звать из любой горутины.
type Stream struct {
	opts    Options
	out     func(Frame)
	stop    chan struct{}
	done    chan struct{}
	keyReq  atomic.Bool
	kbpsReq atomic.Int32

	mu    sync.Mutex
	stats Stats
}

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
	c, err := newCapture(d, s.opts.Window, s.opts.Monitor, s.opts.Cursor)
	if err != nil {
		started <- err
		return
	}
	defer c.close()
	ow, oh := fit(int(c.size.W), int(c.size.H), s.opts.W, s.opts.H)
	fps := uint32(s.opts.FPS)
	conv, err := newConverter(d, ow, oh, fps)
	if err != nil {
		started <- err
		return
	}
	defer conv.close()
	frameDur := time.Second / time.Duration(fps)
	enc, err := newEncoder(d, ow, oh, fps, uint32(s.opts.Kbps), func(data []byte, key bool, ts int64) {
		s.mu.Lock()
		s.stats.Frames++
		s.stats.Bytes += int64(len(data))
		s.mu.Unlock()
		s.out(Frame{Data: data, Key: key, TS: time.Duration(ts) * 100})
	})
	if err != nil {
		started <- err
		return
	}
	defer enc.close()
	s.mu.Lock()
	s.stats.Encoder, s.stats.W, s.stats.H = enc.Name, int(ow), int(oh)
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
	var due int64
	var fresh bool
	var freshTS, firstTS, lastTS int64 // время захвата, единицы 100 нс
	var lastEnc time.Time
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		_, err := c.grab(func(tex com, w, h, tw, th uint32, t int64) {
			if conv.put(tex, w, h, tw, th) == nil {
				if fresh && !lastEnc.IsZero() && freshTS >= due-fd/4 {
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
			return // источник пропал (окно закрыто) или устройство потеряно
		}
		if err := enc.pump(); err != nil {
			return
		}
		if k := s.kbpsReq.Swap(0); k > 0 {
			enc.setBitrate(uint32(k))
		}
		now := time.Now()
		repeat := !fresh && !lastEnc.IsZero() && now.Sub(lastEnc) > 500*time.Millisecond
		onTime := fresh && (lastEnc.IsZero() || freshTS >= due-fd/4)
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
				due += fd
				if due <= ts { // картинка стояла или отстали - отсчет заново от этого кадра
					due = ts + fd
				}
			}
		}
		time.Sleep(time.Millisecond) // чаще не надо: кадры приходят раз в 6-7 мс даже на 165 Гц
	}
}

var ErrUnsupported = errors.New("свой захват недоступен на этом ПК")
