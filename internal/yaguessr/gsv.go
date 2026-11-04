package yaguessr

// Поиск точек с панорамами Google без ключа.
// Слой покрытия Street View (синие линии) отдается обычными PNG-плитками:
// пустая плитка = панорам нет. Берем случайную точку в зоне, на плитке z13
// выбираем случайный закрашенный пиксель, затем на плитке z17 уточняем его
// до середины линии, чтобы встроенная панорама нашлась точно на дороге.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

const (
	gsvCoarseZoom = 13
	gsvFineZoom   = 17
	gsvTileSize   = 256
)

var gsvClient = &http.Client{Timeout: 10 * time.Second}

func gsvTileURL(x, y, z int) string {
	return fmt.Sprintf("https://mts1.googleapis.com/vt?hl=en-US&lyrs=svv|cb_client:apiv3&style=40,18&x=%d&y=%d&z=%d", x, y, z)
}

// Web Mercator: широта/долгота <-> мировые пиксели на уровне z.
func worldPixel(lat, lon float64, z int) (float64, float64) {
	n := float64(gsvTileSize) * math.Exp2(float64(z))
	x := (lon + 180) / 360 * n
	s := math.Sin(lat * math.Pi / 180)
	y := (0.5 - math.Log((1+s)/(1-s))/(4*math.Pi)) * n
	return x, y
}

func pixelLatLon(px, py float64, z int) (float64, float64) {
	n := float64(gsvTileSize) * math.Exp2(float64(z))
	lon := px/n*360 - 180
	lat := math.Atan(math.Sinh(math.Pi*(1-2*py/n))) * 180 / math.Pi
	return lat, lon
}

// gsvTile возвращает плитку покрытия или nil, если панорам на ней нет.
func gsvTile(x, y, z int) (image.Image, error) {
	req, _ := http.NewRequest(http.MethodGet, gsvTileURL(x, y, z), nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (YaGuessr)")
	res, err := gsvClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("плитка покрытия: HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if len(b) < 200 { // пустая прозрачная плитка
		return nil, nil
	}
	return png.Decode(bytes.NewReader(b))
}

func opaque(img image.Image, x, y int) bool {
	_, _, _, a := img.At(x, y).RGBA()
	return a > 0x4000
}

func randomZone(zones []mapZone) mapZone {
	total := 0.0
	for _, z := range zones {
		total += z.W
	}
	x := rand.Float64() * total
	for _, z := range zones {
		x -= z.W
		if x <= 0 {
			return z
		}
	}
	return zones[len(zones)-1]
}

func randomInZone(z mapZone) (float64, float64) {
	d := z.R * math.Sqrt(rand.Float64())
	a := rand.Float64() * 2 * math.Pi
	lat := z.C[0] + d*math.Cos(a)/111.32
	lon := z.C[1] + d*math.Sin(a)/(111.32*math.Cos(z.C[0]*math.Pi/180))
	return lat, lon
}

// gsvTry делает одну попытку найти точку с панорамой в зоне.
func gsvTry(z mapZone) (Loc, bool, error) {
	lat, lon := randomInZone(z)
	px, py := worldPixel(lat, lon, gsvCoarseZoom)
	tx, ty := int(px)/gsvTileSize, int(py)/gsvTileSize
	img, err := gsvTile(tx, ty, gsvCoarseZoom)
	if err != nil || img == nil {
		return Loc{}, false, err
	}
	// закрашенные пиксели плитки, которые лежат внутри зоны
	var cand [][2]int
	for y := 0; y < gsvTileSize; y += 2 {
		for x := 0; x < gsvTileSize; x += 2 {
			if !opaque(img, x, y) {
				continue
			}
			plat, plon := pixelLatLon(float64(tx*gsvTileSize+x), float64(ty*gsvTileSize+y), gsvCoarseZoom)
			if haversine(plat, plon, z.C[0], z.C[1]) <= z.R*1000 {
				cand = append(cand, [2]int{x, y})
			}
		}
	}
	if len(cand) == 0 {
		return Loc{}, false, nil
	}
	c := cand[rand.IntN(len(cand))]
	lat, lon = pixelLatLon(float64(tx*gsvTileSize+c[0])+0.5, float64(ty*gsvTileSize+c[1])+0.5, gsvCoarseZoom)
	if la, lo, ok := gsvRefine(lat, lon); ok {
		lat, lon = la, lo
	}
	return Loc{Lat: lat, Lon: lon, Dir: float64(rand.IntN(360))}, true, nil
}

// nearestOpaque - ближайший к пикселю (cx, cy) закрашенный пиксель плитки.
func nearestOpaque(img image.Image, cx, cy int) (int, int, bool) {
	best, bx, by := math.MaxFloat64, -1, -1
	for y := 0; y < gsvTileSize; y++ {
		for x := 0; x < gsvTileSize; x++ {
			if !opaque(img, x, y) {
				continue
			}
			if d := math.Hypot(float64(x-cx), float64(y-cy)); d < best {
				best, bx, by = d, x, y
			}
		}
	}
	return bx, by, bx >= 0
}

// gsvRefine подтягивает точку к ближайшей линии покрытия на плитке z17 (середина дороги).
func gsvRefine(lat, lon float64) (float64, float64, bool) {
	fx, fy := worldPixel(lat, lon, gsvFineZoom)
	ftx, fty := int(fx)/gsvTileSize, int(fy)/gsvTileSize
	fine, err := gsvTile(ftx, fty, gsvFineZoom)
	if err != nil || fine == nil {
		return 0, 0, false
	}
	bx, by, ok := nearestOpaque(fine, int(fx)-ftx*gsvTileSize, int(fy)-fty*gsvTileSize)
	if !ok {
		return 0, 0, false
	}
	lat, lon = pixelLatLon(float64(ftx*gsvTileSize+bx)+0.5, float64(fty*gsvTileSize+by)+0.5, gsvFineZoom)
	return lat, lon, true
}

var errNoPano = errors.New("Рядом нет панорам Google. Поставь точку ближе к дорогам")

// gsvNearest ищет панораму у точки, которую загадал игрок: сначала на ее плитке z17
// (около 300 м), потом ближайший участок покрытия на плитке z13 (около 5 км).
// Переменная, чтобы в тестах не ходить в Google.
var gsvNearest = func(lat, lon float64) (Loc, error) {
	if la, lo, ok := gsvRefine(lat, lon); ok {
		return Loc{Lat: la, Lon: lo, Dir: float64(rand.IntN(360))}, nil
	}
	px, py := worldPixel(lat, lon, gsvCoarseZoom)
	tx, ty := int(px)/gsvTileSize, int(py)/gsvTileSize
	img, err := gsvTile(tx, ty, gsvCoarseZoom)
	if err != nil {
		return Loc{}, err
	}
	if img == nil {
		return Loc{}, errNoPano
	}
	bx, by, ok := nearestOpaque(img, int(px)-tx*gsvTileSize, int(py)-ty*gsvTileSize)
	if !ok {
		return Loc{}, errNoPano
	}
	lat, lon = pixelLatLon(float64(tx*gsvTileSize+bx)+0.5, float64(ty*gsvTileSize+by)+0.5, gsvCoarseZoom)
	if la, lo, ok := gsvRefine(lat, lon); ok {
		lat, lon = la, lo
	}
	return Loc{Lat: lat, Lon: lon, Dir: float64(rand.IntN(360))}, nil
}

// gsvNear ищет вторую точку обзора в 100-300 м от первой (режим «Два взгляда»):
// плитки z16 (около 600 м) вокруг точки, случайный закрашенный пиксель на нужном расстоянии.
func gsvNear(lat, lon float64) (Loc, error) {
	const z = 16
	px, py := worldPixel(lat, lon, z)
	tx, ty := int(px)/gsvTileSize, int(py)/gsvTileSize
	var cand [][2]float64
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			img, err := gsvTile(tx+dx, ty+dy, z)
			if err != nil {
				return Loc{}, err
			}
			if img == nil {
				continue
			}
			for y := 0; y < gsvTileSize; y += 2 {
				for x := 0; x < gsvTileSize; x += 2 {
					if !opaque(img, x, y) {
						continue
					}
					la, lo := pixelLatLon(float64((tx+dx)*gsvTileSize+x)+0.5, float64((ty+dy)*gsvTileSize+y)+0.5, z)
					if d := haversine(lat, lon, la, lo); d >= 100 && d <= 300 {
						cand = append(cand, [2]float64{la, lo})
					}
				}
			}
		}
	}
	if len(cand) == 0 {
		return Loc{}, errNoPano
	}
	c := cand[rand.IntN(len(cand))]
	la, lo := c[0], c[1]
	if a, b, ok := gsvRefine(la, lo); ok {
		la, lo = a, b
	}
	return Loc{Lat: la, Lon: lo, Dir: float64(rand.IntN(360))}, nil
}

