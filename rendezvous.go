package main

// Короткие коды соединения (6 знаков) без своего сервера.
//
// Длинный код (подписанный SDP) кладется в «почтовый ящик» на публичных
// MQTT-брокерах. Тема ящика выводится из короткого кода, содержимое
// зашифровано ключом из короткого кода и общего секрета двух друзей (X25519
// из их ключей Ed25519): брокер видит только шум, и даже подобрав код, без
// закрытого ключа одного из друзей ящик не открыть. Сообщения retained -
// второй может ввести код в любой момент в течение часа. После соединения
// ящик очищается. Серверов несколько (transport.go), работают параллельно.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"sync"
	"time"

	"filippo.io/edwards25519"
)

const (
	shortAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // без 0/O и 1/I
	shortLen      = 6
	rdvLifetime   = time.Hour
)

// normShort: «k7q-2m9» -> «K7Q2M9»; пусто, если это не короткий код.
func normShort(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch {
		case r == '-' || r == ' ':
		case strings.ContainsRune(shortAlphabet, r):
			b.WriteRune(r)
		case r == '0':
			b.WriteRune('O') // путают
		case r == '1':
			b.WriteRune('I')
		default:
			return ""
		}
	}
	if b.Len() != shortLen || strings.ContainsAny(b.String(), "OI") {
		return ""
	}
	return b.String()
}

func newShort() string {
	out := make([]byte, shortLen)
	for i := range out {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(shortAlphabet))))
		out[i] = shortAlphabet[n.Int64()]
	}
	return string(out)
}

func rdvTopic(short string) string {
	h := sha256.Sum256([]byte("zeno-rdv-topic|" + short))
	return "zeno/v1/" + hex.EncodeToString(h[:16])
}

// sharedSecret - общий секрет с другом: X25519 из ключей Ed25519.
func (a *Account) sharedSecret(friendID string) ([]byte, error) {
	pub, err := b64.DecodeString(friendID)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("кривой ключ друга")
	}
	p, err := new(edwards25519.Point).SetBytes(pub)
	if err != nil {
		return nil, err
	}
	theirs, err := ecdh.X25519().NewPublicKey(p.BytesMontgomery())
	if err != nil {
		return nil, err
	}
	h := sha512.Sum512(a.priv.Seed())
	mine, err := ecdh.X25519().NewPrivateKey(h[:32])
	if err != nil {
		return nil, err
	}
	return mine.ECDH(theirs)
}

func (a *Account) rdvAEAD(friendID, short string) (cipher.AEAD, error) {
	secret, err := a.sharedSecret(friendID)
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256(append([]byte("zeno-rdv-key|"+short+"|"), secret...))
	block, _ := aes.NewCipher(key[:])
	return cipher.NewGCM(block)
}

func (a *Account) rdvSeal(friendID, short, topic, text string) ([]byte, error) {
	g, err := a.rdvAEAD(friendID, short)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	_, _ = rand.Read(nonce)
	return g.Seal(nonce, nonce, []byte(text), []byte(topic)), nil
}

// rdvOpen пробует расшифровать ящик ключом каждого друга; возвращает текст и друга.
func (a *Account) rdvOpen(short, topic string, data []byte) (string, string, bool) {
	for _, f := range a.Friends() {
		g, err := a.rdvAEAD(f.ID, short)
		if err != nil || len(data) < g.NonceSize() {
			continue
		}
		if plain, err := g.Open(nil, data[:g.NonceSize()], data[g.NonceSize():], []byte(topic)); err == nil {
			return string(plain), f.ID, true
		}
	}
	return "", "", false
}

// ---------- ящик на публичных серверах ----------

type rdvBox struct {
	short, topic, friend string
	mu                   sync.Mutex
	ts                   []transport
	subs                 []string // на что подписан ящик
	offer                []byte   // что лежит в ящике (у того, кто его создал)
	closed               bool
	got                  chan string // расшифрованный длинный код от друга
	once                 sync.Once
	created              time.Time
}

type Rendezvous struct {
	app   *App
	mu    sync.Mutex
	boxes map[string]*rdvBox
}

func newRendezvous(app *App) *Rendezvous {
	r := &Rendezvous{app: app, boxes: map[string]*rdvBox{}}
	go func() {
		for range time.Tick(time.Minute) {
			r.mu.Lock()
			for k, b := range r.boxes {
				if time.Since(b.created) > rdvLifetime {
					go b.close(true)
					delete(r.boxes, k)
				}
			}
			r.mu.Unlock()
		}
	}()
	return r
}

func (r *Rendezvous) brokers() []string {
	var out []string
	for _, l := range strings.Fields(r.app.cfg.Str("brokers")) {
		l = strings.TrimSpace(l)
		for _, p := range []string{"tcp://", "ssl://", "ws://", "wss://", "https://", "http://"} {
			if strings.HasPrefix(l, p) {
				out = append(out, l)
				break
			}
		}
	}
	if len(out) == 0 {
		return defaultBrokers
	}
	return out
}

// connect подключает ящик ко всем серверам параллельно. Ждет не всех: через
// 1,5 с после первого ответившего возвращается; опоздавшие подключатся в фоне -
// у друга мог ответить другой сервер. Возвращает, сколько готово к этому моменту.
func (r *Rendezvous) connect(b *rdvBox, onMsg func(topic string, payload []byte)) int {
	ready := make(chan struct{}, 16)
	onReady := func(t transport) {
		b.mu.Lock()
		subs, offer, closed := b.subs, b.offer, b.closed
		b.mu.Unlock()
		if closed {
			return
		}
		t.SetTopics(subs)
		if offer != nil {
			if err := t.Publish(b.topic+"/o", offer, true); err != nil {
				logf("Короткий код через %s: %v", t.Name(), err)
			}
		}
		select {
		case ready <- struct{}{}:
		default:
		}
	}
	b.mu.Lock()
	for _, url := range r.brokers() {
		b.ts = append(b.ts, dialTransport(url, rdvLifetime, onReady, onMsg))
	}
	b.mu.Unlock()
	deadline := time.After(10 * time.Second)
	select {
	case <-ready:
	case <-deadline:
		b.logFailures()
		return 0
	}
	time.Sleep(1500 * time.Millisecond)
	return b.readyCount()
}

