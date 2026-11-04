package yaguessr

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Полная игра на двоих: вход, комната, 2 раунда, итоги.
func TestTwoPlayerGame(t *testing.T) {
	dir := t.TempDir()
	cmdAddUser([]string{"-data", dir, "alice", "pa"})
	cmdAddUser([]string{"-data", dir, "bob", "pb"})
	if err := loadAuth(dir); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", handleLogin)
	mux.Handle("/api/nick", requireAuth(http.HandlerFunc(handleNick)))
	mux.HandleFunc("/ws", serveWS)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	login := func(user, pass string) *http.Client {
		jar, _ := cookiejar.New(nil)
		c := &http.Client{Jar: jar}
		res, err := c.Post(srv.URL+"/api/login", "application/json",
			strings.NewReader(`{"login":"`+user+`","password":"`+pass+`"}`))
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("login %s: %v %v", user, err, res.StatusCode)
		}
		c.Post(srv.URL+"/api/nick", "application/json", strings.NewReader(`{"nick":"`+user+`-nick"}`))
		return c
	}
	if res, _ := http.Post(srv.URL+"/api/login", "application/json",
		strings.NewReader(`{"login":"alice","password":"bad"}`)); res.StatusCode != 401 {
		t.Fatal("плохой пароль принят")
	}

	dial := func(c *http.Client) *websocket.Conn {
		u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
		h := http.Header{}
		for _, ck := range c.Jar.Cookies(mustURL(srv.URL)) {
			h.Add("Cookie", ck.Name+"="+ck.Value)
		}
		ws, _, err := websocket.DefaultDialer.Dial(u, h)
		if err != nil {
			t.Fatal(err)
		}
		return ws
	}
	read := func(ws *websocket.Conn, want func(map[string]any) bool) map[string]any {
		ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			var m map[string]any
			if err := ws.ReadJSON(&m); err != nil {
				t.Fatal(err)
			}
			if want(m) {
				return m
			}
		}
	}
	state := func(s string) func(map[string]any) bool {
		return func(m map[string]any) bool { return m["t"] == "state" && m["state"] == s }
	}

	// без входа WebSocket закрыт
	if _, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil); err == nil {
		t.Fatal("ws без входа открылся")
	}

	a := dial(login("alice", "pa"))
	a.WriteJSON(map[string]any{"t": "hello"})
	w := read(a, func(m map[string]any) bool { return m["t"] == "welcome" })
	room := w["room"].(string)

	b := dial(login("bob", "pb"))
	b.WriteJSON(map[string]any{"t": "hello", "room": room})
	st := read(b, func(m map[string]any) bool { return m["t"] == "state" && len(m["players"].([]any)) == 2 })
	if p := st["players"].([]any)[1].(map[string]any); p["name"] != "bob-nick" {
		t.Fatalf("ник не подтянулся: %v", p["name"])
	}

	// не хост стартовать не может
	locs := []Loc{{Lat: 55.75, Lon: 37.61, Dir: 10}, {Lat: 59.93, Lon: 30.33, Dir: 20}}
	settings := Settings{Region: "moscow", Rounds: 2, MaxDist: 40000}
	b.WriteJSON(map[string]any{"t": "start", "settings": settings, "locs": locs})
	a.WriteJSON(map[string]any{"t": "start", "settings": settings, "locs": locs})
	read(b, state("round"))

	a.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": 55.75, "lon": 37.61})
	b.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": 55.80, "lon": 37.61})
	rv := read(b, state("reveal"))
	gs := rv["results"].([]any)[0].(map[string]any)["guesses"].([]any)
	if len(gs) != 2 || gs[0].(map[string]any)["score"].(float64) != 5000 {
		t.Fatalf("неверные ответы: %v", gs)
	}
	if bs := gs[1].(map[string]any)["score"].(float64); bs <= 0 || bs >= 5000 {
		t.Fatalf("неверные очки bob: %v", bs)
	}

	b.WriteJSON(map[string]any{"t": "next"}) // не хост, игнор
	a.WriteJSON(map[string]any{"t": "next"})
	read(a, state("round"))

	// bob отваливается, раунд закрывается ответом alice
	b.Close()
	a.WriteJSON(map[string]any{"t": "guess", "round": 2, "lat": 59.93, "lon": 30.33})
	read(a, state("reveal"))
	a.WriteJSON(map[string]any{"t": "next"})
	fin := read(a, state("final"))
	if len(fin["results"].([]any)) != 2 {
		t.Fatal("в итогах не 2 раунда")
	}

	// bob возвращается на свое место
	b = dial(login("bob", "pb"))
	b.WriteJSON(map[string]any{"t": "hello", "room": room})
	st = read(b, state("final"))
	if n := len(st["players"].([]any)); n != 2 {
		b, _ := json.Marshal(st)
		t.Fatalf("после переподключения игроков %d: %s", n, b)
	}
}

func mustURL(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}

// Свои карты: создать, получить список, переименовать, удалить.
func TestMapsAPI(t *testing.T) {
	dir := t.TempDir()
	cmdAddUser([]string{"-data", dir, "alice", "pa"})
	if err := loadAuth(dir); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", handleLogin)
	mux.Handle("/api/maps", requireAuth(http.HandlerFunc(handleMaps)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if res, _ := c.Get(srv.URL + "/api/maps"); res.StatusCode != 401 {
		t.Fatal("карты доступны без входа")
	}
	c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"login":"alice","password":"pa"}`))

	post := func(body string) (int, map[string]any) {
		res, err := c.Post(srv.URL+"/api/maps", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.NewDecoder(res.Body).Decode(&m)
		return res.StatusCode, m
	}
	if code, _ := post(`{"name":"Пусто","zones":[]}`); code != 400 {
		t.Fatal("карта без областей сохранилась")
	}
	if code, _ := post(`{"name":"Плохая","zones":[{"c":[95,10],"r":5}]}`); code != 400 {
		t.Fatal("неверные координаты приняты")
	}
	code, m := post(`{"name":"Питер и Казань","zones":[{"c":[59.93,30.33],"r":5},{"c":[55.79,49.1],"r":3}]}`)
	if code != 200 || m["id"] == "" || m["author"] != "alice" {
		t.Fatalf("создание: %d %v", code, m)
	}
	id := m["id"].(string)
	if z := m["zones"].([]any)[0].(map[string]any); z["w"].(float64) != 25 {
		t.Fatalf("вес по умолчанию не r^2: %v", z["w"])
	}
	if code, m = post(`{"id":"` + id + `","name":"Питер","zones":[{"c":[59.93,30.33],"r":5}]}`); code != 200 || m["id"] != id {
		t.Fatalf("обновление: %d %v", code, m)
	}

	res, _ := c.Get(srv.URL + "/api/maps")
	var list []map[string]any
	json.NewDecoder(res.Body).Decode(&list)
	if len(list) != 1 || list[0]["name"] != "Питер" || len(list[0]["zones"].([]any)) != 1 {
		t.Fatalf("список: %v", list)
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/maps?id="+id, nil)
	c.Do(req)
	res, _ = c.Get(srv.URL + "/api/maps")
	list = nil
	json.NewDecoder(res.Body).Decode(&list)
	if len(list) != 0 {
		t.Fatalf("после удаления осталось %d", len(list))
	}
}
