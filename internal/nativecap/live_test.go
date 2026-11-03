package nativecap

// Живые проверки на настоящей видеокарте: ZENO_CAP_TEST=1 go test -run Live -v ./internal/nativecap

import (
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
	"unsafe"
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
	c, err := newCapture(d, 0, monitorAt(0, 0), false, false)
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

// readback копирует текстуру 8 бит (BGRA или RGBA) в память и отдает картинку
// с шагом 2 пикселя (для сравнения глазами).
func readback(t *testing.T, d *device, tex com, format, w, h uint32, bgra bool) *image.RGBA {
	desc := texDesc{Width: w, Height: h, MipLevels: 1, ArraySize: 1, Format: format, SampleCount: 1, Usage: 3, CPUAccessFlags: 0x20000}
	var st unsafe.Pointer
	if err := hr(d.dev.call(5, uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&st)))); err != nil {
		t.Fatal(err)
	}
	stage := com{st}
	defer stage.release()
	d.copyRegion(stage, tex, w, h)
	var m struct {
		Data            unsafe.Pointer
		RowPitch, Depth uint32
	}
	if err := hr(d.ctx.call(14, stage.ptr(), 0, 1, 0, uintptr(unsafe.Pointer(&m)))); err != nil { // Map READ
		t.Fatal(err)
	}
	defer d.ctx.call(15, stage.ptr(), 0)
	img := image.NewRGBA(image.Rect(0, 0, int(w/2), int(h/2)))
	px := unsafe.Slice((*byte)(m.Data), int(m.RowPitch)*int(h))
	for y := 0; y < int(h/2); y++ {
		for x := 0; x < int(w/2); x++ {
			o := y*2*int(m.RowPitch) + x*2*4
			r, g, b := px[o], px[o+1], px[o+2]
			if bgra {
				r, b = b, r
			}
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = r, g, b, 255
		}
	}
	return img
}

// TestLiveHDRShot: один кадр монитора обычным путем (BGRA) и путем HDR,
// PNG в папку из ZENO_CAP_OUT.
func TestLiveHDRShot(t *testing.T) {
	out := os.Getenv("ZENO_CAP_OUT")
	if os.Getenv("ZENO_CAP_TEST") == "" || out == "" {
		t.Skip("ZENO_CAP_TEST / ZENO_CAP_OUT не заданы")
	}
	hi, err := hdrInfo(monitorAt(0, 0))
	t.Logf("монитор: %+v %v", hi, err)
	for _, hdr := range []bool{false, true} {
		d, err := newDevice()
		if err != nil {
			t.Fatal(err)
		}
		var tm *toneMapper
		if hdr {
			if tm, err = newToneMapper(d, hi.WhiteNit); err != nil {
				t.Fatal(err)
			}
		}
		c, err := newCapture(d, 0, monitorAt(0, 0), false, hdr)
		if err != nil {
			t.Fatal(err)
		}
		conv, err := newConverter(d, 1280, 720, 60, tm)
		if err != nil {
			t.Fatal(err)
		}
		got := false
		for i := 0; i < 400 && !got; i++ {
			_, _ = c.grab(func(tex com, w, h, tw, th uint32, _ int64) {
				if err := conv.put(tex, w, h, tw, th); err != nil {
					t.Fatal(err)
				}
				got = true
			})
			time.Sleep(5 * time.Millisecond)
		}
		if !got {
			t.Fatal("кадр не пришел")
		}
		fmtIn := uint32(fmtBGRA)
		if hdr {
			fmtIn = fmtRGBA8
		}
		img := readback(t, d, conv.inTex, fmtIn, conv.srcW, conv.srcH, !hdr)
		name := filepath.Join(out, map[bool]string{false: "sdr-path.png", true: "hdr-path.png"}[hdr])
		f, _ := os.Create(name)
		_ = png.Encode(f, img)
		f.Close()
		var sum float64
		for i := 0; i < len(img.Pix); i += 4 {
			sum += float64(img.Pix[i]) + float64(img.Pix[i+1]) + float64(img.Pix[i+2])
		}
		t.Logf("%s: средняя яркость %.1f из 255", name, sum/float64(len(img.Pix)/4)/3)
		conv.close()
		c.close()
		if tm != nil {
			tm.close()
		}
		d.close()
	}
}
