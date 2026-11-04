package yaguessr

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// pair - комната с alice (хост) и bob, оба в лобби.
func pair(t *testing.T) (*testRoom, *websocket.Conn, *websocket.Conn) {
	tr := newTestRoom(t, "alice", "bob")
	// ники могли остаться от других тестов: здесь игроков ищем по логину
	authMu.Lock()
	delete(nicks, "alice")
	delete(nicks, "bob")
	authMu.Unlock()
	a := tr.dial("alice")
	a.WriteJSON(map[string]any{"t": "hello"})
	room := tr.read(a, func(m map[string]any) bool { return m["t"] == "welcome" })["room"].(string)
	b := tr.dial("bob")
	b.WriteJSON(map[string]any{"t": "hello", "room": room})
	tr.read(a, func(m map[string]any) bool { return m["t"] == "state" && len(m["players"].([]any)) == 2 })
	return tr, a, b
}

func roundIs(state string, n int) func(map[string]any) bool {
	return func(m map[string]any) bool { return isState(state)(m) && m["round"].(float64) == float64(n) }
}

var paris = Loc{Lat: 48.85, Lon: 2.35}
var berlin = Loc{Lat: 52.52, Lon: 13.40}

// Канат: разница очков раунда тянет канат, при перетягивании до tugWin игра кончается.
func TestTug(t *testing.T) {
	tr, a, b := pair(t)
	s := Settings{Region: "g_europe", Provider: "google", Mode: "tug", MaxDist: 4000000}
	a.WriteJSON(map[string]any{"t": "start", "settings": s, "locs": []Loc{paris, berlin, paris}})
	tr.read(b, isState("round"))
	a.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": paris.Lat, "lon": paris.Lon})
	b.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": berlin.Lat, "lon": berlin.Lon})
	st := tr.read(a, isState("reveal"))
	al, bo := playerByName(st, "alice"), playerByName(st, "bob")
	if al["pull"].(float64) <= 0 || al["pull"].(float64) != -bo["pull"].(float64) {
		t.Fatalf("канат не сдвинулся к alice: %v %v", al["pull"], bo["pull"])
	}
	if st["tugWin"].(float64) != tugWin || st["last"] == true {
		t.Fatalf("игра не должна кончиться после одного раунда: %v", st["last"])
	}
}

// Кто быстрее: первый получает бонус, множитель виден в ответе.
func TestSpeed(t *testing.T) {
	tr, a, b := pair(t)
	s := Settings{Region: "g_europe", Provider: "google", Mode: "speed", Rounds: 1, MaxDist: 4000000}
	a.WriteJSON(map[string]any{"t": "start", "settings": s, "locs": []Loc{paris}})
	st := tr.read(b, isState("round"))
	if st["settings"].(map[string]any)["timer"].(float64) != 60 {
		t.Fatalf("кто быстрее без таймера: %v", st["settings"])
	}
	a.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": paris.Lat, "lon": paris.Lon})
	// ответы идут по разным соединениям: bob отвечает, только когда ответ alice уже принят
	tr.read(b, func(m map[string]any) bool { return isState("round")(m) && playerByName(m, "alice")["guessed"] == true })
	b.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": paris.Lat, "lon": paris.Lon})
	st = tr.read(a, isState("reveal"))
	gs := st["results"].([]any)[0].(map[string]any)["guesses"].([]any)
	var am, bm float64
	for _, g := range gs {
		gm := g.(map[string]any)
		if gm["name"] == "alice" {
			am = gm["mult"].(float64)
		} else {
			bm = gm["mult"].(float64)
		}
	}
	if math.Abs(am-1.2) > 0.01 || math.Abs(bm-1) > 0.01 {
		t.Fatalf("множители: первый %v (ждали 1.2), второй %v (ждали 1)", am, bm)
	}
}

// Ставки: близкий ответ с x3 утраивает очки, промах с x2 дает штраф.
func TestBets(t *testing.T) {
	tr, a, b := pair(t)
	s := Settings{Region: "g_europe", Provider: "google", Mode: "bets", Rounds: 1, MaxDist: 4000000}
	a.WriteJSON(map[string]any{"t": "start", "settings": s, "locs": []Loc{paris}})
	tr.read(b, isState("round"))
	a.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": paris.Lat, "lon": paris.Lon, "bet": 3})
	b.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": berlin.Lat, "lon": berlin.Lon, "bet": 2})
	st := tr.read(a, isState("reveal"))
	if sc := playerByName(st, "alice")["score"].(float64); sc != 15000 {
		t.Fatalf("x3 в точку: %v, ждали 15000", sc)
	}
	if sc := playerByName(st, "bob")["score"].(float64); sc != -betPenalty {
		t.Fatalf("x2 мимо: %v, ждали %d", sc, -betPenalty)
	}
}

