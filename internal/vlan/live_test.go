package vlan

import (
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"testing"
	"time"
)

func csum(b []byte) uint16 {
	var s uint32
	for i := 0; i+1 < len(b); i += 2 {
		s += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		s += uint32(b[len(b)-1]) << 8
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return ^uint16(s)
}

// echo - ICMP echo request src -> dst.
func echo(src, dst netip.Addr, seq uint16) []byte {
	p := make([]byte, 20+8+16)
	p[0], p[8], p[9] = 0x45, 64, 1
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	s, d := src.As4(), dst.As4()
	copy(p[12:], s[:])
	copy(p[16:], d[:])
	binary.BigEndian.PutUint16(p[10:], csum(p[:20]))
	icmp := p[20:]
	icmp[0] = 8
	binary.BigEndian.PutUint16(icmp[4:], 0x5a)
	binary.BigEndian.PutUint16(icmp[6:], seq)
	copy(icmp[8:], "virtualZeno-live")
	binary.BigEndian.PutUint16(icmp[2:], csum(icmp))
	return p
}

// TestLive (ZENO_LIVE=1, служба virtualZeno поставлена): хаб A поднимает
// настоящий адаптер через службу, хаб B в этом же процессе - «друг» за pion.
// B шлет ICMP echo на адрес A, Windows отвечает, ответ возвращается к B.
func TestLive(t *testing.T) {
	if os.Getenv("ZENO_LIVE") == "" {
		t.Skip("ZENO_LIVE не задан")
	}
	ha, hb := NewHub("live-a", t.Logf), NewHub("live-b", t.Logf)
	defer ha.Stop()
	defer hb.Stop()
	got := make(chan []byte, 16)
	hb.toTun = func(p []byte) { got <- p }

	ha.SetWant(true)
	deadline := time.Now().Add(20 * time.Second)
	for !ha.Status(nil).Up {
		if time.Now().After(deadline) {
			t.Fatalf("сеть не поднялась: %+v", ha.Status(nil))
		}
		time.Sleep(200 * time.Millisecond)
	}
	out, _ := exec.Command("netsh", "interface", "ipv4", "show", "addresses", "name="+AdapterName).CombinedOutput()
	t.Logf("адаптер:\n%s", out)

	offer, err := ha.Offer("live-b", nil)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := hb.Accept("live-a", offer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ha.Answer("live-b", answer); err != nil {
		t.Fatal(err)
	}
	for ha.Status(nil).Links["live-b"].State != "connected" {
		if time.Now().After(deadline) {
			t.Fatal("pion не соединился")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if os.Getenv("ZENO_LIVE") == "debug" {
		ln, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IP(ha.me.AsSlice()), Port: 47777})
		if err != nil {
			t.Fatalf("адрес не рабочий: %v", err)
		}
		go func() {
			b := make([]byte, 100)
			n, from, err := ln.ReadFromUDP(b)
			t.Logf("UDP пришел: %q от %v %v", b[:n], from, err)
		}()
		hb.fromTun(udp(hb.me, ha.me, 47777, "udp-probe"))
		out, _ := exec.Command("ping", "-n", "1", "-w", "1000", "-S", ha.me.String(), hb.me.String()).CombinedOutput()
		t.Logf("ping наружу: %s", out)
		time.Sleep(time.Second)
		for len(got) > 0 {
			p := <-got
			src, dst, _ := header(p)
			t.Logf("у B: %s -> %s proto %d type %d", src, dst, p[9], p[20])
		}
		ln.Close()
	}
	for seq := uint16(1); seq <= 4; seq++ {
		start := time.Now()
		hb.fromTun(echo(hb.me, ha.me, seq))
	wait:
		for {
			select {
			case p := <-got:
				src, dst, _ := header(p)
				if dst != hb.me || p[9] != 1 || p[20] != 0 {
					t.Logf("попутный пакет %s -> %s, протокол %d", src, dst, p[9]) // IGMP, mDNS и т.п.
					continue
				}
				if src != ha.me {
					t.Fatalf("ответ не от того: %s", src)
				}
				t.Logf("ответ от %s, seq %d, %v", src, seq, time.Since(start))
				break wait
			case <-time.After(3 * time.Second):
				t.Fatalf("нет ответа на ping %d", seq)
			}
		}
	}

	ha.SetWant(false)
	time.Sleep(2 * time.Second)
	out, _ = exec.Command("netsh", "interface", "ipv4", "show", "addresses", "name="+AdapterName).CombinedOutput()
	t.Logf("после выключения:\n%s", out)
	route, _ := exec.Command("route", "print", "-4", "10.77.*").CombinedOutput()
	t.Logf("маршруты:\n%s", route)
}

func udp(src, dst netip.Addr, port uint16, payload string) []byte {
	p := make([]byte, 28+len(payload))
	p[0], p[8], p[9] = 0x45, 64, 17
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	s, d := src.As4(), dst.As4()
	copy(p[12:], s[:])
	copy(p[16:], d[:])
	binary.BigEndian.PutUint16(p[10:], csum(p[:20]))
	binary.BigEndian.PutUint16(p[20:], 40000)
	binary.BigEndian.PutUint16(p[22:], port)
	binary.BigEndian.PutUint16(p[24:], uint16(8+len(payload)))
	copy(p[28:], payload)
	return p
}
