package main

// Транспорты для коротких кодов и автосоединения: публичные серверы, через
// которые ходят зашифрованные сообщения. Разные пути на случай блокировок:
//
//	tcp://, ssl://         MQTT (порты 1883 / 8883) - самый быстрый
//	ws://, wss://          MQTT через WebSocket (для сети выглядит как HTTPS)
//	https://               ntfy (обычный HTTPS на 443, сообщения кэшируются на сервере)
//
// Все работают параллельно, сообщение уходит во все, принимается первое.

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

var defaultBrokers = []string{
	"tcp://broker.emqx.io:1883",
	"wss://broker.emqx.io:8084/mqtt",
	"tcp://broker.hivemq.com:1883",
	"wss://broker.hivemq.com:8884/mqtt",
	"ssl://broker.emqx.io:8883",
	"https://ntfy.sh",
}

type transport interface {
	Name() string
	Ready() bool
	SetTopics(topics []string) // подписка ровно на эти темы; старые сообщения (retained, кэш) тоже приходят
	Publish(topic string, payload []byte, retain bool) error
	Clear(topic string) // стереть сохраненное сообщение (где это возможно)
	Close()
}

// dialTransport запускает подключение. onReady зовется при каждом (пере)подключении,
// onMsg - на каждое сообщение. history - за сколько назад забирать кэш (ntfy).
func dialTransport(url string, history time.Duration, onReady func(transport), onMsg func(topic string, payload []byte)) transport {
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		n := &ntfyT{base: strings.TrimRight(url, "/"), history: history, onReady: onReady, onMsg: onMsg,
			wake: make(chan struct{}, 1), names: map[string]string{}}
		go n.loop()
		return n
	}
	m := &mqttT{url: url, onMsg: onMsg, topics: map[string]bool{}}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	opts := mqtt.NewClientOptions().AddBroker(url).SetClientID("zeno-" + hex.EncodeToString(id)).
		SetConnectTimeout(8 * time.Second).SetKeepAlive(60 * time.Second).SetCleanSession(true).
		SetAutoReconnect(true).SetMaxReconnectInterval(2 * time.Minute).
		SetConnectRetry(true).SetConnectRetryInterval(30 * time.Second).
		SetOnConnectHandler(func(c mqtt.Client) {
			m.resubscribe()
			if onReady != nil {
				onReady(m)
			}
		})
	m.c = mqtt.NewClient(opts)
	m.c.Connect()
	return m
}

// ---------- MQTT ----------

type mqttT struct {
	url    string
	c      mqtt.Client
	mu     sync.Mutex
	topics map[string]bool
	onMsg  func(string, []byte)
}

func (m *mqttT) Name() string { return m.url }
func (m *mqttT) Ready() bool  { return m.c.IsConnectionOpen() }

func (m *mqttT) handler(_ mqtt.Client, msg mqtt.Message) {
	if len(msg.Payload()) > 0 {
		m.onMsg(msg.Topic(), msg.Payload())
	}
}

func (m *mqttT) resubscribe() {
	m.mu.Lock()
	want := map[string]byte{}
	for t := range m.topics {
		want[t] = 1
	}
	m.mu.Unlock()
	if len(want) > 0 {
		m.c.SubscribeMultiple(want, m.handler)
	}
}

func (m *mqttT) SetTopics(topics []string) {
	m.mu.Lock()
	want := map[string]bool{}
	var add, del []string
	for _, t := range topics {
		want[t] = true
		if !m.topics[t] {
			add = append(add, t)
		}
	}
	for t := range m.topics {
		if !want[t] {
			del = append(del, t)
		}
	}
	m.topics = want
	m.mu.Unlock()
	if !m.Ready() {
		return // подпишемся при подключении
	}
	for _, t := range add {
		m.c.Subscribe(t, 1, m.handler)
	}
	if len(del) > 0 {
		m.c.Unsubscribe(del...)
	}
}

func (m *mqttT) Publish(topic string, payload []byte, retain bool) error {
	if !m.Ready() {
		return fmt.Errorf("%s: нет связи", m.url)
	}
	t := m.c.Publish(topic, 1, retain, payload)
	if !t.WaitTimeout(6 * time.Second) {
		return fmt.Errorf("%s: нет ответа", m.url)
	}
	return t.Error()
}

