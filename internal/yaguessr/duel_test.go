package yaguessr

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type testRoom struct {
	t   *testing.T
	srv *httptest.Server
}

func newTestRoom(t *testing.T, logins ...string) *testRoom {
	dir := t.TempDir()
	for _, l := range logins {
		cmdAddUser([]string{"-data", dir, l, "p"})
	}
	if err := loadAuth(dir); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", handleLogin)
	mux.HandleFunc("/ws", serveWS)
	tr := &testRoom{t: t, srv: httptest.NewServer(mux)}
	t.Cleanup(tr.srv.Close)
	return tr
}

func (tr *testRoom) dial(login string) *websocket.Conn {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	c.Post(tr.srv.URL+"/api/login", "application/json", strings.NewReader(`{"login":"`+login+`","password":"p"}`))
	h := http.Header{}
	for _, ck := range jar.Cookies(mustURL(tr.srv.URL)) {
		h.Add("Cookie", ck.Name+"="+ck.Value)
	}
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(tr.srv.URL, "http")+"/ws", h)
	if err != nil {
		tr.t.Fatal(err)
	}
	return ws
}

func (tr *testRoom) read(ws *websocket.Conn, want func(map[string]any) bool) map[string]any {
	tr.t.Helper()
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var m map[string]any
		if err := ws.ReadJSON(&m); err != nil {
			tr.t.Fatal(err)
		}
		if want(m) {
			return m
		}
	}
}

func isState(s string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["t"] == "state" && m["state"] == s }
}

func playerByName(st map[string]any, name string) map[string]any {
	for _, p := range st["players"].([]any) {
		if pm := p.(map[string]any); pm["name"] == name {
			return pm
		}
	}
	return nil
}

// Дуэль: урон по разнице очков, дозапрос мест у хоста, выбывание и итоги.
func TestDuel(t *testing.T) {
	tr := newTestRoom(t, "alice", "bob")
	a := tr.dial("alice")
	a.WriteJSON(map[string]any{"t": "hello"})
	room := tr.read(a, func(m map[string]any) bool { return m["t"] == "welcome" })["room"].(string)
	b := tr.dial("bob")
	b.WriteJSON(map[string]any{"t": "hello", "room": room})
	tr.read(a, func(m map[string]any) bool { return m["t"] == "state" && len(m["players"].([]any)) == 2 })

	// Москва и Владивосток: промах почти 6400 км -> 0 очков
	moscow := Loc{Lat: 55.75, Lon: 37.61}
	settings := Settings{Region: "moscow", Mode: "duel", MaxDist: 40000}
	locs := []Loc{moscow, moscow}
	a.WriteJSON(map[string]any{"t": "start", "settings": settings, "locs": locs})
	tr.read(a, isState("round"))

	round := 1
	for {
		a.WriteJSON(map[string]any{"t": "guess", "round": round, "lat": moscow.Lat, "lon": moscow.Lon})
		b.WriteJSON(map[string]any{"t": "guess", "round": round, "lat": 43.11, "lon": 131.88})
		st := tr.read(a, func(m map[string]any) bool {
			if m["t"] == "needLocs" {
				// хост досылает места по запросу сервера
				a.WriteJSON(map[string]any{"t": "moreLocs", "locs": []Loc{moscow, moscow}})
				return false
			}
			return m["t"] == "state" && m["state"] == "reveal" && m["round"].(float64) == float64(round)
		})
		bob := playerByName(st, "bob")
		dmg := st["results"].([]any)[0].(map[string]any)["damage"].(map[string]any)
		want := int(5000 * duelMult(round))
		if int(dmg[bob["id"].(string)].(float64)) != want {
			t.Fatalf("раунд %d: урон bob %v, ждали %d", round, dmg, want)
		}
		if bob["out"] == true {
			if st["last"] != true {
				t.Fatal("bob выбыл, но игра не заканчивается")
			}
			if round != 2 { // 6000 - 5000 - 5000 < 0 на втором раунде
				t.Fatalf("bob выбыл на раунде %d", round)
			}
			break
		}
		a.WriteJSON(map[string]any{"t": "next"})
		round++
		tr.read(a, func(m map[string]any) bool {
			return m["t"] == "state" && m["state"] == "round" && m["round"].(float64) == float64(round)
		})
	}
	a.WriteJSON(map[string]any{"t": "next"})
	fin := tr.read(a, isState("final"))
	if hp := playerByName(fin, "alice")["hp"].(float64); hp != duelHP {
		t.Fatalf("у alice должно остаться %d HP, а не %v", duelHP, hp)
	}
}

