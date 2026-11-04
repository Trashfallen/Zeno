package yaguessr

// Запас мест и счетчик расхода API Панорам.
// Бесплатный тариф Яндекса дает 100 поисков панорам в сутки, поэтому все
// найденные хостом точки (и лишние тоже) складываются в data/pool.json,
// а новые игры сначала берут несыгранные места оттуда.
// Вызовы считают сами клиенты, сервер только суммирует их по дням (МСК).
// Все функции вызываются под mu.

import (
	"math/rand/v2"
	"time"
)

const (
	poolMaxPerRegion = 1000
	poolMaxRegions   = 200
	poolMinDist      = 300 // ближе этого считаем одним местом, м
)

type poolLoc struct {
	Lat    float64   `json:"lat"`
	Lon    float64   `json:"lon"`
	Added  time.Time `json:"added"`
	Played time.Time `json:"played,omitzero"`
}

type poolFile struct {
	Regions map[string][]*poolLoc `json:"regions"`
	Usage   map[string]int        `json:"usage"` // дата МСК -> поисков панорам
}

var pool = poolFile{Regions: map[string][]*poolLoc{}, Usage: map[string]int{}}

var msk = time.FixedZone("MSK", 3*3600)

func loadPool() {
	readJSON("pool.json", &pool)
	if pool.Regions == nil {
		pool.Regions = map[string][]*poolLoc{}
	}
	if pool.Usage == nil {
		pool.Usage = map[string]int{}
	}
}

func savePool() {
	// старую статистику держим 60 дней
	cutoff := time.Now().In(msk).AddDate(0, 0, -60).Format("2006-01-02")
	for d := range pool.Usage {
		if d < cutoff {
			delete(pool.Usage, d)
		}
	}
	writeJSON("pool.json", pool)
}

func validRegion(region string) bool {
	if region == "" || len(region) > 32 {
		return false
	}
	_, ok := pool.Regions[region]
	return ok || len(pool.Regions) < poolMaxRegions
}

func poolFind(region string, lat, lon float64) *poolLoc {
	for _, p := range pool.Regions[region] {
		if haversine(p.Lat, p.Lon, lat, lon) < poolMinDist {
			return p
		}
	}
	return nil
}

// poolTake отдает до n несыгранных мест в случайном порядке.
func poolTake(region string, n int) []Loc {
	var free []*poolLoc
	for _, p := range pool.Regions[region] {
		if p.Played.IsZero() {
			free = append(free, p)
		}
	}
	rand.Shuffle(len(free), func(i, j int) { free[i], free[j] = free[j], free[i] })
	res := []Loc{}
	for _, p := range free {
		if len(res) >= n {
			break
		}
		res = append(res, Loc{Lat: p.Lat, Lon: p.Lon, Dir: float64(rand.IntN(360))})
	}
	return res
}

func poolAdd(region string, locs []Loc) {
	if !validRegion(region) {
		return
	}
	changed := false
	for _, l := range locs {
		if !validLoc(l) || len(pool.Regions[region]) >= poolMaxPerRegion || poolFind(region, l.Lat, l.Lon) != nil {
			continue
		}
		pool.Regions[region] = append(pool.Regions[region], &poolLoc{Lat: l.Lat, Lon: l.Lon, Added: time.Now()})
		changed = true
	}
	if changed {
		savePool()
	}
}

// poolMarkPlayed помечает места игры сыгранными, чтобы они не повторялись.
func poolMarkPlayed(region string, locs []Loc) {
	if !validRegion(region) {
		return
	}
	now := time.Now()
	for _, l := range locs {
		p := poolFind(region, l.Lat, l.Lon)
		if p == nil {
			p = &poolLoc{Lat: l.Lat, Lon: l.Lon, Added: now}
			pool.Regions[region] = append(pool.Regions[region], p)
		}
		p.Played = now
	}
	savePool()
}

func poolFree(region string) int {
	n := 0
	for _, p := range pool.Regions[region] {
		if p.Played.IsZero() {
			n++
		}
	}
	return n
}

func usageToday() int {
	return pool.Usage[time.Now().In(msk).Format("2006-01-02")]
}

func usageAdd(n int) {
	pool.Usage[time.Now().In(msk).Format("2006-01-02")] += n
	savePool()
}