func (m *mqttT) Clear(topic string) {
	if m.Ready() {
		m.c.Publish(topic, 1, true, []byte{}).WaitTimeout(3 * time.Second)
	}
}

func (m *mqttT) Close() { m.c.Disconnect(250) }

// ---------- ntfy (HTTPS) ----------

type ntfyT struct {
	base    string
	history time.Duration
	onReady func(transport)
	onMsg   func(string, []byte)
	wake    chan struct{}
	ready   atomic.Bool
	closed  atomic.Bool
	mu      sync.Mutex
	names   map[string]string // имя темы ntfy -> наша тема
	since   string
	cancel  context.CancelFunc
}

var ntfyClient = &http.Client{Timeout: 0}

// ntfyName: в ntfy в имени темы только буквы, цифры, _ и -, до 64 знаков.
func ntfyName(topic string) string {
	h := sha256.Sum256([]byte("zeno-ntfy|" + topic))
	return "zeno_" + hex.EncodeToString(h[:20])
}

func (n *ntfyT) Name() string { return n.base }
func (n *ntfyT) Ready() bool  { return n.ready.Load() }

func (n *ntfyT) SetTopics(topics []string) {
	n.mu.Lock()
	names := map[string]string{}
	for _, t := range topics {
		names[ntfyName(t)] = t
	}
	n.names = names
	if n.cancel != nil {
		n.cancel() // поток переподключится с новым списком
	}
	n.mu.Unlock()
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

func (n *ntfyT) loop() {
	backoff := 5 * time.Second
	for !n.closed.Load() {
		n.mu.Lock()
		var list []string
		for name := range n.names {
			list = append(list, name)
		}
		since := n.since
		if since == "" {
			since = fmt.Sprint(time.Now().Add(-n.history).Unix())
		}
		ctx, cancel := context.WithCancel(context.Background())
		n.cancel = cancel
		n.mu.Unlock()

		var err error
		if len(list) == 0 {
			err = n.health(ctx)
			if err == nil {
				select { // темы появятся - разбудят
				case <-n.wake:
				case <-time.After(5 * time.Minute):
				}
			}
		} else {
			err = n.stream(ctx, strings.Join(list, ","), since)
		}
		// отменили снаружи (сменились темы) - до нашего cancel(), иначе ctx.Err() всегда не nil
		resubscribe := ctx.Err() != nil
		cancel()
		if n.closed.Load() {
			return
		}
		if err != nil && !resubscribe {
			n.ready.Store(false)
			logf("ntfy %s: %v", n.base, err)
			select {
			case <-n.wake:
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 2*time.Minute)
			continue
		}
		backoff = 5 * time.Second
	}
}

func (n *ntfyT) markReady() {
	if !n.ready.Swap(true) && n.onReady != nil {
		go n.onReady(n)
	}
}

func (n *ntfyT) health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", n.base+"/v1/health", nil)
	resp, err := ntfyClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("ответ %s", resp.Status)
	}
	n.markReady()
	return nil
}

func (n *ntfyT) stream(ctx context.Context, topics, since string) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", n.base+"/"+topics+"/json?since="+since, nil)
	resp, err := ntfyClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("ответ %s", resp.Status)
	}
	n.markReady()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var ev struct {
			ID      string `json:"id"`
			Event   string `json:"event"`
			Topic   string `json:"topic"`
			Message string `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Event != "message" {
			continue
		}
		n.mu.Lock()
		topic, ok := n.names[ev.Topic]
		n.since = ev.ID // после обрыва продолжим с этого места
		n.mu.Unlock()
		if payload, err := base64.StdEncoding.DecodeString(ev.Message); ok && err == nil && len(payload) > 0 {
			n.onMsg(topic, payload)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return fmt.Errorf("сервер закрыл поток")
}

func (n *ntfyT) Publish(topic string, payload []byte, _ bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", n.base+"/"+ntfyName(topic), strings.NewReader(base64.StdEncoding.EncodeToString(payload)))
	resp, err := ntfyClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: ответ %s", n.base, resp.Status)
	}
	n.markReady()
	return nil
}

func (n *ntfyT) Clear(string) {} // кэш ntfy живет 12 ч, содержимое зашифровано

func (n *ntfyT) Close() {
	n.closed.Store(true)
	n.mu.Lock()
	if n.cancel != nil {
		n.cancel()
	}
	n.mu.Unlock()
	select {
	case n.wake <- struct{}{}:
	default:
	}
}
