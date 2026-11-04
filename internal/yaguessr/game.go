// ЯГессер: сервер комнат для мультиплеера (из проекта YaGuessr, встроен в Zeno).
// Раздает статику из web/ и держит комнаты в памяти, общение по WebSocket.
// Панорамы ищет браузер хоста (API Яндекса работает только на клиенте),
// сервер хранит точки раундов, считает очки и ведет таймеры.
package yaguessr

import (
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"math"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

//go:embed web
var webFS embed.FS

const (
	maxScore       = 5000
	maxRooms       = 300
	maxPlayers     = 16
	maxRounds      = 20
	duelHP         = 6000
	duelMaxRounds  = 30
	locBatch       = 5 // мест хост ищет за раз, остальные дозапрашиваются по ходу игры
	roomIdleTTL    = 10 * time.Minute
	revealGraceSec = 3 // запас к таймеру раунда на загрузку панорамы у игроков
)

type Loc struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
	Dir float64 `json:"dir"`
	Alt *Loc    `json:"alt,omitempty"` // «Два взгляда»: вторая точка обзора рядом
}

type Settings struct {
	Region      string  `json:"region"`
	Mode        string  `json:"mode"`       // classic | duel | tug | speed | bets | coop | scout | twoviews | custom | conquer
	CoopLevel   string  `json:"coopLevel"`  // общая экспедиция и разведчик: tourist | traveler | cartographer
	CoopRule    string  `json:"coopRule"`   // общая экспедиция: best (лучший ответ) | middle (середина ответов)
	Rounds      int     `json:"rounds"`     // для классики
	Timer       int     `json:"timer"`      // сек на раунд, 0 = без лимита
	GuessTimer  int     `json:"guessTimer"` // после первого ответа остальным остается столько сек, 0 = выкл
	Move        string  `json:"move"`       // move | nomove | nmpz
	HideMarkers bool    `json:"hideMarkers"`
	Layer       string  `json:"layer"`    // ground | air (воздушные панорамы, перелеты по шарам)
	Provider    string  `json:"provider"` // yandex | google
	MaxDist     float64 `json:"maxDist"`
}

// poolKey: у воздушных панорам и у Google свой запас мест, они не пересекаются с уличными Яндекса.
func (s Settings) poolKey() string {
	switch {
	case s.Provider == "google":
		return s.Region + "@g"
	case s.Layer == "air":
		return s.Region + "@air"
	}
	return s.Region
}

func (s Settings) duel() bool { return s.Mode == "duel" }

type Guess struct {
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Dist  float64 `json:"d"`
	Score int     `json:"score"`
	Base  int     `json:"base,omitempty"` // кто быстрее и ставки: очки до множителя
	Mult  float64 `json:"mult,omitempty"` // кто быстрее: множитель за время
	Bet   int     `json:"bet,omitempty"`  // ставки: x1..x3
	Ans   *Loc    `json:"ans,omitempty"`  // место, которое угадывал игрок, если у каждого свое
}

type Player struct {
	ID      string
	Login   string
	Name    string
	Score   int
	Rounds  []*Guess // ответ по каждому раунду, nil = не ответил
	HP      int      // дуэль: здоровье
	Out     bool     // выбыл в дуэли или вошел зрителем в режим с постоянным составом
	Dmg     []int    // дуэль: урон по раундам, канат: сдвиг по раундам
	Pull    int      // канат: насколько перетянут на свою сторону
	Mark    *Loc     // метка на карте без «Угадать»: засчитается, когда раунд кончится
	MarkOf  int      // раунд, в котором поставлена Mark
	MarkBet int      // ставка при метке
	conn    *Conn
}

