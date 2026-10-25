package main

// Короткие коды соединения (6 знаков) без своего сервера.
//
// Длинный код (подписанный SDP) кладется в «почтовый ящик» на публичных
// MQTT-брокерах. Тема ящика выводится из короткого кода, содержимое
// зашифровано ключом из короткого кода и общего секрета двух друзей (X25519
// из их ключей Ed25519): брокер видит только шум, и даже подобрав код, без
// закрытого ключа одного из друзей ящик не открыть. Сообщения retained -
// второй может ввести код в любой момент в течение часа. После соединения
// ящик очищается. Брокеров несколько, работают параллельно, побеждает первый.

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
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	shortAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // без 0/O и 1/I
	shortLen      = 6
	rdvLifetime   = time.Hour
)

var defaultBrokers = []string{
	"tcp://broker.emqx.io:1883",
	"tcp://broker.hivemq.com:1883",
	"tcp://test.mosquitto.org:1883",
	"tcp://mqtt.eclipseprojects.io:1883",
}

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

// ---------- ящик на брокерах ----------

type rdvBox struct {
	short, topic, friend string
	mu                   sync.Mutex
	clients              []mqtt.Client
	closed               bool
	got                  chan string // расшифрованный длинный код от друга
	once                 sync.Once
	created              time.Time
}

// add запоминает подключенного брокера; ящик уже закрыт - отключает его.
func (b *rdvBox) add(c mqtt.Client) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		c.Disconnect(0)
		return
	}
	b.clients = append(b.clients, c)
}

func (b *rdvBox) list() []mqtt.Client {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]mqtt.Client(nil), b.clients...)
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
	for _, l := range strings.Split(r.app.cfg.Str("brokers"), "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "tcp://") || strings.HasPrefix(l, "ssl://") ||
			strings.HasPrefix(l, "ws://") || strings.HasPrefix(l, "wss://") {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return defaultBrokers
	}
	return out
}

// connect подключает ящик ко всем брокерам параллельно; onConnect зовется на
// каждом (и после переподключения). Ждет не всех: через 1,5 с после первого
// успешного возвращается, опоздавшие добавляются в ящик позже - у друга мог
// ответить другой брокер. Возвращает, сколько подключилось к этому моменту.
func (r *Rendezvous) connect(b *rdvBox, onConnect func(mqtt.Client)) int {
	brokers := r.brokers()
	done := make(chan bool, len(brokers))
	for _, broker := range brokers {
		go func() {
			id := make([]byte, 8)
			_, _ = rand.Read(id)
			opts := mqtt.NewClientOptions().AddBroker(broker).SetClientID("zeno-" + hex.EncodeToString(id)).
				SetConnectTimeout(7 * time.Second).SetKeepAlive(30 * time.Second).SetCleanSession(true).
				SetAutoReconnect(true).SetConnectRetry(false).SetOnConnectHandler(onConnect)
			c := mqtt.NewClient(opts)
			t := c.Connect()
			if !t.WaitTimeout(8*time.Second) || t.Error() != nil {
				logf("MQTT %s: не подключился: %v", broker, t.Error())
				c.Disconnect(0)
				done <- false
				return
			}
			b.add(c)
			done <- true
		}()
	}
	n := 0
	var grace <-chan time.Time
	for pending := len(brokers); pending > 0; pending-- {
		select {
		case ok := <-done:
			if ok {
				n++
				if grace == nil {
					grace = time.After(1500 * time.Millisecond)
				}
			}
		case <-grace:
			return n
		}
	}
	return n
}

func (b *rdvBox) deliver(code string) {
	select {
	case b.got <- code:
	default:
	}
}

// close отключается от брокеров; clear - стереть содержимое ящика.
func (b *rdvBox) close(clear bool) {
	b.once.Do(func() {
		b.mu.Lock()
		b.closed = true
		clients := b.clients
		b.mu.Unlock()
		for _, c := range clients {
			if clear {
				c.Publish(b.topic+"/o", 1, true, []byte{}).WaitTimeout(3 * time.Second)
				c.Publish(b.topic+"/a", 1, true, []byte{}).WaitTimeout(3 * time.Second)
			}
			c.Disconnect(500)
		}
	})
}

func (r *Rendezvous) put(b *rdvBox) {
	r.mu.Lock()
	if old := r.boxes[b.short]; old != nil {
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

// Offer кладет длинный код-предложение в ящик и ждет там ответ. Возвращает короткий код.
func (r *Rendezvous) Offer(acc *Account, to, longCode string) (string, int, error) {
	short := newShort()
	b := &rdvBox{short: short, topic: rdvTopic(short), friend: to, got: make(chan string, 1), created: time.Now()}
	payload, err := acc.rdvSeal(to, short, b.topic, longCode)
	if err != nil {
		return "", 0, err
	}
	n := r.connect(b, func(c mqtt.Client) {
		c.Subscribe(b.topic+"/a", 1, func(_ mqtt.Client, m mqtt.Message) {
			if text, from, ok := acc.rdvOpen(short, b.topic, m.Payload()); ok && from == to {
				b.deliver(text)
			}
		})
		c.Publish(b.topic+"/o", 1, true, payload)
	})
	if n == 0 {
		return "", 0, errors.New("публичные серверы для коротких кодов недоступны - отправьте другу длинный код")
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
	var fromMu sync.Mutex
	n := r.connect(b, func(c mqtt.Client) {
		c.Subscribe(b.topic+"/o", 1, func(_ mqtt.Client, m mqtt.Message) {
			if text, from, ok := acc.rdvOpen(short, b.topic, m.Payload()); ok {
				fromMu.Lock()
				if b.friend == "" {
					b.friend = from
				}
				fromMu.Unlock()
				b.deliver(text)
			}
		})
	})
	if n == 0 {
		return "", "", errors.New("публичные серверы для коротких кодов недоступны - попросите у друга длинный код")
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
	sent := 0
	for _, c := range b.list() {
		if t := c.Publish(b.topic+"/a", 1, true, payload); t.WaitTimeout(5*time.Second) && t.Error() == nil {
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

// Close закрывает ящик и стирает его содержимое на брокерах.
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
