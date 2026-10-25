// Рисует значок Zeno: сине-фиолетовый квадрат со скруглением, белый пузырь
// сообщения с хвостиком и двумя «глазами». Пишет assets/icon.ico (16-256) и
// ui/icon-*.png. Запуск из корня проекта: go run ./tools/genicon
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"strconv"
)

type col struct{ r, g, b, a float64 }

func lerp(a, b col, t float64) col {
	return col{a.r + (b.r-a.r)*t, a.g + (b.g-a.g)*t, a.b + (b.b-a.b)*t, a.a + (b.a-a.a)*t}
}

// over накладывает c с покрытием k поверх dst.
func over(dst, c col, k float64) col {
	a := c.a * k
	outA := a + dst.a*(1-a)
	if outA == 0 {
		return col{}
	}
	mix := func(x, y float64) float64 { return (x*a + y*dst.a*(1-a)) / outA }
	return col{mix(c.r, dst.r), mix(c.g, dst.g), mix(c.b, dst.b), outA}
}

func roundRect(x, y, cx, cy, hw, hh, r float64) float64 {
	dx := math.Abs(x-cx) - hw + r
	dy := math.Abs(y-cy) - hh + r
	return math.Hypot(math.Max(dx, 0), math.Max(dy, 0)) + math.Min(math.Max(dx, dy), 0) - r
}

func circle(x, y, cx, cy, r float64) float64 { return math.Hypot(x-cx, y-cy) - r }

// segment - расстояние до отрезка минус толщина (капсула).
func capsule(x, y, ax, ay, bx, by, r float64) float64 {
	px, py, vx, vy := x-ax, y-ay, bx-ax, by-ay
	t := math.Max(0, math.Min(1, (px*vx+py*vy)/(vx*vx+vy*vy)))
	return math.Hypot(px-vx*t, py-vy*t) - r
}

func smin(a, b, k float64) float64 {
	h := math.Max(k-math.Abs(a-b), 0) / k
	return math.Min(a, b) - h*h*k*0.25
}

func hex(s string) col {
	v, _ := strconv.ParseUint(s, 16, 32)
	return col{float64(v>>16&255) / 255, float64(v>>8&255) / 255, float64(v&255) / 255, 1}
}

func pixel(x, y float64) col {
	var c col
	// фон
	if d := roundRect(x, y, 0.5, 0.5, 0.5, 0.5, 0.23); d <= 0 {
		t := math.Max(0, math.Min(1, (x*0.35+y*0.65)))
		bg := lerp(hex("5b8cff"), hex("4b2fe0"), t)
		c = over(c, bg, 1)
	} else {
		return c
	}
	// пузырь: почти круглый прямоугольник + хвостик снизу слева
	body := roundRect(x, y, 0.53, 0.47, 0.27, 0.235, 0.2)
	tail := capsule(x, y, 0.38, 0.58, 0.29, 0.735, 0.065)
	bubble := smin(body, tail, 0.12)
	if bubble <= 0 {
		c = over(c, col{1, 1, 1, 1}, 1)
	}
	// глаза
	eye := lerp(hex("3d5bff"), hex("4b2fe0"), y)
	if math.Min(capsule(x, y, 0.445, 0.445, 0.445, 0.495, 0.048), capsule(x, y, 0.615, 0.445, 0.615, 0.495, 0.048)) <= 0 {
		c = over(c, eye, 1)
	}
	return c
}

func render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 6
	for py := range size {
		for px := range size {
			var acc col
			for sy := range ss {
				for sx := range ss {
					x := (float64(px) + (float64(sx)+0.5)/ss) / float64(size)
					y := (float64(py) + (float64(sy)+0.5)/ss) / float64(size)
					p := pixel(x, y)
					acc.r += p.r * p.a
					acc.g += p.g * p.a
					acc.b += p.b * p.a
					acc.a += p.a
				}
			}
			n := float64(ss * ss)
			a := acc.a / n
			if a > 0 {
				img.SetNRGBA(px, py, color.NRGBA{uint8(acc.r / acc.a * 255), uint8(acc.g / acc.a * 255), uint8(acc.b / acc.a * 255), uint8(a*255 + 0.5)})
			}
		}
	}
	return img
}

func pngBytes(img image.Image) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func main() {
	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	var imgs [][]byte
	for _, s := range sizes {
		imgs = append(imgs, pngBytes(render(s)))
	}
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, []uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		w := uint8(s)
		if s >= 256 {
			w = 0
		}
		ico.Write([]byte{w, w, 0, 0})
		binary.Write(&ico, binary.LittleEndian, []uint16{1, 32})
		binary.Write(&ico, binary.LittleEndian, []uint32{uint32(len(imgs[i])), uint32(offset)})
		offset += len(imgs[i])
	}
	for _, b := range imgs {
		ico.Write(b)
	}
	must(os.WriteFile("assets/icon.ico", ico.Bytes(), 0o644))
	for _, s := range []int{32, 64, 128, 256, 512} {
		must(os.WriteFile("ui/icon-"+strconv.Itoa(s)+".png", pngBytes(render(s)), 0o644))
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
