package vlan

import (
	"net/netip"
	"testing"
	"time"
)

func TestAddr(t *testing.T) {
	a := Addr("ключ-друга")
	if a != Addr("ключ-друга") || !subnet.Contains(a) || toAll(a) {
		t.Fatalf("адрес %s", a)
	}
	b := a.As4()
	if b[3] == 0 || b[3] == 255 {
		t.Fatalf("адрес сети или широковещательный: %s", a)
	}
}

// ipv4 - минимальный заголовок IPv4 + полезная нагрузка.
func ipv4(src, dst netip.Addr, payload string) []byte {
	p := make([]byte, 20+len(payload))
	p[0] = 0x45
	s, d := src.As4(), dst.As4()
	copy(p[12:], s[:])
	copy(p[16:], d[:])
	copy(p[20:], payload)
	return p
}

// TestLink: два хаба в одном процессе соединяются pion и гоняют пакеты:
// адресный, широковещательный и поддельный (должен пропасть).
func TestLink(t *testing.T) {
	got := map[string]chan []byte{"a": make(chan []byte, 8), "b": make(chan []byte, 8)}
	ha, hb := NewHub("a", t.Logf), NewHub("b", t.Logf)
	defer ha.Stop()
	defer hb.Stop()
	ha.toTun = func(p []byte) { got["a"] <- p }
	hb.toTun = func(p []byte) { got["b"] <- p }

	offer, err := ha.Offer("b", nil)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := hb.Accept("a", offer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ha.Answer("b", answer); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for ha.Status(nil).Links["b"].State != "connected" || hb.Status(nil).Links["a"].State != "connected" {
		if time.Now().After(deadline) {
			t.Fatalf("не соединились: %+v %+v", ha.Status(nil).Links, hb.Status(nil).Links)
		}
		time.Sleep(50 * time.Millisecond)
	}

	wait := func(side, want string) {
		t.Helper()
		select {
		case p := <-got[side]:
			if string(p[20:]) != want {
				t.Fatalf("%s получил %q, ждали %q", side, p[20:], want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s не получил %q", side, want)
		}
	}
	ha.fromTun(ipv4(ha.me, hb.me, "привет"))
	wait("b", "привет")
	hb.fromTun(ipv4(hb.me, bcastAll, "кто тут"))
	wait("a", "кто тут")
	ha.fromTun(ipv4(ha.me, bcastNet, "игра"))
	wait("b", "игра")

	// чужой адрес отправителя и адрес вне сети не проходят
	ha.fromTun(ipv4(netip.MustParseAddr("10.77.1.1"), hb.me, "подделка"))
	ha.fromTun(ipv4(ha.me, netip.MustParseAddr("192.0.2.1"), "мимо"))
	ha.fromTun(ipv4(ha.me, hb.me, "после"))
	wait("b", "после")

	time.Sleep(2500 * time.Millisecond)
	if rtt := ha.Status(nil).Links["b"].RTT; rtt <= 0 {
		t.Fatalf("пинг не измерен: %v", rtt)
	}
	ha.Close("b")
	if st := ha.Status([]string{"b"}).Links["b"]; st.State != "none" || st.Addr != hb.me.String() {
		t.Fatalf("после закрытия: %+v", st)
	}
}
