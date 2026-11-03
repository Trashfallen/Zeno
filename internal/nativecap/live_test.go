package nativecap

// Живые проверки на настоящей видеокарте: ZENO_CAP_TEST=1 go test -run Live -v ./internal/nativecap

import (
	"math"
	"os"
	"sort"
	"testing"
	"time"
)

func intervalStats(t *testing.T, name string, ts []int64) {
	if len(ts) < 3 {
		t.Logf("%s: мало кадров (%d)", name, len(ts))
		return
	}
	var d []float64
	for i := 1; i < len(ts); i++ {
		d = append(d, float64(ts[i]-ts[i-1])/1e4) // 100 нс -> мс
	}
	sort.Float64s(d)
	var sum, sq float64
	for _, x := range d {
		sum += x
	}
	mean := sum / float64(len(d))
	for _, x := range d {
		sq += (x - mean) * (x - mean)
	}
	over := 0
	for _, x := range d {
		if x > 25 {
			over++
		}
	}
	t.Logf("%s: кадров %d, средний интервал %.1f мс, разброс %.1f мс, медиана %.1f, 95%% %.1f, макс %.1f, >25 мс: %d",
		name, len(ts), mean, math.Sqrt(sq/float64(len(d))), d[len(d)/2], d[len(d)*95/100], d[len(d)-1], over)
}

// TestLiveCaptureCadence: как ровно Windows Graphics Capture отдает кадры
// монитора и как ровно мы отдаем их кодеру.
func TestLiveCaptureCadence(t *testing.T) {
	if os.Getenv("ZENO_CAP_TEST") == "" {
		t.Skip("ZENO_CAP_TEST не задан")
	}
	d, err := newDevice()
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	c, err := newCapture(d, 0, monitorAt(0, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	var capTS, arrive []int64
	start := time.Now()
	for time.Since(start) < 6*time.Second {
		_, err := c.grab(func(_ com, _, _, _, _ uint32, ts int64) {
			capTS = append(capTS, ts)
			arrive = append(arrive, int64(time.Since(start)/100))
		})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond / 2)
	}
	intervalStats(t, "метки захвата", capTS)
	intervalStats(t, "приход к нам", arrive)
}
