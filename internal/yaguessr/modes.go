package yaguessr

// Режимы поверх классики: канат, кто быстрее, ставки, общая экспедиция,
// штурман и разведчик, два взгляда, загадай место. Захват стран сервер считает
// как классику, страны по ответам раскрашивает клиент.

import (
	"math"
	"time"
)

const (
	tugWin     = 10000 // канат: столько надо перетянуть на свою сторону
	speedBonus = 1.2   // кто быстрее: множитель первому ответившему
	betPenalty = 1000  // ставки: штраф за каждую ступень ставки сверх x1 при промахе
	betSafeMax = 25000 // ставки: дальше этого (м) ставка не играет ни на какой карте
)

var gameModes = map[string]bool{
	"classic": true, "duel": true, "tug": true, "speed": true, "bets": true,
	"coop": true, "scout": true, "twoviews": true, "custom": true, "conquer": true,
}

func (s Settings) tug() bool { return s.Mode == "tug" }

// fixedPlayers: играют те, кто был на старте, новые входят зрителями.
func (s Settings) fixedPlayers() bool {
	return s.Mode == "duel" || s.Mode == "tug" || s.Mode == "scout"
}

// team: у игроков общий счет команды.
func (s Settings) team() bool { return s.Mode == "coop" || s.Mode == "scout" }

// ownTargets: у каждого игрока в раунде свое место.
func (s Settings) ownTargets() bool { return s.Mode == "twoviews" || s.Mode == "custom" }

// coopPerRound - цель команды за раунд по уровню сложности.
func (s Settings) coopPerRound() int {
	switch s.CoopLevel {
	case "tourist":
		return 2500
	case "cartographer":
		return 4500
	}
	return 3500
}

// betSafe - ставка x2/x3 играет, если промах не дальше этого: 5% размаха карты,
// но не больше 25 км, иначе на картах стран и мира ставка играет слишком легко.
func (s Settings) betSafe() float64 { return math.Min(s.MaxDist*0.05, betSafeMax) }

func (p *Player) roundScore(i int) int {
	if len(p.Rounds) > i && p.Rounds[i] != nil {
		return p.Rounds[i].Score
	}
	return 0
}

// active - игроки, которые участвуют (не выбыли и не зрители).
func (r *Room) active() []*Player {
	var res []*Player
	for _, p := range r.Players {
		if !p.Out {
			res = append(res, p)
		}
	}
	return res
}

// target - место, которое угадывает игрок p в раунде i.
func (r *Room) target(p *Player, i int) Loc {
	if i < len(r.Targets) && r.Targets[i] != nil {
		if l, ok := r.Targets[i][p.ID]; ok {
			return l
		}
	}
	l := r.Locs[i]
	l.Alt = nil
	return l
}

// peekTarget - место игрока p в будущем раунде i, чтобы браузер заранее загрузил панораму.
// Нет, если место еще не выбрано (загадай место) или p в том раунде штурман.
func (r *Room) peekTarget(p *Player, i int) (Loc, bool) {
	if i >= len(r.Locs) || r.Settings.Mode == "custom" {
		return Loc{}, false
	}
	l := r.Locs[i]
	switch r.Settings.Mode {
	case "scout":
		if act := r.active(); len(act) > 0 && act[i%len(act)].ID != p.ID && !p.Out {
			return Loc{}, false
		}
	case "twoviews":
		for k, pl := range r.Players {
			if pl == p && k%2 == 1 && l.Alt != nil {
				l = *l.Alt
			}
		}
	}
	l.Alt = nil
	return l, true
}

// prepareRound раздает роли и места текущего раунда.
func (r *Room) prepareRound() {
	i := r.Round - 1
	for len(r.Targets) <= i {
		r.Targets = append(r.Targets, nil)
		r.From = append(r.From, nil)
		r.Scouts = append(r.Scouts, "")
	}
	r.RoundStart = time.Now()
	switch r.Settings.Mode {
	case "twoviews":
		// игроки через одного смотрят со второй точки рядом
		base := r.Locs[i]
		t := map[string]Loc{}
		for k, p := range r.Players {
			l := base
			if k%2 == 1 && base.Alt != nil {
				l = *base.Alt
			}
			l.Alt = nil
			t[p.ID] = l
		}
		r.Targets[i] = t
	case "custom":
		r.Targets[i], r.From[i] = r.pendTargets, r.pendFrom
	case "scout":
		// разведчик меняется каждый раунд
		if act := r.active(); len(act) > 0 {
			r.Scouts[i] = act[i%len(act)].ID
		}
	}
}

func (r *Room) scout() string {
	if r.Settings.Mode != "scout" || r.Round < 1 || r.Round > len(r.Scouts) {
		return ""
	}
	return r.Scouts[r.Round-1]
}

// canGuess: выбывшие, зрители и разведчик не отвечают.
func (r *Room) canGuess(p *Player) bool {
	return !p.Out && p.ID != r.scout()
}

// scoreGuess считает ответ по правилам режима. first - это первый ответ раунда.
func (r *Room) scoreGuess(p *Player, lat, lon float64, bet int, first bool) *Guess {
	a := r.target(p, r.Round-1)
	d := haversine(lat, lon, a.Lat, a.Lon)
	base := calcScore(d, r.Settings.MaxDist)
	g := &Guess{Lat: lat, Lon: lon, Dist: d, Score: base}
	if r.Settings.ownTargets() {
		g.Ans = &a
	}
	switch r.Settings.Mode {
	case "speed":
		// за быстрый ответ до x1, за ответ на последней секунде x0.5, первому еще x1.2
		el := time.Since(r.RoundStart).Seconds() - revealGraceSec
		tf := 1 - math.Max(0, el)/float64(r.Settings.Timer)
		f := 0.5 + 0.5*math.Max(0, math.Min(1, tf))
		if first {
			f *= speedBonus
		}
		g.Base, g.Mult = base, math.Round(f*100)/100
		g.Score = int(math.Round(float64(base) * g.Mult))
	case "bets":
		if bet < 1 || bet > 3 {
			bet = 1
		}
		g.Base, g.Bet = base, bet
		if bet > 1 {
			if d <= r.Settings.betSafe() {
				g.Score = base * bet
			} else {
				g.Score = -(bet - 1) * betPenalty
			}
		}
	}
	return g
}

