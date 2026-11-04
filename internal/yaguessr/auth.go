package yaguessr

// Вход по логину и паролю. Аккаунты лежат в data/users.txt строками
// login:соль:хеш (PBKDF2-SHA256), добавляются командой `yaguessr adduser`.
// Ники и сессии хранятся рядом в JSON, чтобы переживать перезапуск.

import (
	"bufio"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	cookieName   = "yg_session"
	sessionTTL   = 30 * 24 * time.Hour
	pbkdfIter    = 200_000
	maxFails     = 5
	failLockTime = time.Minute
)

var loginSem = make(chan struct{}, 4) // одновременных проверок пароля

type userRec struct {
	salt []byte
	hash []byte
}

type session struct {
	Login   string    `json:"login"`
	Expires time.Time `json:"expires"`
}

var (
	authMu       sync.Mutex
	dataDir      string
	users        = map[string]userRec{}
	nicks        = map[string]string{}
	sessions     = map[string]session{}
	fails        = map[string]*failInfo{}
	secureCookie bool
)

type failInfo struct {
	n     int
	until time.Time
}

func hashPassword(pass string, salt []byte) []byte {
	h, _ := pbkdf2.Key(sha256.New, pass, salt, pbkdfIter, 32)
	return h
}

func readUsers(path string) (map[string]userRec, []string, error) {
	res := map[string]userRec{}
	var lines []string
	f, err := os.Open(path)
	if err != nil {
		return res, nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) != 3 {
			continue
		}
		salt, err1 := hex.DecodeString(parts[1])
		hash, err2 := hex.DecodeString(parts[2])
		if err1 != nil || err2 != nil {
			continue
		}
		res[parts[0]] = userRec{salt: salt, hash: hash}
		lines = append(lines, line)
	}
	return res, lines, sc.Err()
}

func loadAuth(dir string) error {
	dataDir = dir
	var err error
	users, _, err = readUsers(filepath.Join(dir, "users.txt"))
	if err != nil {
		return fmt.Errorf("нет аккаунтов: %v (добавь командой: yaguessr adduser -data %s логин пароль)", err, dir)
	}
	if len(users) == 0 {
		return fmt.Errorf("в %s/users.txt нет аккаунтов", dir)
	}
	readJSON("nicks.json", &nicks)
	readJSON("sessions.json", &sessions)
	now := time.Now()
	for t, s := range sessions {
		if now.After(s.Expires) || users[s.Login].hash == nil {
			delete(sessions, t)
		}
	}
	return nil
}

func readJSON(name string, v any) {
	b, err := os.ReadFile(filepath.Join(dataDir, name))
	if err == nil {
		json.Unmarshal(b, v)
	}
}

// writeJSON вызывается под authMu.
func writeJSON(name string, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	tmp := filepath.Join(dataDir, name+".tmp")
	if os.WriteFile(tmp, b, 0600) == nil {
		os.Rename(tmp, filepath.Join(dataDir, name))
	}
}

func nickFor(login string) string {
	authMu.Lock()
	defer authMu.Unlock()
	if n := nicks[login]; n != "" {
		return n
	}
	return login
}

func authLogin(r *http.Request) string {
	if identFn != nil {
		login, nick := identFn(r)
		if login != "" {
			authMu.Lock()
			nicks[login] = nick
			authMu.Unlock()
		}
		return login
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	authMu.Lock()
	defer authMu.Unlock()
	s, ok := sessions[c.Value]
	if !ok || time.Now().After(s.Expires) {
		return ""
	}
	return s.Login
}

func requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authLogin(r) == "" {
			if r.URL.Path == "/" || r.URL.Path == "/index.html" {
				to := "/login.html"
				if r.URL.RawQuery != "" {
					to += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, to, http.StatusFound)
			} else {
				http.Error(w, "нужен вход", http.StatusUnauthorized)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// за локальным nginx настоящий адрес приходит в X-Real-IP
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if real := r.Header.Get("X-Real-IP"); real != "" {
			return real
		}
	}
	return host
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		http.Error(w, "плохой запрос", http.StatusBadRequest)
		return
	}
	ip := clientIP(r)

	authMu.Lock()
	f := fails[ip]
	if f != nil && time.Now().Before(f.until) {
		authMu.Unlock()
		http.Error(w, "Слишком много попыток, подожди минуту", http.StatusTooManyRequests)
		return
	}
	// попытка засчитывается до хеширования: пачка параллельных запросов
	// не проскочит проверку, пока первые считают PBKDF2
	if f == nil {
		f = &failInfo{}
		fails[ip] = f
	}
	f.n++
	if f.n >= maxFails {
		f.n = 0
		f.until = time.Now().Add(failLockTime)
	}
	u, ok := users[strings.TrimSpace(req.Login)]
	authMu.Unlock()

	loginSem <- struct{}{} // PBKDF2 тяжелый: не больше нескольких сразу на весь сервер
	good := ok && subtle.ConstantTimeCompare(hashPassword(req.Password, u.salt), u.hash) == 1
	<-loginSem

	authMu.Lock()
	defer authMu.Unlock()
	if !good {
		http.Error(w, "Неверный логин или пароль", http.StatusUnauthorized)
		return
	}
	delete(fails, ip)
	token := randToken(32)
	login := strings.TrimSpace(req.Login)
	sessions[token] = session{Login: login, Expires: time.Now().Add(sessionTTL)}
	writeJSON("sessions.json", sessions)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/", HttpOnly: true,
		Secure: secureCookie, SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds()),
	})
	json.NewEncoder(w).Encode(map[string]any{"login": login, "nick": nicks[login]})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		authMu.Lock()
		delete(sessions, c.Value)
		writeJSON("sessions.json", sessions)
		authMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login.html", http.StatusFound)
}

func handleMe(w http.ResponseWriter, r *http.Request) {
	login := authLogin(r)
	authMu.Lock()
	nick := nicks[login]
	authMu.Unlock()
	json.NewEncoder(w).Encode(map[string]string{"login": login, "nick": nick})
}

func handleNick(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Nick string `json:"nick"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		http.Error(w, "плохой запрос", http.StatusBadRequest)
		return
	}
	nick := cleanName(req.Nick)
	login := authLogin(r)
	authMu.Lock()
	nicks[login] = nick
	writeJSON("nicks.json", nicks)
	authMu.Unlock()
	json.NewEncoder(w).Encode(map[string]string{"login": login, "nick": nick})
}

// yaguessr adduser [-data папка] логин пароль
func cmdAddUser(args []string) {
	dir := "data"
	if len(args) >= 2 && args[0] == "-data" {
		dir, args = args[1], args[2:]
	}
	if len(args) != 2 || args[0] == "" || strings.ContainsAny(args[0], ": ") {
		fmt.Println("использование: yaguessr adduser [-data папка] логин пароль")
		os.Exit(2)
	}
	login, pass := args[0], args[1]
	os.MkdirAll(dir, 0700)
	path := filepath.Join(dir, "users.txt")
	_, lines, _ := readUsers(path)
	var out []string
	for _, l := range lines {
		if !strings.HasPrefix(l, login+":") {
			out = append(out, l)
		}
	}
	salt := []byte(randToken(16))
	out = append(out, login+":"+hex.EncodeToString(salt)+":"+hex.EncodeToString(hashPassword(pass, salt)))
	if err := os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0600); err != nil {
		fmt.Println("ошибка:", err)
		os.Exit(1)
	}
	fmt.Println("аккаунт", login, "сохранен в", path)
}
