package main

// Push-to-talk по всей системе: клавиша работает, даже когда окно Zeno не в
// фокусе (в игре). Опрос GetAsyncKeyState раз в 10 мс, только пока режим
// включен; нажатие и отпускание передаются в окно.

import (
	"strconv"
	"sync/atomic"
	"time"
)

var procGetAsyncKeyState = modUser32.NewProc("GetAsyncKeyState")

type PTT struct {
	app *App
	vk  atomic.Int32 // код клавиши Windows (VK_*), 0 - выключено
}

func newPTT(app *App) *PTT { return &PTT{app: app} }

func (p *PTT) Set(vk int) {
	if vk < 0 || vk > 0xFE {
		vk = 0
	}
	p.vk.Store(int32(vk))
}

func (p *PTT) run() {
	down := false
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-p.app.quitCh:
			return
		case <-tick.C:
		}
		vk := p.vk.Load()
		now := false
		if vk != 0 {
			r, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
			now = r&0x8000 != 0
		}
		if now != down {
			down = now
			p.app.window.Eval("window.zenoPTT && window.zenoPTT(" + strconv.FormatBool(now) + ")")
		}
	}
}