// Общая экспедиция: в зачет середина между ответами, цель зависит от уровня.
func TestCoopMiddle(t *testing.T) {
	tr, a, b := pair(t)
	s := Settings{Region: "g_europe", Provider: "google", Mode: "coop", CoopLevel: "tourist", CoopRule: "middle",
		Rounds: 2, MaxDist: 4000000}
	a.WriteJSON(map[string]any{"t": "start", "settings": s, "locs": []Loc{paris, berlin}})
	tr.read(b, isState("round"))
	// ответы по разные стороны от Парижа: середина почти в Париже
	a.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": paris.Lat, "lon": paris.Lon - 1})
	b.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": paris.Lat, "lon": paris.Lon + 1})
	st := tr.read(a, isState("reveal"))
	rv := st["results"].([]any)[0].(map[string]any)
	if rv["team"].(float64) < 4990 || rv["mid"] == nil {
		t.Fatalf("середина ответов должна дать почти 5000: %v", rv["team"])
	}
	if st["teamGoal"].(float64) != 2500*2 || st["team"].(float64) != rv["team"].(float64) {
		t.Fatalf("цель или счет команды: %v из %v", st["team"], st["teamGoal"])
	}
}

// Штурман и разведчик: разведчик видит место и не отвечает, штурман не видит места, роли меняются.
func TestScout(t *testing.T) {
	tr, a, b := pair(t)
	s := Settings{Region: "g_europe", Provider: "google", Mode: "scout", Rounds: 2, MaxDist: 4000000}
	a.WriteJSON(map[string]any{"t": "start", "settings": s, "locs": []Loc{paris, berlin}})
	sa := tr.read(a, isState("round"))
	sb := tr.read(b, isState("round"))
	scout := sa["scout"].(string)
	var scoutWS, navWS *websocket.Conn
	if scout == playerByName(sa, "alice")["id"] {
		scoutWS, navWS = a, b
		if sa["loc"] == nil || sb["loc"] != nil {
			t.Fatalf("место видит только разведчик: alice %v, bob %v", sa["loc"], sb["loc"])
		}
	} else {
		scoutWS, navWS = b, a
		if sb["loc"] == nil || sa["loc"] != nil {
			t.Fatalf("место видит только разведчик: alice %v, bob %v", sa["loc"], sb["loc"])
		}
	}
	scoutWS.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": paris.Lat, "lon": paris.Lon}) // игнор
	navWS.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": paris.Lat, "lon": paris.Lon})
	st := tr.read(a, isState("reveal"))
	if st["team"].(float64) != 5000 || len(st["results"].([]any)[0].(map[string]any)["guesses"].([]any)) != 1 {
		t.Fatalf("засчитан должен быть только ответ штурмана: %v", st["results"])
	}
	a.WriteJSON(map[string]any{"t": "next"})
	st = tr.read(a, roundIs("round", 2))
	if st["scout"] == scout {
		t.Fatal("разведчик не сменился во втором раунде")
	}
}

// Два взгляда: игроки смотрят с разных точек и угадывают каждый свою.
func TestTwoViews(t *testing.T) {
	tr, a, b := pair(t)
	alt := Loc{Lat: 48.852, Lon: 2.352}
	loc := paris
	loc.Alt = &alt
	s := Settings{Region: "g_europe", Provider: "yandex", Mode: "twoviews", Rounds: 1, MaxDist: 4000000}
	a.WriteJSON(map[string]any{"t": "start", "settings": s, "locs": []Loc{loc}})
	sa := tr.read(a, isState("round"))
	sb := tr.read(b, isState("round"))
	if sa["settings"].(map[string]any)["provider"] != "google" {
		t.Fatal("два взгляда работают только на Google")
	}
	if sa["loc"].(map[string]any)["lat"] != paris.Lat || sb["loc"].(map[string]any)["lat"] != alt.Lat {
		t.Fatalf("места у игроков: alice %v, bob %v", sa["loc"], sb["loc"])
	}
	a.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": paris.Lat, "lon": paris.Lon})
	b.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": alt.Lat, "lon": alt.Lon})
	st := tr.read(a, isState("reveal"))
	if playerByName(st, "alice")["score"].(float64) != 5000 || playerByName(st, "bob")["score"].(float64) != 5000 {
		t.Fatal("каждый угадал свою точку, должно быть по 5000")
	}
}

