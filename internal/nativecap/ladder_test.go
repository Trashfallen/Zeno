package nativecap

import "testing"

// Ступени разрешения не опускаются ниже 720p (рамка 1280x720 с пропорциями источника).
func TestBelow720(t *testing.T) {
	cases := []struct {
		w, h int
		k    float64
		want bool
	}{
		{1920, 1080, 0.7, false}, // 1344x756
		{1920, 1080, 0.6, true},  // 1152x648
		{2560, 1440, 0.5, false}, // 1280x720 ровно
		{2560, 1072, 0.5, false}, // широкий: 1280x536 - это и есть 720p по ширине
		{1920, 804, 0.6, true},   // 1152x482
		{1280, 720, 0.9, true},   // уже 720p - ниже нельзя
	}
	for _, c := range cases {
		if got := below720(c.w, c.h, c.k); got != c.want {
			t.Errorf("below720(%d, %d, %.1f) = %v, нужно %v", c.w, c.h, c.k, got, c.want)
		}
	}
}