type Room struct {
	Code     string
	Host     string
	Players  []*Player
	Settings Settings
	State    string // lobby | pick | round | reveal | final
	Locs     []Loc
	Round    int // номер текущего раунда с 1
	Deadline time.Time
	timer    *time.Timer
	idleAt   time.Time
	asked    bool // у хоста уже запрошены новые места
	waitNext bool // хост нажал «дальше», а места еще нет

	RoundStart  time.Time
	Targets     []map[string]Loc    // по раундам: свое место у каждого игрока (два взгляда, загадай место)
	From        []map[string]string // загадай место: кто загадал место игроку
	Scouts      []string            // штурман и разведчик: разведчик раунда
	Team        []int               // счет команды по раундам
	Mids        []*Loc              // общая экспедиция: середина ответов
	PickBy      map[string]Loc      // загадай место: панорамы, загаданные на следующий раунд
	picking     map[string]bool     // ищем панораму у загаданной точки
	pendTargets map[string]Loc
	pendFrom    map[string]string
}

type Conn struct {
	login  string
	ws     *websocket.Conn
	send   chan []byte
	room   *Room
	player *Player
}

var (
	mu    sync.Mutex
	rooms = map[string]*Room{}
)

// ---------- утилиты ----------

func randToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func randCode() string {
	const letters = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	for {
		var sb strings.Builder
		for i := 0; i < 5; i++ {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
			sb.WriteByte(letters[n.Int64()])
		}
		if rooms[sb.String()] == nil {
			return sb.String()
		}
	}
}

func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371008.8
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}

func calcScore(d, maxDist float64) int {
	if d <= 25 {
		return maxScore
	}
	return int(math.Round(maxScore * math.Exp(-10*d/maxDist)))
}

func cleanName(s string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 32 {
			return -1
		}
		return r
	}, s))
	if utf8.RuneCountInString(s) > 20 {
		s = string([]rune(s)[:20])
	}
	if s == "" {
		s = "Игрок"
	}
	return s
}

func validLoc(l Loc) bool {
	if l.Alt != nil && (l.Alt.Alt != nil || !validLoc(*l.Alt)) {
		return false
	}
	return l.Lat >= -90 && l.Lat <= 90 && l.Lon >= -180 && l.Lon <= 180 &&
		!math.IsNaN(l.Lat) && !math.IsNaN(l.Lon) && !math.IsNaN(l.Dir)
}

// ---------- состояние для клиентов ----------

type playerView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Score   int    `json:"score"`
	Guessed bool   `json:"guessed"`
	Online  bool   `json:"online"`
	HP      int    `json:"hp"`
	Out     bool   `json:"out"`
	Pull    int    `json:"pull"`
	Picked  bool   `json:"picked"`  // загадай место: уже загадал
	Picking bool   `json:"picking"` // загадай место: ищем панораму у его точки
}

type guessView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	*Guess
}

type roundView struct {
	Answer  Loc               `json:"answer"`
	Guesses []guessView       `json:"guesses"`
	Damage  map[string]int    `json:"damage,omitempty"` // дуэль: id игрока -> урон, канат: сдвиг
	Mult    float64           `json:"mult,omitempty"`
	Team    int               `json:"team,omitempty"`    // счет команды за раунд
	Mid     *Loc              `json:"mid,omitempty"`     // общая экспедиция: середина ответов
	Targets map[string]Loc    `json:"targets,omitempty"` // свое место у каждого игрока
	From    map[string]string `json:"from,omitempty"`    // загадай место: кто загадал
	Scout   string            `json:"scout,omitempty"`   // разведчик раунда
}

type stateMsg struct {
	T        string       `json:"t"`
	Room     string       `json:"room"`
	Host     string       `json:"host"`
	State    string       `json:"state"`
	Settings Settings     `json:"settings"`
	Round    int          `json:"round"`
	Players  []playerView `json:"players"`
	Loc      *Loc         `json:"loc,omitempty"`
	Left     int64        `json:"left"`              // мс до конца раунда, 0 = без лимита
	Results  []roundView  `json:"results,omitempty"` // reveal: последний раунд, final: все
	Usage    int          `json:"usage"`             // поисков панорам сегодня (МСК)
	PoolFree int          `json:"poolFree"`          // несыгранных мест в запасе для региона
	Mult     float64      `json:"mult"`              // дуэль: множитель урона текущего раунда
	Last     bool         `json:"last"`              // reveal: дальше итоги игры
	Waiting  bool         `json:"waiting"`           // ждем, пока хост найдет следующее место
	Team     int          `json:"team"`              // общий счет команды
	TeamGoal int          `json:"teamGoal"`          // цель команды на всю игру
	Scout    string       `json:"scout,omitempty"`   // разведчик текущего раунда
	TugWin   int          `json:"tugWin,omitempty"`  // канат: сколько надо перетянуть
	Next     *Loc         `json:"next,omitempty"`    // reveal: место следующего раунда для предзагрузки
}

