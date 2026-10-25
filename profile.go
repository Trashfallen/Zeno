package main

// Учетная запись на этом ПК: вход по логину и паролю закрытой беты, потом ник.
// После входа хранится сертификат участника и свой ключ (Ed25519), которым
// подписываются коды-приглашения. Ключ зашифрован DPAPI - на другом ПК или под
// другим пользователем Windows файл бесполезен. Повторный вход не нужен.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"zeno/internal/beta"
)

//go:embed beta_users.json
var betaUsersJSON []byte

var betaUsers = func() *beta.File {
	f, err := beta.Parse(betaUsersJSON)
	if err != nil {
		return &beta.File{}
	}
	return f
}()

var b64 = base64.RawURLEncoding

// Цвета аватарок, как в Discord: по ключу, у каждого свой.
var avatarColors = []string{"#5865f2", "#3ba55c", "#faa61a", "#ed4245", "#eb459e", "#9b59b6", "#1abc9c", "#e67e22"}

type Friend struct {
	ID    string `json:"id"` // публичный ключ
	Login string `json:"login"`
	Nick  string `json:"nick"`
	Color string `json:"color"`
	Added int64  `json:"added"`
}

type accountFile struct {
	V       int      `json:"v"`
	Login   string   `json:"login"`
	Nick    string   `json:"nick"`
	Color   string   `json:"color"`
	Cert    string   `json:"cert"`
	Seed    string   `json:"seed"` // DPAPI
	Friends []Friend `json:"friends"`
	Created int64    `json:"created"`
}

type Account struct {
	mu      sync.Mutex
	path    string
	dir     string
	f       accountFile
	priv    ed25519.PrivateKey
	histKey []byte
}

type Profiles struct {
	dir string
	mu  sync.Mutex
	acc *Account
}

func newProfiles(dir string) *Profiles {
	ps := &Profiles{dir: dir}
	if acc, err := loadAccount(dir); err == nil {
		ps.acc = acc
	} else if !errors.Is(err, os.ErrNotExist) {
		logf("Учетная запись: %v", err)
	}
	return ps
}

func loadAccount(dir string) (*Account, error) {
	path := filepath.Join(dir, "account.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f accountFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	sealed, err := base64.StdEncoding.DecodeString(f.Seed)
	if err != nil {
		return nil, err
	}
	seed, err := unprotect(sealed)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("ключ не расшифровался (другой ПК или пользователь Windows)")
	}
	cert, _ := b64.DecodeString(f.Cert)
	if !betaUsers.Verify(f.Login, cert) {
		return nil, errors.New("доступ к бете для " + f.Login + " отозван")
	}
	return newAccount(dir, path, f, seed), nil
}

func newAccount(dir, path string, f accountFile, seed []byte) *Account {
	h := sha256.Sum256(append([]byte("zeno-history|"), seed...))
	return &Account{path: path, dir: dir, f: f, priv: ed25519.NewKeyFromSeed(seed), histKey: h[:]}
}

// Current - вошедшая учетная запись или nil.
func (ps *Profiles) Current() *Account {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.acc
}

