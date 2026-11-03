package main

// Свое окно выбора трансляции (см. internal/screenpick): список окон и экранов
// с превью и проход родного диалога Chromium вместо пользователя.

import (
	"errors"
	"fmt"
	"sync"
	"time"

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
