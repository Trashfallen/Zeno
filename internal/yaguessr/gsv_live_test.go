package yaguessr

import (
	"os"
	"testing"
	"time"
)

// Живой поиск по плиткам Google: go test -run GsvLive с YG_LIVE=1.
func TestGsvLive(t *testing.T) {
	if os.Getenv("YG_LIVE") == "" {
		t.Skip("сеть: запускать с YG_LIVE=1")
	}
	start := time.Now()
	locs, err := gsvFind([]mapZone{{C: [2]float64{48.8566, 2.3522}, R: 8, W: 1}}, 3)
	if err != nil || len(locs) != 3 {
		t.Fatalf("Париж: %v %v", locs, err)
	}
	for _, l := range locs {
		if haversine(l.Lat, l.Lon, 48.8566, 2.3522) > 9000 {
			t.Fatalf("точка вне зоны: %+v", l)
		}
		t.Logf("https://www.google.com/maps?layer=c&cbll=%.6f,%.6f", l.Lat, l.Lon)
	}
	t.Logf("Париж: %v", time.Since(start))

	start = time.Now()
	locs, _ = gsvFind([]mapZone{{C: [2]float64{30, -40}, R: 50, W: 1}}, 1)
	if len(locs) != 0 {
		t.Fatalf("в океане нашлись панорамы: %v", locs)
	}
	t.Logf("океан: %v", time.Since(start))
}