// Login проверяет логин и пароль беты. Новый ключ создается только при смене
// логина: при повторном входе тем же логином друзья остаются.
func (ps *Profiles) Login(login, password string) (*Account, error) {
	cert, err := betaUsers.Login(login, password)
	if err != nil {
		return nil, err
	}
	login = beta.NormLogin(login)
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.acc != nil && ps.acc.f.Login == login {
		return ps.acc, nil
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	sealed, err := protect(seed)
	if err != nil {
		return nil, err
	}
	f := accountFile{V: 1, Login: login, Cert: b64.EncodeToString(cert), Seed: base64.StdEncoding.EncodeToString(sealed),
		Friends: []Friend{}, Created: time.Now().Unix()}
	acc := newAccount(ps.dir, filepath.Join(ps.dir, "account.json"), f, seed)
	acc.f.Color = colorFor(acc.ID())
	if err := acc.save(); err != nil {
		return nil, err
	}
	ps.acc = acc
	logf("Вход: %s", login)
	return acc, nil
}

// Logout забывает учетную запись (ключ и друзей); история сообщений остается на диске.
func (ps *Profiles) Logout() error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.acc = nil
	err := os.Remove(filepath.Join(ps.dir, "account.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func colorFor(id string) string {
	h := sha256.Sum256([]byte(id))
	return avatarColors[int(h[0])%len(avatarColors)]
}

func (a *Account) ID() string { return b64.EncodeToString(a.priv.Public().(ed25519.PublicKey)) }

func (a *Account) save() error {
	b, err := json.MarshalIndent(a.f, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(a.path, b)
}

func (a *Account) Info() M {
	a.mu.Lock()
	defer a.mu.Unlock()
	return M{"id": a.ID(), "login": a.f.Login, "nick": a.f.Nick, "color": a.f.Color}
}

func cleanNick(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if n := utf8.RuneCountInString(s); n < 2 || n > 32 {
		return "", errors.New("ник - от 2 до 32 знаков")
	}
	return s, nil
}

func (a *Account) SetProfile(nick, color string) error {
	nick, err := cleanNick(nick)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.f.Nick = nick
	if slices.Contains(avatarColors, color) {
		a.f.Color = color
	}
	return a.save()
}

func (a *Account) Friends() []Friend {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.f.Friends)
}

func (a *Account) friend(id string) (Friend, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, f := range a.f.Friends {
		if f.ID == id {
			return f, true
		}
	}
	return Friend{}, false
}

// ---------- коды дружбы ----------

const friendPrefix = "ZENO-FRIEND:"

type friendCard struct {
	V     int    `json:"v"`
	ID    string `json:"id"`
	Login string `json:"l"`
	Nick  string `json:"n"`
	Color string `json:"c"`
	Cert  string `json:"cert"`
}

func (a *Account) FriendCode() string {
	a.mu.Lock()
	card := friendCard{V: 1, ID: a.ID(), Login: a.f.Login, Nick: a.f.Nick, Color: a.f.Color, Cert: a.f.Cert}
	a.mu.Unlock()
	b, _ := json.Marshal(card)
	return friendPrefix + b64.EncodeToString(b)
}

// AddFriend принимает код дружбы: только от участника беты и не свой.
func (a *Account) AddFriend(code string) (Friend, error) {
	code = strings.Join(strings.Fields(code), "")
	if !strings.HasPrefix(code, friendPrefix) {
		return Friend{}, errors.New("это не код дружбы Zeno")
	}
	b, err := b64.DecodeString(strings.TrimPrefix(code, friendPrefix))
	var card friendCard
	if err == nil {
		err = json.Unmarshal(b, &card)
	}
	if err != nil {
		return Friend{}, errors.New("код дружбы поврежден - скопируйте его целиком")
	}
	pub, err := b64.DecodeString(card.ID)
	cert, _ := b64.DecodeString(card.Cert)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return Friend{}, errors.New("код дружбы поврежден")
	}
	if !betaUsers.Verify(card.Login, cert) {
		return Friend{}, errors.New("код не от участника беты или его доступ отозван")
	}
	if card.ID == a.ID() {
		return Friend{}, errors.New("это ваш собственный код")
	}
	nick, err := cleanNick(card.Nick)
	if err != nil {
		nick = card.Login
	}
	color := card.Color
	if !slices.Contains(avatarColors, color) {
		color = colorFor(card.ID)
	}
	fr := Friend{ID: card.ID, Login: beta.NormLogin(card.Login), Nick: nick, Color: color, Added: time.Now().Unix()}
	a.mu.Lock()
	defer a.mu.Unlock()
	if i := slices.IndexFunc(a.f.Friends, func(f Friend) bool { return f.ID == fr.ID }); i >= 0 {
		fr.Added = a.f.Friends[i].Added
		a.f.Friends[i] = fr
	} else {
		a.f.Friends = append(a.f.Friends, fr)
	}
	return fr, a.save()
}

func (a *Account) RemoveFriend(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.f.Friends = slices.DeleteFunc(a.f.Friends, func(f Friend) bool { return f.ID == id })
	return a.save()
}

// UpdateFriend: друг сменил ник или цвет (приходит по каналу после соединения).
func (a *Account) UpdateFriend(id, nick, color string) error {
	nick, err := cleanNick(nick)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.f.Friends {
		if a.f.Friends[i].ID == id {
			a.f.Friends[i].Nick = nick
			if slices.Contains(avatarColors, color) {
				a.f.Friends[i].Color = color
			}
			return a.save()
		}
	}
	return errNotFound
}
