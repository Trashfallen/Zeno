package main

import (
	"os"
	"testing"
	"time"
)

// Первый ответ net/stun должен приходить уже после проверки серверов,
// иначе браузер берет запасной список с зарубежным STUN.
func TestStunWaitLive(t *testing.T) {
	if os.Getenv("ZENO_NET_TEST") == "" {
		t.Skip("ZENO_NET_TEST=1 для живой проверки")
	}
	quit := make(chan struct{})
	defer close(quit)
	p := newStunPicker(quit)
	if p.Info().Checked != 0 {
		t.Fatal("проверка не могла закончиться мгновенно")
	}
	start := time.Now()
	p.Wait(20 * time.Second)
	info := p.Info()
	if info.Checked == 0 {
		t.Fatal("Wait вернулся без проверки")
	}
	t.Logf("ждали %v, direct=%s vpn=%v servers=%v", time.Since(start).Round(time.Millisecond), info.Direct, info.VPN, info.Servers)
}