// settleRound подводит раунд: урон в дуэли, канат, счет команды.
func (r *Room) settleRound() {
	switch {
	case r.Settings.duel():
		r.applyDamage()
	case r.Settings.tug():
		r.applyPull()
	}
	r.computeTeam()
}

// applyPull: канат сдвигается на разницу очков раунда (с 5-го раунда с множителем).
// Сдвиг раунда хранится в Dmg со знаком: плюс - в сторону игрока.
func (r *Room) applyPull() {
	i := r.Round - 1
	for _, p := range r.Players {
		for len(p.Dmg) <= i {
			p.Dmg = append(p.Dmg, 0)
		}
	}
	act := r.active()
	if len(act) != 2 {
		return
	}
	a, b := act[0], act[1]
	sh := int(math.Round(float64(a.roundScore(i)-b.roundScore(i)) * duelMult(r.Round)))
	a.Pull += sh
	b.Pull -= sh
	a.Dmg[i], b.Dmg[i] = sh, -sh
}

// computeTeam: счет команды за раунд - лучший ответ или, по выбору, середина между ответами.
func (r *Room) computeTeam() {
	if !r.Settings.team() {
		return
	}
	i := r.Round - 1
	for len(r.Team) <= i {
		r.Team = append(r.Team, 0)
		r.Mids = append(r.Mids, nil)
	}
	var gs []*Guess
	best := 0
	for _, p := range r.Players {
		if len(p.Rounds) > i && p.Rounds[i] != nil {
			gs = append(gs, p.Rounds[i])
			best = max(best, p.Rounds[i].Score)
		}
	}
	r.Team[i], r.Mids[i] = best, nil
	if r.Settings.Mode == "coop" && r.Settings.CoopRule == "middle" && len(gs) > 0 {
		m := midpoint(gs)
		a := r.Locs[i]
		r.Team[i] = calcScore(haversine(m.Lat, m.Lon, a.Lat, a.Lon), r.Settings.MaxDist)
		r.Mids[i] = &m
	}
}

func (r *Room) teamTotal() int {
	t := 0
	for i := 0; i < len(r.Team) && i < r.Round; i++ {
		t += r.Team[i]
	}
	return t
}

// midpoint - средняя точка ответов на сфере (через сумму векторов, без проблем с 180-м меридианом).
func midpoint(gs []*Guess) Loc {
	rad := math.Pi / 180
	var x, y, z float64
	for _, g := range gs {
		la, lo := g.Lat*rad, g.Lon*rad
		x += math.Cos(la) * math.Cos(lo)
		y += math.Cos(la) * math.Sin(lo)
		z += math.Sin(la)
	}
	return Loc{Lat: math.Atan2(z, math.Hypot(x, y)) / rad, Lon: math.Atan2(y, x) / rad}
}

// ---------- Загадай место ----------

// enterPick: игроки загадывают друг другу места на следующий раунд.
func (r *Room) enterPick() {
	r.stopTimer()
	r.Deadline = time.Time{}
	r.State = "pick"
	r.PickBy = map[string]Loc{}
	r.picking = map[string]bool{}
	r.broadcast()
}

// checkAllPicked: когда все онлайн загадали, каждый получает место следующего по кругу.
func (r *Room) checkAllPicked() {
	if r.State != "pick" {
		return
	}
	var ps []*Player
	for _, p := range r.Players {
		if p.conn == nil || p.Out {
			continue
		}
		if _, ok := r.PickBy[p.ID]; !ok {
			return
		}
		ps = append(ps, p)
	}
	if len(ps) < 2 {
		return
	}
	r.pendTargets, r.pendFrom = map[string]Loc{}, map[string]string{}
	for k, p := range ps {
		from := ps[(k+1)%len(ps)]
		r.pendTargets[p.ID] = r.PickBy[from.ID]
		r.pendFrom[p.ID] = from.ID
	}
	r.Locs = append(r.Locs[:r.Round], r.pendTargets[ps[0].ID])
	r.startRound()
}

// pick: игрок загадал точку, панораму рядом ищем без блокировки комнаты.
func (r *Room) pick(p *Player, lat, lon float64) {
	if r.picking[p.ID] {
		return
	}
	r.picking[p.ID] = true
	delete(r.PickBy, p.ID)
	r.broadcast()
	round := r.Round
	go func() {
		loc, err := gsvNearest(lat, lon)
		mu.Lock()
		defer mu.Unlock()
		delete(r.picking, p.ID)
		if r.State != "pick" || r.Round != round {
			return
		}
		if err != nil {
			if p.conn != nil {
				p.conn.sendJSON(map[string]string{"t": "pickError", "msg": err.Error()})
			}
			r.broadcast()
			return
		}
		r.PickBy[p.ID] = loc
		if p.conn != nil {
			p.conn.sendJSON(map[string]any{"t": "picked", "loc": loc, "d": haversine(lat, lon, loc.Lat, loc.Lon)})
		}
		r.broadcast()
		r.checkAllPicked()
	}()
}
