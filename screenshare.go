package main

// Свое окно выбора трансляции (см. internal/screenpick): список окон и экранов
// с превью и проход родного диалога Chromium вместо пользователя.

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"zeno/internal/nativecap"
	"zeno/internal/screenpick"
)

type screenPicker struct {
	mu   sync.Mutex
	done chan struct{}
}

var theScreenPicker screenPicker

func init() {
	// шаги повторяются каждые 40 мс, пока диалога нет - пишем только смену шага
	var mu sync.Mutex
	last := ""
	screenpick.Debug = func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		if s := fmt.Sprintf(f, a...); s != last {
			last = s
			logf("Выбор экрана: %s", s)
		}
	}
	nativecap.Logf = logf
}

// ScreenSources - окна и экраны с превью для окна выбора.
func (a *App) ScreenSources() []screenpick.Source {
	return screenpick.List(true)
}

// ScreenPick готовит проход родного диалога: окно накрыто снимком, можно звать
// getDisplayMedia. Ответ страницы - ScreenPicked.
func (a *App) ScreenPick(id string) error {
	src, ok := screenpick.Find(id)
	if !ok {
		return errors.New("источник пропал, откройте список заново")
	}
	host := a.window.hwnd()
	if host == 0 {
		return errors.New("окно программы закрыто")
	}
	p := &theScreenPicker
	p.mu.Lock()
	if p.done != nil {
		close(p.done)
	}
	done := make(chan struct{})
	p.done = done
	p.mu.Unlock()

	ready := make(chan struct{})
	go func() {
		if err := screenpick.Pick(host, src, done, func() { close(ready) }); err != nil {
			logf("Выбор экрана %q: %v", src.Name, err)
		}
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
	}
	return nil
}

// ---------- свой захват (видеокарта) ----------

var (
	nativeMu sync.Mutex
	native   *nativecap.Sender
)

type iceServer struct {
	URLs       any    `json:"urls"`
	Username   string `json:"username"`
	Credential string `json:"credential"`
}

type iceEntry struct {
	URLs                 []string
	Username, Credential string
}

// parseICE - список STUN/TURN из страницы (urls - строка или массив).
func parseICE(v any) []iceEntry {
	var out []iceEntry
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var list []iceServer
	_ = json.Unmarshal(b, &list)
	for _, s := range list {
		var urls []string
		switch u := s.URLs.(type) {
		case string:
			urls = []string{u}
		case []any:
			for _, x := range u {
				if v, ok := x.(string); ok {
					urls = append(urls, v)
				}
			}
		}
		out = append(out, iceEntry{URLs: urls, Username: s.Username, Credential: s.Credential})
	}
	return out
}

// NativeStart запускает свой захват источника id и возвращает предложение SDP
// для браузера друга.
func (a *App) NativeStart(body M) (any, error) {
	src, ok := screenpick.Find(str(body, "id"))
	if !ok {
		return nil, errors.New("источник пропал, откройте список заново")
	}
	num := func(k string) int { v, _ := body[k].(float64); return int(v) }
	o := nativecap.Options{W: num("w"), H: num("h"), FPS: num("fps"), Kbps: num("kbps"), Cursor: true}
	hwnd, x, y := src.Target()
	if hwnd != 0 {
		o.Window = hwnd
	} else {
		o.Monitor = nativecap.MonitorAt(x, y)
	}
	var ice []nativecap.ICEServer
	for _, x := range parseICE(body["ice"]) {
		ice = append(ice, nativecap.ICEServer{URLs: x.URLs, Username: x.Username, Credential: x.Credential})
	}
	a.NativeStop()
	snd, sdp, err := nativecap.NewSender(o, ice)
	if err != nil {
		logf("Свой захват %q: %v", src.Name, err)
		return nil, err
	}
	st := snd.Stats()
	logf("Свой захват %q: %s, %dx%d, %d к/с, %d кбит/с", src.Name, st.Encoder, st.W, st.H, o.FPS, o.Kbps)
	nativeMu.Lock()
	native = snd
	nativeMu.Unlock()
	return M{"sdp": sdp}, nil
}

func nativeSender() *nativecap.Sender {
	nativeMu.Lock()
	defer nativeMu.Unlock()
	return native
}

func (a *App) NativeAnswer(sdp string) error {
	s := nativeSender()
	if s == nil {
		return errors.New("свой захват не запущен")
	}
	return s.SetAnswer(sdp)
}

// NativeQuality - смена качества во время своего захвата: размер, частота, битрейт.
func (a *App) NativeQuality(w, h, fps, kbps int) {
	if s := nativeSender(); s != nil {
		s.SetQuality(w, h, fps, kbps)
	}
}

func (a *App) NativeStats() any {
	s := nativeSender()
	if s == nil {
		return M{"active": false}
	}
	st := s.Stats()
	ended := false
	select {
	case <-s.Done():
		ended = true
	default:
	}
	reason, gone := s.EndReason()
	return M{"active": true, "ended": ended, "reason": reason, "gone": gone, "stats": st}
}

func (a *App) NativeStop() {
	nativeMu.Lock()
	s := native
	native = nil
	nativeMu.Unlock()
	if s != nil {
		s.Stop()
	}
}

// ScreenPicked: getDisplayMedia в странице завершился - снимок можно убирать.
func (a *App) ScreenPicked() {
	p := &theScreenPicker
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done != nil {
		close(p.done)
		p.done = nil
	}
}
