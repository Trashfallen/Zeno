package vlan

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestDaemonPipe: служба в этом же процессе (без прав администратора) -
// приветствие по каналу, ошибка подъема сети без wintun.dll рядом, пакет без
// сети молча пропадает, после отключения клиента канал принимает новый.
func TestDaemonPipe(t *testing.T) {
	if ServiceState() != "missing" {
		t.Skip("служба virtualZeno стоит - канал занят ею")
	}
	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	d := newDaemon(sid)
	d.dir = t.TempDir()
	go d.serve()
	defer d.close()

	for round := range 2 {
		c, err := dialPipe(3 * time.Second)
		if err != nil {
			t.Fatal(err)
		}
		hello, _ := json.Marshal(map[string]any{"proto": Proto})
		if err := c.write(fHello, hello); err != nil {
			t.Fatal(err)
		}
		typ, b, err := c.read()
		if err != nil || typ != fHello || !strings.Contains(string(b), `"proto":1`) {
			t.Fatalf("приветствие: %c %s %v", typ, b, err)
		}
		_ = c.write(fPacket, ipv4(Addr("a"), Addr("b"), "в никуда"))
		up, _ := json.Marshal(map[string]any{"ip": Addr("a").String()})
		_ = c.write(fUp, up)
		typ, b, err = c.read()
		if err != nil || typ != fStatus || !strings.Contains(string(b), "wintun.dll") {
			t.Fatalf("раунд %d, подъем без wintun.dll: %c %s %v", round, typ, b, err)
		}
		bad, _ := json.Marshal(map[string]any{"ip": "192.0.2.7"})
		_ = c.write(fUp, bad)
		if typ, b, _ = c.read(); !strings.Contains(string(b), "не из") {
			t.Fatalf("чужой адрес: %c %s", typ, b)
		}
		c.Close()
	}
}