// Таймер после первого ответа и выгон игрока.
func TestGuessTimerAndKick(t *testing.T) {
	tr := newTestRoom(t, "alice", "bob")
	a := tr.dial("alice")
	a.WriteJSON(map[string]any{"t": "hello"})
	room := tr.read(a, func(m map[string]any) bool { return m["t"] == "welcome" })["room"].(string)
	b := tr.dial("bob")
	b.WriteJSON(map[string]any{"t": "hello", "room": room})
	st := tr.read(a, func(m map[string]any) bool { return m["t"] == "state" && len(m["players"].([]any)) == 2 })
	bobID := playerByName(st, "bob")["id"].(string)

	loc := Loc{Lat: 55.75, Lon: 37.61}
	settings := Settings{Region: "moscow", Mode: "classic", Rounds: 1, Timer: 120, GuessTimer: 1, MaxDist: 40000}
	a.WriteJSON(map[string]any{"t": "start", "settings": settings, "locs": []Loc{loc}})
	tr.read(a, isState("round"))
	a.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": loc.Lat, "lon": loc.Lon})
	st = tr.read(b, func(m map[string]any) bool {
		return m["t"] == "state" && playerByName(m, "alice")["guessed"] == true
	})
	if left := st["left"].(float64); left > float64((1+revealGraceSec)*1000) {
		t.Fatalf("после ответа осталось %v мс, таймер не сократился", left)
	}
	// bob молчит: раунд закрывается по сокращенному таймеру
	tr.read(a, isState("reveal"))

	b.WriteJSON(map[string]any{"t": "kick", "id": playerByName(st, "alice")["id"]}) // не хост, игнор
	a.WriteJSON(map[string]any{"t": "kick", "id": bobID})
	tr.read(b, func(m map[string]any) bool { return m["t"] == "kicked" })
	st = tr.read(a, func(m map[string]any) bool { return m["t"] == "state" && len(m["players"].([]any)) == 1 })
	if playerByName(st, "bob") != nil {
		t.Fatal("bob остался в комнате")
	}
}

// Воздушные панорамы ведут свой запас мест, отдельный от уличных.
func TestAirLayerPool(t *testing.T) {
	tr := newTestRoom(t, "alice")
	a := tr.dial("alice")
	a.WriteJSON(map[string]any{"t": "hello"})
	tr.read(a, func(m map[string]any) bool { return m["t"] == "welcome" })

	loc := Loc{Lat: 59.94, Lon: 30.31}
	settings := Settings{Region: "spb-air-test", Mode: "classic", Rounds: 1, Layer: "air", MaxDist: 40000}
	a.WriteJSON(map[string]any{"t": "start", "settings": settings, "locs": []Loc{loc}})
	st := tr.read(a, isState("round"))
	if st["settings"].(map[string]any)["layer"] != "air" {
		t.Fatalf("слой не сохранился: %v", st["settings"])
	}

	mu.Lock()
	defer mu.Unlock()
	if len(pool.Regions["spb-air-test@air"]) != 1 || len(pool.Regions["spb-air-test"]) != 0 {
		t.Fatalf("запас: air=%d ground=%d", len(pool.Regions["spb-air-test@air"]), len(pool.Regions["spb-air-test"]))
	}
}

// «Нет панорамы»: игрок просит другое место, хост присылает замену, ответы раунда сбрасываются.
func TestReroll(t *testing.T) {
	tr := newTestRoom(t, "alice", "bob")
	a := tr.dial("alice")
	a.WriteJSON(map[string]any{"t": "hello"})
	room := tr.read(a, func(m map[string]any) bool { return m["t"] == "welcome" })["room"].(string)
	b := tr.dial("bob")
	b.WriteJSON(map[string]any{"t": "hello", "room": room})
	tr.read(a, func(m map[string]any) bool { return m["t"] == "state" && len(m["players"].([]any)) == 2 })

	old := Loc{Lat: 48.85, Lon: 2.35}
	settings := Settings{Region: "g_europe", Provider: "google", Mode: "classic", Rounds: 1, MaxDist: 4000000}
	a.WriteJSON(map[string]any{"t": "start", "settings": settings, "locs": []Loc{old}})
	tr.read(b, isState("round"))
	a.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": old.Lat, "lon": old.Lon})
	tr.read(b, func(m map[string]any) bool {
		return m["t"] == "state" && playerByName(m, "alice")["guessed"] == true
	})

	b.WriteJSON(map[string]any{"t": "reroll", "round": 1})
	nr := tr.read(a, func(m map[string]any) bool { return m["t"] == "needReroll" })
	if nr["by"] != "bob" {
		t.Fatalf("хост не узнал, кто просил: %v", nr)
	}
	repl := Loc{Lat: 52.52, Lon: 13.40, Dir: 90}
	b.WriteJSON(map[string]any{"t": "replaceLoc", "round": 1, "locs": []Loc{{Lat: 1, Lon: 1}}}) // не хост, игнор
	a.WriteJSON(map[string]any{"t": "replaceLoc", "round": 1, "locs": []Loc{repl}})
	st := tr.read(b, func(m map[string]any) bool {
		l, ok := m["loc"].(map[string]any)
		return m["t"] == "state" && ok && l["lat"].(float64) == repl.Lat
	})
	if al := playerByName(st, "alice"); al["guessed"] == true || al["score"].(float64) != 0 {
		t.Fatalf("ответ по старому месту не сброшен: %v", al)
	}
	if st["settings"].(map[string]any)["provider"] != "google" {
		t.Fatalf("сервис панорам не сохранился: %v", st["settings"])
	}
}