func (b *rdvBox) readyCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, t := range b.ts {
		if t.Ready() {
			n++
		}
	}
	return n
}

func (b *rdvBox) logFailures() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, t := range b.ts {
		if !t.Ready() {
			logf("Короткий код: %s не отвечает", t.Name())
		}
	}
}

func (b *rdvBox) deliver(code string) {
	select {
	case b.got <- code:
	default:
	}
}

// close отключается от серверов; clear - стереть содержимое ящика.
func (b *rdvBox) close(clear bool) {
	b.once.Do(func() {
		b.mu.Lock()
		b.closed = true
		ts := b.ts
		b.mu.Unlock()
		for _, t := range ts {
			if clear {
				t.Clear(b.topic + "/o")
				t.Clear(b.topic + "/a")
			}
			t.Close()
		}
	})
}

func (r *Rendezvous) put(b *rdvBox) {
	r.mu.Lock()
	if old := r.boxes[b.short]; old != nil && old != b {
		go old.close(false)
	}
	r.boxes[b.short] = b
	r.mu.Unlock()
}

func (r *Rendezvous) box(short string) *rdvBox {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.boxes[normShort(short)]
}

const unreachable = "не удалось связаться ни с одним публичным сервером (возможно, их блокирует провайдер или VPN)"

// Offer кладет длинный код-предложение в ящик и ждет там ответ. Возвращает короткий код.
func (r *Rendezvous) Offer(acc *Account, to, longCode string) (string, int, error) {
	short := newShort()
	b := &rdvBox{short: short, topic: rdvTopic(short), friend: to, got: make(chan string, 1), created: time.Now()}
	payload, err := acc.rdvSeal(to, short, b.topic, longCode)
	if err != nil {
		return "", 0, err
	}
	b.subs, b.offer = []string{b.topic + "/a"}, payload
	n := r.connect(b, func(topic string, data []byte) {
		if topic != b.topic+"/a" {
			return
		}
		if text, from, ok := acc.rdvOpen(short, b.topic, data); ok && from == to {
			b.deliver(text)
		}
	})
	if n == 0 {
		b.close(false)
		return "", 0, errors.New(unreachable + " - отправьте другу длинный код")
	}
	r.put(b)
	return short, n, nil
}

// Fetch открывает ящик по короткому коду: длинный код-предложение и от кого он.
func (r *Rendezvous) Fetch(acc *Account, short string) (string, string, error) {
	short = normShort(short)
	if short == "" {
		return "", "", errors.New("код - 6 букв и цифр")
	}
	b := &rdvBox{short: short, topic: rdvTopic(short), got: make(chan string, 1), created: time.Now()}
	b.subs = []string{b.topic + "/o"}
	var fromMu sync.Mutex
	n := r.connect(b, func(topic string, data []byte) {
		if topic != b.topic+"/o" {
			return
		}
		if text, from, ok := acc.rdvOpen(short, b.topic, data); ok {
			fromMu.Lock()
			if b.friend == "" {
				b.friend = from
			}
			fromMu.Unlock()
			b.deliver(text)
		}
	})
	if n == 0 {
		b.close(false)
		return "", "", errors.New(unreachable + " - попросите у друга длинный код")
	}
	select {
	case code := <-b.got:
		fromMu.Lock()
		from := b.friend
		fromMu.Unlock()
		r.put(b)
		return code, from, nil
	case <-time.After(15 * time.Second):
		b.close(false)
		return "", "", errors.New("по этому коду ничего нет: проверьте код или попросите друга создать новый")
	}
}

// Answer кладет длинный код-ответ в ящик, который открыли через Fetch.
func (r *Rendezvous) Answer(acc *Account, short, longCode string) error {
	b := r.box(short)
	if b == nil || b.friend == "" {
		return errors.New("ящик для этого кода закрыт - введите код заново")
	}
	payload, err := acc.rdvSeal(b.friend, b.short, b.topic, longCode)
	if err != nil {
		return err
	}
	b.mu.Lock()
	ts := append([]transport(nil), b.ts...)
	b.mu.Unlock()
	sent := 0
	for _, t := range ts {
		if t.Ready() && t.Publish(b.topic+"/a", payload, true) == nil {
			sent++
		}
	}
	if sent == 0 {
		return errors.New("не удалось отправить ответ через публичные серверы - отправьте длинный код")
	}
	return nil
}

// Wait ждет ответ друга в ящике предложения не дольше timeout.
func (r *Rendezvous) Wait(short string, timeout time.Duration) (string, bool) {
	b := r.box(short)
	if b == nil {
		return "", false
	}
	select {
	case code := <-b.got:
		return code, true
	case <-time.After(timeout):
		return "", false
	}
}

// Close закрывает ящик и стирает его содержимое на серверах.
func (r *Rendezvous) Close(short string) {
	short = normShort(short)
	r.mu.Lock()
	b := r.boxes[short]
	delete(r.boxes, short)
	r.mu.Unlock()
	if b != nil {
		go b.close(true)
	}
}