// snapshot - состояние комнаты глазами игрока me: места и роли у игроков бывают разные.
func (r *Room) snapshot(me *Player) []byte {
	m := stateMsg{T: "state", Room: r.Code, Host: r.Host, State: r.State, Settings: r.Settings, Round: r.Round,
		Usage: usageToday(), PoolFree: poolFree(r.Settings.poolKey()),
		Mult: duelMult(r.Round), Last: r.State == "reveal" && r.gameOver(), Waiting: r.waitNext,
		Scout: r.scout()}
	if r.Settings.team() {
		m.Team, m.TeamGoal = r.teamTotal(), r.Settings.coopPerRound()*r.totalRounds()
	}
	if r.Settings.tug() {
		m.TugWin = tugWin
	}
	for _, p := range r.Players {
		_, picked := r.PickBy[p.ID]
		m.Players = append(m.Players, playerView{
			ID: p.ID, Name: p.Name, Score: p.Score, Online: p.conn != nil, HP: p.HP, Out: p.Out, Pull: p.Pull,
			Guessed: r.State == "round" && len(p.Rounds) >= r.Round && p.Rounds[r.Round-1] != nil,
			Picked:  r.State == "pick" && picked, Picking: r.State == "pick" && r.picking[p.ID],
		})
	}
	if r.State == "round" || r.State == "reveal" {
		// штурман панорамы не видит: место ему не отправляем
		navigator := r.scout() != "" && me != nil && me.ID != r.scout() && !me.Out
		if r.State == "round" && !navigator {
			l := r.Locs[r.Round-1]
			if me != nil {
				l = r.target(me, r.Round-1)
			}
			l.Alt = nil
			m.Loc = &l
		}
		if r.State == "round" && !r.Deadline.IsZero() {
			m.Left = time.Until(r.Deadline).Milliseconds()
			if m.Left < 1 {
				m.Left = 1
			}
		}
	}
	switch r.State {
	case "reveal":
		m.Results = []roundView{r.roundResult(r.Round - 1)}
		// пока смотрят итоги, браузер загружает панораму следующего раунда
		if me != nil && !r.gameOver() {
			if l, ok := r.peekTarget(me, r.Round); ok {
				m.Next = &l
			}
		}
	case "final":
		for i := 0; i < r.Round; i++ {
			m.Results = append(m.Results, r.roundResult(i))
		}
	}
	b, _ := json.Marshal(m)
	return b
}

func (r *Room) roundResult(i int) roundView {
	rv := roundView{Answer: r.Locs[i], Guesses: []guessView{}}
	rv.Answer.Alt = nil
	if r.Settings.duel() || r.Settings.tug() {
		rv.Damage, rv.Mult = map[string]int{}, duelMult(i+1)
	}
	if i < len(r.Targets) {
		rv.Targets, rv.From = r.Targets[i], r.From[i]
	}
	if i < len(r.Scouts) {
		rv.Scout = r.Scouts[i]
	}
	if i < len(r.Team) {
		rv.Team, rv.Mid = r.Team[i], r.Mids[i]
	}
	for _, p := range r.Players {
		if len(p.Rounds) > i && p.Rounds[i] != nil {
			rv.Guesses = append(rv.Guesses, guessView{ID: p.ID, Name: p.Name, Guess: p.Rounds[i]})
		}
		if rv.Damage != nil && len(p.Dmg) > i {
			rv.Damage[p.ID] = p.Dmg[i]
		}
	}
	return rv
}

func (r *Room) broadcast() {
	for _, p := range r.Players {
		if p.conn != nil {
			p.conn.push(r.snapshot(p))
		}
	}
}

