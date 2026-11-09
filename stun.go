package main

// Выбор STUN-серверов. Через STUN программа узнает свой внешний адрес и
// передает его другу. Если на ПК или роутере стоит VPN «российское напрямую,
// остальное через VPN» (так устроено большинство VPN в РФ), зарубежный STUN
// видит адрес VPN-сервера - и звонок идет через VPN: пинг 100+ мс, канал
// узкий, трансляция в квадратах. Поэтому при запуске (и раз в 10 минут)
// опрашиваем и российские, и зарубежные серверы: если адреса разные, берем
// только те серверы, что видят настоящий адрес.

import (
	"crypto/rand"
	"encoding/binary"
	"net"
	"sync"
	"time"
)

// сначала российские: при VPN с правилом «РФ напрямую» они видят настоящий адрес
var stunDirect = []string{"stun.sipnet.net:3478", "stun.sipnet.ru:5060", "stun.zadarma.com:3478", "stun.webcalldirect.com:3478"}
var stunForeign = []string{"stun.l.google.com:19302", "stun.cloudflare.com:3478"}

type StunInfo struct {
	Servers []string `json:"servers"` // stun:host:port для WebRTC
	VPN     bool     `json:"vpn"`     // зарубежные серверы видят другой адрес
	Direct  string   `json:"direct"`  // настоящий внешний адрес
	Other   string   `json:"other"`   // адрес, который видят через VPN
	Checked int64    `json:"checked"`
}

type StunPicker struct {
	mu    sync.Mutex
	info  StunInfo
	ready chan struct{} // закрывается после первой проверки
}

func newStunPicker(quit <-chan struct{}) *StunPicker {
	p := &StunPicker{ready: make(chan struct{})}
	go func() {
		first := true
		for {
			p.refresh()
			if first {
				first = false
				close(p.ready)
			}
			select {
			case <-quit:
				return
			case <-time.After(10 * time.Minute):
			}
		}
	}()
	return p
}

// Wait - ждет первую проверку не дольше d. Без нее браузер взял бы запасной
// список с зарубежным STUN, и через VPN соединение пошло бы через VPN-сервер.
func (p *StunPicker) Wait(d time.Duration) {
	select {
	case <-p.ready:
	case <-time.After(d):
	}
}

func (p *StunPicker) Info() StunInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.info
}

func (p *StunPicker) refresh() {
	all := append(append([]string{}, stunDirect...), stunForeign...)
	mapped := make([]string, len(all))
	var wg sync.WaitGroup
	for i, s := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mapped[i] = stunMappedIP(s)
		}()
	}
	wg.Wait()
	// настоящий адрес - тот, что видят российские серверы
	direct := ""
	for i := range stunDirect {
		if mapped[i] != "" {
			direct = mapped[i]
			break
		}
	}
	info := StunInfo{Checked: time.Now().Unix(), Direct: direct}
	for i, s := range all {
		switch {
		case mapped[i] == "":
		case direct == "" || mapped[i] == direct:
			info.Servers = append(info.Servers, "stun:"+s)
		default:
			info.VPN, info.Other = true, mapped[i]
		}
	}
	if len(info.Servers) == 0 { // ничего не ответило - пусть пробует все
		for _, s := range all {
			info.Servers = append(info.Servers, "stun:"+s)
		}
	}
	if info.VPN {
		logf("STUN: зарубежные серверы видят %s, российские - %s: похоже на VPN, беру только видящие настоящий адрес", info.Other, direct)
	}
	p.mu.Lock()
	p.info = info
	p.mu.Unlock()
}

// stunMappedIP - внешний адрес по ответу STUN (Binding Request, RFC 5389); пусто - нет ответа.
func stunMappedIP(server string) string {
	c, err := net.Dial("udp4", server)
	if err != nil {
		return ""
	}
	defer c.Close()
	req := make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:], 0x0001)
	binary.BigEndian.PutUint32(req[4:], 0x2112A442)
	_, _ = rand.Read(req[8:20])
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write(req); err != nil {
		return ""
	}
	buf := make([]byte, 1024)
	n, err := c.Read(buf)
	if err != nil || n < 20 {
		return ""
	}
	for i := 20; i+4 <= n; {
		t := binary.BigEndian.Uint16(buf[i:])
		l := int(binary.BigEndian.Uint16(buf[i+2:]))
		if i+4+l > n {
			break
		}
		v := buf[i+4 : i+4+l]
		if (t == 0x0020 || t == 0x0001) && l >= 8 && v[1] == 1 {
			ip := net.IP(append([]byte(nil), v[4:8]...))
			if t == 0x0020 { // XOR-MAPPED-ADDRESS
				for k, m := range []byte{0x21, 0x12, 0xA4, 0x42} {
					ip[k] ^= m
				}
			}
			return ip.String()
		}
		i += 4 + (l+3)/4*4
	}
	return ""
}
