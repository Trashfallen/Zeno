package main

// История чата: у каждого своя копия на диске, по файлу на друга. Каждая строка
// зашифрована AES-GCM ключом, выведенным из ключа учетной записи.

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxMessage = 4000 // знаков

type Message struct {
	ID   string `json:"id"`
	From string `json:"from"` // ключ автора
	TS   int64  `json:"ts"`   // мс
	Text string `json:"text"`
}

func (a *Account) historyPath(friend string) string {
	h := sha256.Sum256([]byte(a.ID() + "|" + friend))
	return filepath.Join(a.dir, "history", hex.EncodeToString(h[:12])+".log")
}

func (a *Account) gcm() cipher.AEAD {
	block, _ := aes.NewCipher(a.histKey)
	g, _ := cipher.NewGCM(block)
	return g
}

func (a *Account) AppendHistory(friend string, m Message) error {
	if utf8.RuneCountInString(m.Text) > maxMessage {
		m.Text = string([]rune(m.Text)[:maxMessage])
	}
	js, err := json.Marshal(m)
	if err != nil {
		return err
	}
	g := a.gcm()
	nonce := make([]byte, g.NonceSize())
	_, _ = rand.Read(nonce)
	line := base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, js, []byte(friend))) + "\n"
	path := a.historyPath(friend)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}

// LoadHistory - последние limit сообщений, без повторов.
func (a *Account) LoadHistory(friend string, limit int) []Message {
	a.mu.Lock()
	f, err := os.Open(a.historyPath(friend))
	a.mu.Unlock()
	out := []Message{}
	if err != nil {
		return out
	}
	defer f.Close()
	g := a.gcm()
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sc.Text()))
		if err != nil || len(raw) < g.NonceSize() {
			continue
		}
		js, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], []byte(friend))
		if err != nil {
			continue
		}
		var m Message
		if json.Unmarshal(js, &m) != nil || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}