func (c *Conn) push(b []byte) {
	select {
	case c.send <- b:
	default:
		// клиент не успевает читать, рвем соединение
		c.ws.Close()
	}
}

func (c *Conn) sendJSON(v any) {
	b, _ := json.Marshal(v)
	c.push(b)
}

func (c *Conn) fail(msg string) {
	c.sendJSON(map[string]string{"t": "error", "msg": msg})
}

// ---------- логика игры ----------

func (r *Room) startRound() {
	r.Round++
	r.State = "round"
	r.waitNext = false
	for _, p := range r.Players {
		for len(p.Rounds) < r.Round {
			p.Rounds = append(p.Rounds, nil)
		}
	}
	r.stopTimer()
	r.Deadline = time.Time{}
	if r.Settings.Timer > 0 {
		r.setDeadline(time.Duration(r.Settings.Timer+revealGraceSec) * time.Second)
	}
	r.prepareRound()
	r.requestLocs()
	r.broadcast()
}

// setDeadline заводит конец текущего раунда через d.
func (r *Room) setDeadline(d time.Duration) {
	r.stopTimer()
	r.Deadline = time.Now().Add(d)
	round := r.Round
	r.timer = time.AfterFunc(d, func() {
		mu.Lock()
		defer mu.Unlock()
		if r.State == "round" && r.Round == round {
			r.reveal()
		}
	})
}

func (r *Room) stopTimer() {
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
}

func (r *Room) totalRounds() int {
	if r.Settings.duel() || r.Settings.tug() {
		return duelMaxRounds
	}
	return r.Settings.Rounds
}

// requestLocs просит хоста найти еще мест, когда запас на исходе.
// В «Загадай место» места загадывают игроки, хост их не ищет.
func (r *Room) requestLocs() {
	if r.Settings.Mode == "custom" || r.asked || len(r.Locs) >= r.totalRounds() || len(r.Locs)-r.Round >= 2 {
		return
	}
	h := r.player(r.Host)
	if h == nil || h.conn == nil {
		return
	}
	r.asked = true
	h.conn.sendJSON(map[string]any{"t": "needLocs", "count": min(locBatch, r.totalRounds()-len(r.Locs))})
}

// duelMult - множитель урона: первые 4 раунда x1, дальше +0.5 за раунд.
func duelMult(round int) float64 {
	if round <= 4 {
		return 1
	}
	return 1 + 0.5*float64(round-4)
}

func (r *Room) alive() int {
	n := 0
	for _, p := range r.Players {
		if !p.Out {
			n++
		}
	}
	return n
}

func (r *Room) gameOver() bool {
	switch {
	case r.Settings.duel():
		return r.alive() <= 1 || r.Round >= duelMaxRounds
	case r.Settings.tug():
		for _, p := range r.Players {
			if p.Pull >= tugWin || p.Pull <= -tugWin {
				return true
			}
		}
		return r.Round >= duelMaxRounds
	}
	return r.Round >= r.Settings.Rounds
}

func (r *Room) reveal() {
	r.stopTimer()
	r.autoGuess()
	r.settleRound()
	r.State = "reveal"
	r.broadcast()
}

// autoGuess засчитывает метки тех, кто поставил их, но не нажал «Угадать».
func (r *Room) autoGuess() {
	i := r.Round - 1
	for _, p := range r.Players {
		if p.Mark == nil || p.MarkOf != r.Round || len(p.Rounds) <= i || p.Rounds[i] != nil || !r.canGuess(p) {
			continue
		}
		g := r.scoreGuess(p, p.Mark.Lat, p.Mark.Lon, p.MarkBet, false)
		p.Rounds[i] = g
		p.Score += g.Score
	}
}

// applyDamage: каждый живой теряет (лучший результат раунда - свой) * множитель.
func (r *Room) applyDamage() {
	i := r.Round - 1
	best := 0
	for _, p := range r.Players {
		if !p.Out {
			best = max(best, p.roundScore(i))
		}
	}
	mult := duelMult(r.Round)
	for _, p := range r.Players {
		for len(p.Dmg) <= i {
			p.Dmg = append(p.Dmg, 0)
		}
		if p.Out {
			continue
		}
		d := int(math.Round(float64(best-p.roundScore(i)) * mult))
		p.Dmg[i] = d
		p.HP -= d
		if p.HP <= 0 {
			p.HP, p.Out = 0, true
		}
	}
}

