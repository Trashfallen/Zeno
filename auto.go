package main

// Автосоединение с друзьями: обменялись кодами дружбы один раз - дальше
// программы находят друг друга сами при каждом запуске.
//
// У каждой пары друзей свой постоянный канал на публичных серверах (transport.go).
// Тема и ключ шифрования выводятся из общего секрета двух ключей (X25519),
// поэтому знают их только эти двое; брокер видит случайную тему и шум.
// Пока программа запущена, она держит подключение к серверам (несколько
// килобайт в минуту). По каналу ходят «я в сети» и подписанные коды
// соединения - их создает и разбирает окно (WebRTC живет там), Go только
// шифрует и передает.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

const autoMaxSkew = 10 * time.Minute

type autoEvent struct {
	From  string `json:"from"`
	Kind  string `json:"kind"` // hello | offer | answer
	Data  string `json:"data"`
	Reply bool   `json:"reply"`
	TS    int64  `json:"ts"` // время отправки по часам друга, мс
}

type autoWire struct {
	ID string `json:"i"`
	TS int64  `json:"t"`
	autoEvent
}

type autoPair struct {
	friend string
	topic  string
	aead   cipher.AEAD
}

type Auto struct {
	app    *App
	mu     sync.Mutex
	acc    *Account
	ts     []transport
	pairs  map[string]*autoPair // тема -> пара
	byID   map[string]*autoPair // друг -> пара
	events chan autoEvent
	seen   map[string]time.Time // id сообщений: одно и то же приходит от нескольких брокеров
	stop   chan struct{}
}

func newAuto(app *App) *Auto {
	return &Auto{app: app, events: make(chan autoEvent, 256), pairs: map[string]*autoPair{}, byID: map[string]*autoPair{}, seen: map[string]time.Time{}}
}

func (a *Account) pair(friendID string) (*autoPair, error) {
	secret, err := a.sharedSecret(friendID)
	if err != nil {
		return nil, err
	}
	t := sha256.Sum256(append([]byte("zeno-pair-topic|"), secret...))
	k := sha256.Sum256(append([]byte("zeno-pair-key|"), secret...))
	block, _ := aes.NewCipher(k[:])
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &autoPair{friend: friendID, topic: "zeno/v1/p/" + hex.EncodeToString(t[:16]), aead: g}, nil
}

// Start подключается к серверам от имени учетной записи и слушает каналы друзей.
func (au *Auto) Start(acc *Account) {
	au.Stop()
	au.mu.Lock()
	au.acc = acc
	au.stop = make(chan struct{})
	au.mu.Unlock()
	au.Sync()
	var ts []transport
	for _, url := range au.app.rdv.brokers() {
		// без истории: старые предложения соединения из кэша сервера только мешают
		ts = append(ts, dialTransport(url, 0, func(t transport) {
			t.SetTopics(au.topics())
			au.push(autoEvent{Kind: "broker"}) // новый сервер - повод еще раз сказать друзьям «я в сети»
		}, au.onMessage))
	}
	au.mu.Lock()
	au.ts = ts
	au.mu.Unlock()
}

func (au *Auto) topics() []string {
	au.mu.Lock()
	defer au.mu.Unlock()
	out := make([]string, 0, len(au.pairs))
	for t := range au.pairs {
		out = append(out, t)
	}
	return out
}

func (au *Auto) Stop() {
	au.mu.Lock()
	ts := au.ts
	au.ts = nil
	au.acc = nil
	au.pairs = map[string]*autoPair{}
	au.byID = map[string]*autoPair{}
	if au.stop != nil {
		close(au.stop)
		au.stop = nil
	}
	au.mu.Unlock()
	for _, t := range ts {
		go t.Close()
	}
}

// Sync подписывается на каналы новых друзей и отписывается от удаленных.
func (au *Auto) Sync() {
	au.mu.Lock()
	acc := au.acc
	au.mu.Unlock()
	if acc == nil {
		return
	}
	want := map[string]*autoPair{}
	byID := map[string]*autoPair{}
	for _, f := range acc.Friends() {
		if p, err := acc.pair(f.ID); err == nil {
			want[p.topic] = p
			byID[f.ID] = p
		}
	}
	au.mu.Lock()
	au.pairs, au.byID = want, byID
	ts := append([]transport(nil), au.ts...)
	au.mu.Unlock()
	topics := au.topics()
	for _, t := range ts {
		t.SetTopics(topics)
	}
}

func (au *Auto) onMessage(topic string, data []byte) {
	au.mu.Lock()
	p := au.pairs[topic]
	acc := au.acc
	au.mu.Unlock()
	if p == nil || acc == nil || len(data) < p.aead.NonceSize() {
		return
	}
	plain, err := p.aead.Open(nil, data[:p.aead.NonceSize()], data[p.aead.NonceSize():], []byte(p.topic))
	if err != nil {
		return
	}
	var w autoWire
	if json.Unmarshal(plain, &w) != nil || w.From != p.friend || w.From == acc.ID() {
		return // свое же сообщение или чужое в канале
	}
	if d := time.Since(time.UnixMilli(w.TS)); d > autoMaxSkew || d < -autoMaxSkew {
		return
	}
	au.mu.Lock()
	if _, dup := au.seen[w.ID]; dup {
		au.mu.Unlock()
		return
	}
	au.seen[w.ID] = time.Now()
	if len(au.seen) > 2000 {
		for id, t := range au.seen {
			if time.Since(t) > 2*autoMaxSkew {
				delete(au.seen, id)
			}
		}
	}
	au.mu.Unlock()
	logf("Авто: %s <- %s", w.Kind, short8(w.From))
	ev := w.autoEvent
	ev.TS = w.TS
	au.push(ev)
}

func short8(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (au *Auto) push(ev autoEvent) {
	select {
	case au.events <- ev:
	default: // окна нет или оно не забирает - старые события не нужны
	}
}

// Send шифрует и отправляет событие другу через все подключенные брокеры.
func (au *Auto) Send(to, kind, data string, reply bool) error {
	au.mu.Lock()
	p := au.byID[to]
	acc := au.acc
	ts := append([]transport(nil), au.ts...)
	au.mu.Unlock()
	if p == nil || acc == nil {
		return errNotFound
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	w := autoWire{ID: hex.EncodeToString(id), TS: time.Now().UnixMilli(), autoEvent: autoEvent{From: acc.ID(), Kind: kind, Data: data, Reply: reply}}
	plain, _ := json.Marshal(w)
	nonce := make([]byte, p.aead.NonceSize())
	_, _ = rand.Read(nonce)
	payload := p.aead.Seal(nonce, nonce, plain, []byte(p.topic))
	sent := 0
	for _, t := range ts {
		if t.Ready() {
			go t.Publish(p.topic, payload, false)
			sent++
		}
	}
	if sent == 0 {
		return errors.New("нет связи с публичными серверами")
	}
	logf("Авто: %s -> %s (серверов %d)", kind, short8(to), sent)
	return nil
}

// Poll отдает накопившиеся события, ожидая первое не дольше timeout.
func (au *Auto) Poll(timeout time.Duration) []autoEvent {
	var out []autoEvent
	select {
	case ev := <-au.events:
		out = append(out, ev)
	case <-time.After(timeout):
		return out
	}
	for {
		select {
		case ev := <-au.events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// Online - сколько брокеров сейчас на связи.
func (au *Auto) Online() int {
	au.mu.Lock()
	defer au.mu.Unlock()
	n := 0
	for _, t := range au.ts {
		if t.Ready() {
			n++
		}
	}
	return n
}
