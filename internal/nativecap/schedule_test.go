package nativecap

import "testing"

// Кадры монитора 164 Гц (сетка ~6,1 мс), игра рисует каждый кадр, на 1-й
// секунде подвисает на 120 мс. Расписание 60 к/с: в каждой секунде не больше
// 61 кадра, после подвисания нет пачки коротких интервалов.
func TestScheduleNoCatchUp(t *testing.T) {
	const grid = 1e7 / 164 // 100 нс
	sch := schedule{fd: int64(1e7) / 60}
	var sent []int64
	for i := 0; i < 164*3; i++ {
		ts := int64(float64(i) * grid)
		if ts > 1e7 && ts < 1e7+1.2e6 {
			continue // игра подвисла - кадров нет
		}
		if len(sent) == 0 || sch.due(ts) {
			sch.sent(ts)
			sent = append(sent, ts)
		}
	}
	per := map[int64]int{}
	short := 0
	for i, ts := range sent {
		per[ts/1e7]++
		if i > 0 && ts-sent[i-1] < 1e5 { // короче 10 мс
			short++
		}
	}
	t.Logf("кадров по секундам: %v, коротких интервалов: %d", per, short)
	for sec, n := range per {
		if n > 61 {
			t.Errorf("секунда %d: %d кадров, больше 61", sec, n)
		}
	}
	if per[0] < 58 || per[2] < 58 {
		t.Errorf("без подвисаний должно быть около 60 к/с, вышло %v", per)
	}
	if short > 0 {
		t.Errorf("после подвисания пачка: %d интервалов короче 10 мс", short)
	}
}