// Раунд заканчивается, когда ответили все, кто онлайн и может отвечать
// (не выбыл, не зритель, не разведчик).
func (r *Room) checkAllGuessed() {
	if r.State != "round" {
		return
	}
	online := 0
	for _, p := range r.Players {
		if p.conn == nil || !r.canGuess(p) {
			continue
		}
		online++
		if len(p.Rounds) < r.Round || p.Rounds[r.Round-1] == nil {
			return
		}
	}
	if online > 0 {
		r.reveal()
	}
}

func (r *Room) guessCount() int {
	n := 0
	for _, p := range r.Players {
		if len(p.Rounds) >= r.Round && p.Rounds[r.Round-1] != nil {
			n++
		}
	}
	return n
}

func validLocs(locs []Loc) bool {
	for _, l := range locs {
		if !validLoc(l) {
			return false
		}
	}
	return true
}

func (r *Room) player(id string) *Player {
	for _, p := range r.Players {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// ensureHost передает хоста подключенному игроку; true - хост сменился.
func (r *Room) ensureHost() bool {
	if h := r.player(r.Host); h != nil && h.conn != nil {
		return false
	}
	for _, p := range r.Players {
		if p.conn != nil {
			r.Host = p.ID
			return true
		}
	}
	return false
}

// reaskLocs: у хоста новое соединение, прежний запрос мест до него не дойдет.
func (r *Room) reaskLocs() {
	if r.State != "round" && r.State != "reveal" {
		return
	}
	r.asked = false
	r.requestLocs()
}

type inMsg struct {
	T        string    `json:"t"`
	Name     string    `json:"name"`
	Room     string    `json:"room"`
	Settings *Settings `json:"settings"`
	Locs     []Loc     `json:"locs"`
	Round    int       `json:"round"`
	Lat      float64   `json:"lat"`
	Lon      float64   `json:"lon"`
	Region   string    `json:"region"`
	ID       string    `json:"id"`
	Count    int       `json:"count"`
	N        int       `json:"n"`
	Bet      int       `json:"bet"` // ставки: x1..x3
}

func (c *Conn) handle(m inMsg) {
	mu.Lock()
	defer mu.Unlock()

	if m.T == "hello" {
		c.hello(m)
		return
	}
	r, p := c.room, c.player
	if r == nil {
		c.fail("Сначала войди в комнату")
		return
	}
	isHost := r.Host == p.ID

	switch m.T {
	case "settings":
		if !isHost || m.Settings == nil || (r.State != "lobby" && r.State != "final") {
			return
		}
		r.Settings = *m.Settings
		r.broadcast()

	case "start":
		if !isHost || m.Settings == nil || (r.State != "lobby" && r.State != "final") {
			return
		}
		s := *m.Settings
		if !gameModes[s.Mode] {
			s.Mode = "classic"
		}
		if s.Move != "nomove" && s.Move != "nmpz" {
			s.Move = "move"
		}
		// свои места рядом и загаданные точки ищутся только по покрытию Google
		if s.Provider != "google" && !s.ownTargets() {
			s.Provider = "yandex"
		} else {
			s.Provider = "google"
		}
		if s.Layer != "air" || s.Provider == "google" {
			s.Layer = "ground"
		}
		online := 0
		for _, pl := range r.Players {
			if pl.conn != nil {
				online++
			}
		}
		switch s.Mode {
		case "duel", "twoviews", "custom":
			if online < 2 {
				c.fail("Для этого режима нужно хотя бы 2 игрока")
				return
			}
		case "tug", "scout":
			if online != 2 {
				c.fail("Для этого режима нужно ровно 2 игрока")
				return
			}
		}
		if !s.duel() && !s.tug() && (s.Rounds < 1 || s.Rounds > maxRounds) {
			c.fail("Неверное число раундов")
			return
		}
		if s.Mode != "custom" && (len(m.Locs) < 1 || len(m.Locs) > maxRounds || !validLocs(m.Locs)) {
			c.fail("Неверные места для раундов")
			return
		}
		if s.Timer < 0 || s.Timer > 3600 {
			s.Timer = 0
		}
		if s.GuessTimer < 0 || s.GuessTimer > 600 {
			s.GuessTimer = 0
		}
		if s.Mode == "speed" {
			// без таймера скорость не посчитать
			if s.Timer == 0 {
				s.Timer = 60
			}
			if s.GuessTimer == 0 {
				s.GuessTimer = 10
			}
		}
		if s.MaxDist < 1000 {
			s.MaxDist = 1000
		}
		r.Settings = s
		r.Locs = nil
		if s.Mode != "custom" {
			r.Locs = m.Locs
		}
		if len(r.Locs) > r.totalRounds() {
			r.Locs = r.Locs[:r.totalRounds()]
		}
		poolMarkPlayed(s.poolKey(), r.Locs)
		r.Round, r.asked, r.waitNext = 0, false, false
		r.Targets, r.From, r.Scouts, r.Team, r.Mids = nil, nil, nil, nil, nil
		for _, pl := range r.Players {
			pl.Score, pl.Rounds, pl.HP, pl.Dmg, pl.Pull, pl.Mark = 0, nil, duelHP, nil, 0, nil
			// в режимах с постоянным составом отключенные на старте только смотрят
			pl.Out = s.fixedPlayers() && pl.conn == nil
		}
		if s.Mode == "custom" {
			r.enterPick()
		} else {
			r.startRound()
		}

	case "moreLocs":
		if !isHost || r.State == "lobby" || r.State == "final" || len(m.Locs) > locBatch || !validLocs(m.Locs) {
			return
		}
		r.asked = false
		if len(m.Locs) == 0 {
			// хост не нашел мест: снимаем ожидание, чтобы «Дальше» запросило поиск снова
			if r.waitNext {
				r.waitNext = false
				r.broadcast()
			}
			break
		}
		room := r.totalRounds() - len(r.Locs)
		if len(m.Locs) > room {
			m.Locs = m.Locs[:room]
		}
		r.Locs = append(r.Locs, m.Locs...)
		poolMarkPlayed(r.Settings.poolKey(), m.Locs)
		if r.waitNext && r.Round < len(r.Locs) {
			r.startRound()
		} else if r.State == "reveal" {
			r.broadcast() // игроки получат следующее место для предзагрузки
		}

	case "kick":
		if !isHost || m.ID == p.ID {
			return
		}
		for i, pl := range r.Players {
			if pl.ID != m.ID {
				continue
			}
			r.Players = append(r.Players[:i], r.Players[i+1:]...)
			if pl.conn != nil {
				pl.conn.sendJSON(map[string]string{"t": "kicked"})
				pl.conn.room, pl.conn.player = nil, nil
				pl.conn.push(nil) // закрыть после сообщения о выгоне
			}
			break
		}
		r.broadcast()
		r.checkAllGuessed()
		r.checkAllPicked()

	case "reroll":
		// у игрока не открылась панорама: просим хоста подобрать другое место
		if r.State != "round" || m.Round != r.Round {
			return
		}
		if h := r.player(r.Host); h != nil && h.conn != nil {
			h.conn.sendJSON(map[string]any{"t": "needReroll", "round": r.Round, "by": p.Name})
		}

	case "replaceLoc":
		if !isHost || r.State != "round" || m.Round != r.Round || len(m.Locs) != 1 || !validLocs(m.Locs) ||
			r.Settings.Mode == "custom" {
			return
		}
		i := r.Round - 1
		r.Locs[i] = m.Locs[0]
		r.prepareRound()
		poolMarkPlayed(r.Settings.poolKey(), m.Locs)
		// ответы и метки по старому месту больше не считаются, раунд начинается заново
		for _, pl := range r.Players {
			pl.Mark = nil
			if len(pl.Rounds) > i && pl.Rounds[i] != nil {
				pl.Score -= pl.Rounds[i].Score
				pl.Rounds[i] = nil
			}
		}
		r.stopTimer()
		r.Deadline = time.Time{}
		if r.Settings.Timer > 0 {
			r.setDeadline(time.Duration(r.Settings.Timer+revealGraceSec) * time.Second)
		}
		r.broadcast()

	case "makeHost":
		if t := r.player(m.ID); isHost && t != nil && t.conn != nil {
			r.Host = t.ID
			r.asked = false
			r.broadcast()
			r.requestLocs()
		}

	case "poolGet":
		if !isHost || m.Count < 1 || m.Count > maxRounds {
			return
		}
		c.sendJSON(map[string]any{"t": "pool", "locs": poolTake(m.Region, m.Count)})

	case "poolAdd":
		if len(m.Locs) > 100 {
			return
		}
		poolAdd(m.Region, m.Locs)
		r.broadcast()

	case "spent":
		if m.N < 1 || m.N > 100 {
			return
		}
		usageAdd(m.N)
		r.broadcast()

	case "mark":
		// метка поставлена или сдвинута без подтверждения: сервер помнит последнюю
		if r.State != "round" || m.Round != r.Round || !validLoc(Loc{Lat: m.Lat, Lon: m.Lon}) {
			return
		}
		p.Mark = &Loc{Lat: m.Lat, Lon: m.Lon}
		p.MarkOf, p.MarkBet = r.Round, m.Bet

	case "guess":
		if r.State != "round" || m.Round != r.Round || !validLoc(Loc{Lat: m.Lat, Lon: m.Lon}) {
			return
		}
		if len(p.Rounds) < r.Round || p.Rounds[r.Round-1] != nil || !r.canGuess(p) {
			return
		}
		first := r.guessCount() == 0
		g := r.scoreGuess(p, m.Lat, m.Lon, m.Bet, first)
		p.Rounds[r.Round-1] = g
		p.Score += g.Score
		// после первого ответа остальным остается GuessTimer секунд
		if first && r.Settings.GuessTimer > 0 {
			d := time.Duration(r.Settings.GuessTimer+revealGraceSec) * time.Second
			if r.Deadline.IsZero() || time.Until(r.Deadline) > d {
				r.setDeadline(d)
			}
		}
		r.broadcast()
		r.checkAllGuessed()

	case "next":
		if !isHost {
			return
		}
		switch r.State {
		case "reveal":
			switch {
			case r.gameOver():
				r.State = "final"
				r.broadcast()
			case r.Settings.Mode == "custom":
				r.enterPick()
			case r.Round < len(r.Locs):
				r.startRound()
			default:
				// хост еще ищет следующее место
				r.waitNext = true
				r.requestLocs()
				r.broadcast()
			}
		case "final":
			r.State = "lobby"
			r.broadcast()
		}

	case "end":
		// хост заканчивает игру досрочно: раунд, где никто не ответил, не считается
		if !isHost || (r.State != "round" && r.State != "reveal" && r.State != "pick") {
			return
		}
		r.stopTimer()
		r.Deadline = time.Time{}
		r.waitNext = false
		if r.State == "round" {
			r.autoGuess()
			if r.guessCount() == 0 {
				r.Round--
			} else {
				r.settleRound()
			}
		}
		if r.Round == 0 {
			r.State = "lobby"
		} else {
			r.State = "final"
		}
		r.broadcast()

	case "pick":
		// загадай место: точка для соперника, панораму рядом найдет сервер
		if r.State != "pick" || p.Out || !validLoc(Loc{Lat: m.Lat, Lon: m.Lon}) {
			return
		}
		r.pick(p, m.Lat, m.Lon)
	}
}

func (c *Conn) hello(m inMsg) {
	if c.room != nil {
		return
	}
	name := cleanName(nickFor(c.login))
	code := strings.ToUpper(strings.TrimSpace(m.Room))

	var r *Room
	if code == "" {
		if len(rooms) >= maxRooms {
			c.fail("Сервер переполнен, попробуй позже")
			return
		}
		r = &Room{Code: randCode(), State: "lobby"}
		rooms[r.Code] = r
	} else if r = rooms[code]; r == nil {
		c.fail("Комната " + code + " не найдена")
		return
	}

	// игрок привязан к аккаунту: повторный вход того же логина занимает его место
	var p *Player
	for _, pl := range r.Players {
		if pl.Login == c.login {
			p = pl
			break
		}
	}
	if p != nil && p.conn != nil {
		p.conn.ws.Close()
		p.conn.room, p.conn.player = nil, nil
	}
	if p == nil {
		if len(r.Players) >= maxPlayers {
			c.fail("В комнате нет мест")
			return
		}
		p = &Player{ID: randToken(4), Login: c.login, HP: duelHP}
		// в идущую игру с постоянным составом (дуэль, канат, разведчик) новичок входит зрителем
		p.Out = r.Settings.fixedPlayers() && r.State != "lobby" && r.State != "final"
		r.Players = append(r.Players, p)
		for len(p.Rounds) < r.Round {
			p.Rounds = append(p.Rounds, nil)
		}
	}
	p.Name = name
	p.conn = c
	c.room, c.player = r, p
	if r.Host == "" {
		r.Host = p.ID
	}
	changed := r.ensureHost()
	c.sendJSON(map[string]string{"t": "welcome", "you": p.ID, "room": r.Code})
	r.broadcast()
	if changed || p.ID == r.Host {
		r.reaskLocs()
	}
}

func (c *Conn) leave() {
	mu.Lock()
	defer mu.Unlock()
	r, p := c.room, c.player
	if r == nil || p.conn != c {
		return
	}
	p.conn = nil
	c.room, c.player = nil, nil
	if r.State == "lobby" {
		// из лобби уходят насовсем, в игре место держим для переподключения
		for i, pl := range r.Players {
			if pl == p {
				r.Players = append(r.Players[:i], r.Players[i+1:]...)
				break
			}
		}
	}
	if r.ensureHost() {
		r.reaskLocs()
	}
	r.idleAt = time.Now()
	r.broadcast()
	r.checkAllGuessed()
	r.checkAllPicked()
}

func janitor() {
	for range time.Tick(time.Minute) {
		mu.Lock()
		for code, r := range rooms {
			online := false
			for _, p := range r.Players {
				if p.conn != nil {
					online = true
					break
				}
			}
			if !online && time.Since(r.idleAt) > roomIdleTTL {
				r.stopTimer()
				delete(rooms, code)
			}
		}
		mu.Unlock()
	}
}

// ---------- HTTP ----------

var upgrader = websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096}

func serveWS(w http.ResponseWriter, req *http.Request) {
	login := authLogin(req)
	if login == "" {
		http.Error(w, "нужен вход", http.StatusUnauthorized)
		return
	}
	ws, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	c := &Conn{login: login, ws: ws, send: make(chan []byte, 64)}
	ws.SetReadLimit(32 << 10)

	go func() {
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			select {
			case b, ok := <-c.send:
				if !ok {
					return
				}
				if b == nil {
					// просьба закрыть соединение после уже отправленных сообщений
					ws.Close()
					return
				}
				ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if ws.WriteMessage(websocket.TextMessage, b) != nil {
					ws.Close()
					return
				}
			case <-ping.C:
				ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if ws.WriteMessage(websocket.PingMessage, nil) != nil {
					ws.Close()
					return
				}
			}
		}
	}()

	ws.SetReadDeadline(time.Now().Add(60 * time.Second))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		var m inMsg
		if json.Unmarshal(data, &m) == nil {
			c.handle(m)
		}
	}
	c.leave()
	ws.Close()
}

// withETags ставит статике ETag по содержимому и no-cache: после обновления
// бинарника браузер сразу берет новые файлы, а без изменений получает 304.
func withETags(fsys fs.FS, next http.Handler) http.Handler {
	etags := map[string]string{}
	fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, err := fs.ReadFile(fsys, p); err == nil {
				sum := sha256.Sum256(b)
				etags["/"+p] = `"` + hex.EncodeToString(sum[:8]) + `"`
			}
		}
		return nil
	})
	etags["/"] = etags["/index.html"]
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tag, ok := etags[r.URL.Path]; ok {
			w.Header().Set("ETag", tag)
		}
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}
