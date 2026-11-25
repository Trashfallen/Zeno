package nativecap

import "testing"

// runLadder гоняет ladderStep по секундам: kbps[i] - битрейт кодера в секунду i.
// Возвращает ступень после каждой секунды.
func runLadder(rungs []rung, kbps []float64) []int {
	step, low, high := 0, 0, 0
	var hist []float64
	out := make([]int, 0, len(kbps))
	for _, k := range kbps {
		hist = append(hist, k)
		if len(hist) > ladderAvg {
			hist = hist[1:]
		}
		avg := 0.0
		for _, v := range hist {
			avg += v
		}
		avg /= float64(len(hist))
		step = ladderStep(rungs, step, 2560, 1440, 60, avg, k, &low, &high)
		out = append(out, step)
	}
	return out
}

func series(parts ...[2]float64) []float64 { // {секунд, кбит/с}
	var out []float64
	for _, p := range parts {
		for i := 0; i < int(p[0]); i++ {
			out = append(out, p[1])
		}
	}
	return out
}

// Провал канала на 3 с (16 -> 3 Мбит/с) не трогает разрешение.
func TestLadderShortDipKeepsStep(t *testing.T) {
	steps := runLadder(motionRungs, series([2]float64{10, 16000}, [2]float64{3, 3000}, [2]float64{10, 16000}))
	for i, s := range steps {
		if s != 0 {
			t.Fatalf("секунда %d: ступень %d, ждали 0", i, s)
		}
	}
}

// Узкий канал надолго (3 Мбит/с на 1440p60) - ступени вниз, но не ниже 720p.
func TestLadderLongNarrowStepsDown(t *testing.T) {
	steps := runLadder(motionRungs, series([2]float64{5, 16000}, [2]float64{60, 3000}))
	last := steps[len(steps)-1]
	if last == 0 {
		t.Fatal("за минуту узкого канала ступень не сменилась")
	}
	if motionRungs[last].k < 0.5 {
		t.Fatalf("ступень %d ниже 720p", last)
	}
	first := -1
	for i, s := range steps {
		if s > 0 {
			first = i
			break
		}
	}
	if first < 5+downSecs {
		t.Fatalf("первая ступень вниз через %d с после провала, ждали не раньше %d", first-5, downSecs)
	}
}

// «Четкость» при узком канале сначала снижает частоту, размер держит.
func TestLadderDetailDropsFpsFirst(t *testing.T) {
	steps := runLadder(detailRungs, series([2]float64{5, 16000}, [2]float64{20, 4000}))
	if steps[len(steps)-1] == 0 {
		t.Fatal("ступень не сменилась")
	}
	if detailRungs[1].k != 1 || detailRungs[1].fps(60) != 48 || detailRungs[2].fps(60) != 40 {
		t.Fatalf("первые ступени «Четкости»: %+v", detailRungs[:3])
	}
}

// Канал вернулся - наверх за upSecs на каждую ступень, без средней.
func TestLadderRecoversFast(t *testing.T) {
	steps := runLadder(motionRungs, series([2]float64{5, 16000}, [2]float64{40, 3000}, [2]float64{30, 16000}))
	low := steps[44]
	if low == 0 {
		t.Fatal("не опустились")
	}
	for i := 45; i < len(steps); i++ {
		if steps[i] == 0 {
			if took := i - 44; took > upSecs*low+1 {
				t.Fatalf("наверх за %d с с %d ступеней", took, low)
			}
			return
		}
	}
	t.Fatalf("не вернулись на полное разрешение, ступень %d", steps[len(steps)-1])
}