// gsvFind ищет count точек, разнесенных хотя бы на poolMinDist метров.
func gsvFind(zones []mapZone, count int) ([]Loc, error) {
	var (
		lk       sync.Mutex
		res      []Loc
		attempts = count * 30
		lastErr  error
		wg       sync.WaitGroup
	)
	jobs := make(chan struct{})
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				loc, ok, err := gsvTry(randomZone(zones))
				lk.Lock()
				if err != nil {
					lastErr = err
				}
				if ok && len(res) < count {
					far := true
					for _, l := range res {
						if haversine(l.Lat, l.Lon, loc.Lat, loc.Lon) < poolMinDist {
							far = false
						}
					}
					if far {
						res = append(res, loc)
					}
				}
				lk.Unlock()
			}
		}()
	}
	deadline := time.Now().Add(40 * time.Second)
	for i := 0; i < attempts && time.Now().Before(deadline); i++ {
		lk.Lock()
		done := len(res) >= count
		lk.Unlock()
		if done {
			break
		}
		jobs <- struct{}{}
	}
	close(jobs)
	wg.Wait()
	if len(res) < count && lastErr != nil && len(res) == 0 {
		return nil, lastErr
	}
	return res, nil
}

// POST /api/gsv/find {zones, count}: точки с панорамами Google для раундов.
func handleGsvFind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Zones []mapZone `json:"zones"`
		Count int       `json:"count"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req) != nil ||
		len(req.Zones) == 0 || len(req.Zones) > maxMapZones || req.Count < 1 || req.Count > locBatch {
		http.Error(w, "плохой запрос", http.StatusBadRequest)
		return
	}
	for i, z := range req.Zones {
		if !validLoc(Loc{Lat: z.C[0], Lon: z.C[1]}) || !(z.R > 0 && z.R <= 3000) {
			http.Error(w, "неверная область", http.StatusBadRequest)
			return
		}
		if !(z.W > 0) {
			req.Zones[i].W = z.R * z.R
		}
	}
	locs, err := gsvFind(req.Zones, req.Count)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": "Google не отдал покрытие: " + err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"locs": locs})
}

// POST /api/gsv/near {lat, lon}: вторая точка обзора рядом для режима «Два взгляда».
func handleGsvNear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var req Loc
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req) != nil || !validLoc(req) {
		http.Error(w, "плохой запрос", http.StatusBadRequest)
		return
	}
	loc, err := gsvNear(req.Lat, req.Lon)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"loc": loc})
}
