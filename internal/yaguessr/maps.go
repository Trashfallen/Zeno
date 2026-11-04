package yaguessr

// Свои карты: игроки рисуют на карте мира круги, внутри которых
// выбираются точки для раундов. Карты общие для всех аккаунтов,
// хранятся в data/maps.json.

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxMaps     = 100
	maxMapZones = 3000
)

type mapZone struct {
	C [2]float64 `json:"c"` // [lat, lon]
	R float64    `json:"r"` // радиус, км
	W float64    `json:"w"` // вес выбора зоны
}

type customMap struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Author  string    `json:"author"` // логин
	Zones   []mapZone `json:"zones"`
	Updated time.Time `json:"updated"`
}

var (
	mapsMu sync.Mutex
	maps   = map[string]*customMap{}
)

func loadMaps() {
	var list []*customMap
	readJSON("maps.json", &list)
	for _, m := range list {
		maps[m.ID] = m
	}
}

// saveMaps вызывается под mapsMu.
func saveMaps() {
	list := make([]*customMap, 0, len(maps))
	for _, m := range maps {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Updated.After(list[j].Updated) })
	writeJSON("maps.json", list)
}

type mapView struct {
	*customMap
	AuthorNick string `json:"authorNick"`
}

func handleMaps(w http.ResponseWriter, r *http.Request) {
	login := authLogin(r)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	switch r.Method {
	case http.MethodGet:
		mapsMu.Lock()
		list := make([]mapView, 0, len(maps))
		for _, m := range maps {
			list = append(list, mapView{customMap: m})
		}
		mapsMu.Unlock()
		for i := range list {
			list[i].AuthorNick = nickFor(list[i].Author)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Updated.After(list[j].Updated) })
		json.NewEncoder(w).Encode(list)

	case http.MethodPost: // создать или обновить
		var m customMap
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&m) != nil {
			http.Error(w, "плохой запрос", http.StatusBadRequest)
			return
		}
		m.Name = strings.TrimSpace(m.Name)
		if utf8.RuneCountInString(m.Name) > 40 {
			m.Name = string([]rune(m.Name)[:40])
		}
		if m.Name == "" {
			http.Error(w, "Дай карте название", http.StatusBadRequest)
			return
		}
		if len(m.Zones) == 0 || len(m.Zones) > maxMapZones {
			http.Error(w, "Нарисуй на карте хотя бы одну область", http.StatusBadRequest)
			return
		}
		for i, z := range m.Zones {
			if !validLoc(Loc{Lat: z.C[0], Lon: z.C[1]}) || !(z.R >= 0.05 && z.R <= 3000) {
				http.Error(w, "неверная область", http.StatusBadRequest)
				return
			}
			if !(z.W > 0) || math.IsInf(z.W, 0) {
				m.Zones[i].W = z.R * z.R
			}
		}

		mapsMu.Lock()
		defer mapsMu.Unlock()
		if old := maps[m.ID]; m.ID != "" && old != nil {
			old.Name, old.Zones, old.Updated = m.Name, m.Zones, time.Now()
			saveMaps()
			json.NewEncoder(w).Encode(old)
			return
		}
		if len(maps) >= maxMaps {
			http.Error(w, "Слишком много карт, удали лишние", http.StatusBadRequest)
			return
		}
		m.ID, m.Author, m.Updated = randToken(4), login, time.Now()
		maps[m.ID] = &m
		saveMaps()
		json.NewEncoder(w).Encode(&m)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		mapsMu.Lock()
		delete(maps, id)
		saveMaps()
		mapsMu.Unlock()
		w.Write([]byte("{}"))

	default:
		http.Error(w, "метод", http.StatusMethodNotAllowed)
	}
}