// Хост заканчивает игру досрочно: итоги по сыгранным раундам, раунд без ответов не считается.
func TestEndGame(t *testing.T) {
	tr := newTestRoom(t, "alice", "bob")
	a := tr.dial("alice")
	a.WriteJSON(map[string]any{"t": "hello"})
	room := tr.read(a, func(m map[string]any) bool { return m["t"] == "welcome" })["room"].(string)
	b := tr.dial("bob")
	b.WriteJSON(map[string]any{"t": "hello", "room": room})
	tr.read(a, func(m map[string]any) bool { return m["t"] == "state" && len(m["players"].([]any)) == 2 })

	locs := []Loc{{Lat: 48.85, Lon: 2.35}, {Lat: 52.52, Lon: 13.40}, {Lat: 41.90, Lon: 12.49}}
	settings := Settings{Region: "g_europe", Provider: "google", Mode: "classic", Rounds: 3, MaxDist: 4000000}

	// без единого ответа в первом раунде игра возвращается в лобби
	a.WriteJSON(map[string]any{"t": "start", "settings": settings, "locs": locs})
	tr.read(b, isState("round"))
	a.WriteJSON(map[string]any{"t": "end"})
	tr.read(b, isState("lobby"))

	a.WriteJSON(map[string]any{"t": "start", "settings": settings, "locs": locs})
	tr.read(b, isState("round"))
	a.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": 48.85, "lon": 2.35})
	b.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": 48.9, "lon": 2.4})
	tr.read(a, isState("reveal"))
	a.WriteJSON(map[string]any{"t": "next"})
	tr.read(b, func(m map[string]any) bool { return isState("round")(m) && m["round"].(float64) == 2 })

	b.WriteJSON(map[string]any{"t": "end"}) // не хост, игнор
	a.WriteJSON(map[string]any{"t": "end"})
	st := tr.read(b, isState("final"))
	if res := st["results"].([]any); len(res) != 1 {
		t.Fatalf("в итогах должен быть 1 сыгранный раунд, а их %d", len(res))
	}
	if al := playerByName(st, "alice"); al["score"].(float64) != 5000 {
		t.Fatalf("очки первого раунда потерялись: %v", al)
	}
}

// Метка без «Угадать» засчитывается, когда раунд кончается.
func TestAutoGuessMark(t *testing.T) {
	tr := newTestRoom(t, "alice", "bob")
	a := tr.dial("alice")
	a.WriteJSON(map[string]any{"t": "hello"})
	room := tr.read(a, func(m map[string]any) bool { return m["t"] == "welcome" })["room"].(string)
	b := tr.dial("bob")
	b.WriteJSON(map[string]any{"t": "hello", "room": room})
	tr.read(a, func(m map[string]any) bool { return m["t"] == "state" && len(m["players"].([]any)) == 2 })

	locs := []Loc{{Lat: 48.85, Lon: 2.35}, {Lat: 52.52, Lon: 13.40}}
	settings := Settings{Region: "g_europe", Provider: "google", Mode: "classic", Rounds: 2, MaxDist: 4000000, GuessTimer: 1}
	a.WriteJSON(map[string]any{"t": "start", "settings": settings, "locs": locs})
	tr.read(b, isState("round"))
	b.WriteJSON(map[string]any{"t": "mark", "round": 1, "lat": 10, "lon": 10}) // передумал
	b.WriteJSON(map[string]any{"t": "mark", "round": 1, "lat": 48.85, "lon": 2.35})
	a.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": 48.9, "lon": 2.4})
	// после ответа alice у bob осталась 1 с (+ запас), потом раунд кончается сам
	st := tr.read(a, isState("reveal"))
	if bob := playerByName(st, "bob"); bob["score"].(float64) != 5000 {
		t.Fatalf("метка bob не засчитана: %v", bob)
	}

	// метка прошлого раунда в следующем не считается
	a.WriteJSON(map[string]any{"t": "next"})
	tr.read(b, func(m map[string]any) bool { return isState("round")(m) && m["round"].(float64) == 2 })
	a.WriteJSON(map[string]any{"t": "guess", "round": 2, "lat": 52.52, "lon": 13.40})
	st = tr.read(a, func(m map[string]any) bool { return isState("reveal")(m) && m["round"].(float64) == 2 })
	if bob := playerByName(st, "bob"); bob["score"].(float64) != 5000 {
		t.Fatalf("метка прошлого раунда засчитана снова: %v", bob)
	}
}