// Загадай место: каждый загадывает точку, угадывает загаданную соперником.
func TestCustomPick(t *testing.T) {
	old := gsvNearest
	gsvNearest = func(lat, lon float64) (Loc, error) { return Loc{Lat: lat + 0.001, Lon: lon}, nil }
	defer func() { gsvNearest = old }()

	tr, a, b := pair(t)
	s := Settings{Region: "g_europe", Provider: "google", Mode: "custom", Rounds: 2, MaxDist: 4000000}
	a.WriteJSON(map[string]any{"t": "start", "settings": s})
	tr.read(b, isState("pick"))
	a.WriteJSON(map[string]any{"t": "pick", "lat": paris.Lat, "lon": paris.Lon})
	tr.read(a, func(m map[string]any) bool { return m["t"] == "picked" })
	b.WriteJSON(map[string]any{"t": "pick", "lat": berlin.Lat, "lon": berlin.Lon})
	sa := tr.read(a, isState("round"))
	sb := tr.read(b, isState("round"))
	// alice угадывает место bob (Берлин), bob - место alice (Париж)
	if math.Abs(sa["loc"].(map[string]any)["lat"].(float64)-berlin.Lat) > 0.01 ||
		math.Abs(sb["loc"].(map[string]any)["lat"].(float64)-paris.Lat) > 0.01 {
		t.Fatalf("места перепутаны: alice %v, bob %v", sa["loc"], sb["loc"])
	}
	a.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": berlin.Lat, "lon": berlin.Lon})
	b.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": berlin.Lat, "lon": berlin.Lon})
	st := tr.read(a, isState("reveal"))
	if playerByName(st, "alice")["score"].(float64) < 4900 || playerByName(st, "bob")["score"].(float64) > 1000 {
		t.Fatalf("очки посчитаны не по своим местам: %v", st["players"])
	}
	rv := st["results"].([]any)[0].(map[string]any)
	if rv["from"].(map[string]any)[playerByName(st, "alice")["id"].(string)] != playerByName(st, "bob")["id"] {
		t.Fatalf("не видно, кто загадал: %v", rv["from"])
	}
	a.WriteJSON(map[string]any{"t": "next"})
	tr.read(b, isState("pick"))
}

// Предзагрузка: в итогах раунда каждый получает свое следующее место, штурман - нет.
func TestNextPreload(t *testing.T) {
	tr, a, b := pair(t)
	alt := Loc{Lat: 52.521, Lon: 13.402}
	second := berlin
	second.Alt = &alt
	s := Settings{Region: "g_europe", Provider: "google", Mode: "twoviews", Rounds: 2, MaxDist: 4000000}
	a.WriteJSON(map[string]any{"t": "start", "settings": s, "locs": []Loc{paris, second}})
	tr.read(b, isState("round"))
	a.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": paris.Lat, "lon": paris.Lon})
	b.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": paris.Lat, "lon": paris.Lon})
	sa := tr.read(a, isState("reveal"))
	sb := tr.read(b, isState("reveal"))
	if sa["next"].(map[string]any)["lat"] != berlin.Lat || sb["next"].(map[string]any)["lat"] != alt.Lat {
		t.Fatalf("следующие места: alice %v, bob %v", sa["next"], sb["next"])
	}
	if sa["next"].(map[string]any)["alt"] != nil {
		t.Fatal("вторая точка не должна уходить игрокам")
	}
	a.WriteJSON(map[string]any{"t": "next"})
	tr.read(a, roundIs("round", 2))
	a.WriteJSON(map[string]any{"t": "guess", "round": 2, "lat": paris.Lat, "lon": paris.Lon})
	b.WriteJSON(map[string]any{"t": "guess", "round": 2, "lat": paris.Lat, "lon": paris.Lon})
	if st := tr.read(a, isState("reveal")); st["next"] != nil {
		t.Fatalf("после последнего раунда следующего места нет: %v", st["next"])
	}
}

// Замеры: сводка принимается и пишется в журнал, мусор отклоняется.
func TestMetrics(t *testing.T) {
	ok := httptest.NewRecorder()
	handleMetrics(ok, httptest.NewRequest(http.MethodPost, "/api/metrics", strings.NewReader(`{"tiles":3,"tileP50":400}`)))
	if ok.Code != http.StatusNoContent {
		t.Fatalf("сводка не принята: %d", ok.Code)
	}
	bad := httptest.NewRecorder()
	handleMetrics(bad, httptest.NewRequest(http.MethodPost, "/api/metrics", strings.NewReader(`не json`)))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("мусор принят: %d", bad.Code)
	}
}

// Ставки: на большой карте порог не больше 25 км, промах в 30 км уже штраф.
func TestBetsCap(t *testing.T) {
	s := Settings{MaxDist: 4000000}
	if s.betSafe() != betSafeMax {
		t.Fatalf("порог на карте Европы: %v, ждали %v", s.betSafe(), float64(betSafeMax))
	}
	city := Settings{MaxDist: 30000}
	if city.betSafe() != 1500 {
		t.Fatalf("порог на карте города: %v, ждали 1500", city.betSafe())
	}
	tr, a, b := pair(t)
	set := Settings{Region: "g_europe", Provider: "google", Mode: "bets", Rounds: 1, MaxDist: 4000000}
	a.WriteJSON(map[string]any{"t": "start", "settings": set, "locs": []Loc{paris}})
	tr.read(b, isState("round"))
	// около 30 км к северу от Парижа
	a.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": paris.Lat + 0.27, "lon": paris.Lon, "bet": 2})
	b.WriteJSON(map[string]any{"t": "guess", "round": 1, "lat": paris.Lat + 0.1, "lon": paris.Lon, "bet": 2})
	st := tr.read(a, isState("reveal"))
	if sc := playerByName(st, "alice")["score"].(float64); sc != -betPenalty {
		t.Fatalf("x2 с промахом 30 км: %v, ждали %d", sc, -betPenalty)
	}
	if sc := playerByName(st, "bob")["score"].(float64); sc <= 5000 {
		t.Fatalf("x2 с промахом 11 км должна сыграть: %v", sc)
	}
}
