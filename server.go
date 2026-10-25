package main

// Локальный HTTP-сервер для окна программы: отдает интерфейс (ui/, вшит в exe)
// и JSON API. Слушает только 127.0.0.1, все /api/* требуют токен (X-Token),
// чтобы сайты в браузере не могли управлять программой.
//
// Порт постоянный (запоминается в настройках): от адреса страницы браузер
// считает идентификаторы микрофонов и камер и разрешения, со случайным портом
// выбранное устройство забывалось бы при каждом запуске.

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed ui
var uiFiles embed.FS

const maxBody = 1 << 20

var errNotFound = errors.New("не найдено")

type Server struct {
	app   *App
	token string
	ln    net.Listener
	srv   *http.Server
}

func newServer(app *App) (*Server, error) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	ln, err := listen(app.cfg)
	if err != nil {
		return nil, err
	}
	s := &Server{app: app, token: hex.EncodeToString(b), ln: ln}
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	return s, nil
}

func listen(cfg *Config) (net.Listener, error) {
	if p := int(cfg.Num("port")); p > 0 {
		if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			return ln, nil
		}
		logf("Порт %d занят, беру другой", p)
	}
	for range 30 {
		p := 20000 + mrand.IntN(30000)
		if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			_ = cfg.Set(M{"port": float64(p)})
			return ln, nil
		}
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

func (s *Server) Port() int   { return s.ln.Addr().(*net.TCPAddr).Port }
func (s *Server) URL() string { return "http://127.0.0.1:" + strconv.Itoa(s.Port()) + "/#t=" + s.token }
func (s *Server) Start()      { go s.srv.Serve(s.ln) }
func (s *Server) Stop()       { _ = s.srv.Close() }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		s.api(w, r)
		return
	}
	s.static(w, r)
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	b, err := fs.ReadFile(uiFiles, "ui/"+name)
	if err != nil || !strings.Contains(name, ".") {
		http.NotFound(w, r)
		return
	}
	ct := map[string]string{
		".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
		".css": "text/css; charset=utf-8", ".png": "image/png", ".svg": "image/svg+xml", ".mp3": "audio/mpeg",
	}[name[strings.LastIndex(name, "."):]]
	if ct == "" {
		ct = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; "+
		"style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'")
	_, _ = w.Write(b)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		logf("JSON: %v", err)
		code, b = 500, []byte(`{"error":"внутренняя ошибка"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Token")), []byte(s.token)) != 1 {
		writeJSON(w, 403, M{"error": "нет доступа"})
		return
	}
	body := M{}
	if r.Method == http.MethodPost {
		b, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
		if err == nil && len(b) > 0 {
			err = json.Unmarshal(b, &body)
		}
		if err != nil {
			writeJSON(w, 400, M{"error": "кривой запрос: " + err.Error()})
			return
		}
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	res, err := s.route(path, body)
	switch {
	case errors.Is(err, errNotFound):
		writeJSON(w, 404, M{"error": "не найдено"})
	case err != nil:
		writeJSON(w, 400, M{"error": err.Error()})
	case res == nil:
		writeJSON(w, 200, M{"ok": true})
	default:
		writeJSON(w, 200, res)
	}
}

func str(m M, k string) string { v, _ := m[k].(string); return v }

func (s *Server) state() M {
	app := s.app
	st := M{"version": appVersion, "app": appName, "settings": app.cfg.All(), "autostart": autostartEnabled(),
		"update": app.updater.State(), "account": nil}
	if acc := app.profiles.Current(); acc != nil {
		st["account"] = acc.Info()
		st["friends"] = acc.Friends()
	}
	return st
}

func (s *Server) route(path string, body M) (any, error) {
	app := s.app
	switch path {
	case "state":
		return s.state(), nil
	case "login":
		if _, err := app.profiles.Login(str(body, "login"), str(body, "password")); err != nil {
			return nil, err
		}
		_ = app.cfg.Set(M{"last_login": str(body, "login")})
		return s.state(), nil
	case "logout":
		return nil, app.profiles.Logout()
	case "settings":
		if err := app.cfg.Set(body); err != nil {
			return nil, err
		}
		return app.cfg.All(), nil
	case "autostart":
		on, _ := body["on"].(bool)
		return M{"autostart": autostartEnabled()}, setAutostart(on)
	case "show":
		app.window.Show()
		return nil, nil
	case "attention":
		show, _ := body["show"].(bool)
		app.window.Attention(show)
		return nil, nil
	case "notify":
		app.notify(str(body, "title"), str(body, "text"))
		return nil, nil
	case "ptt":
		vk, _ := body["vk"].(float64)
		app.ptt.Set(int(vk))
		return nil, nil
	case "open-url":
		if u := str(body, "url"); strings.HasPrefix(u, "https://") {
			openURL(u)
		}
		return nil, nil
	case "update/check":
		app.updater.Check()
		return app.updater.State(), nil
	case "update/install":
		go app.installUpdate()
		return nil, nil
	case "quit":
		go app.Quit()
		return nil, nil
	}

	acc := app.profiles.Current()
	if acc == nil {
		return nil, errors.New("сначала войдите")
	}
	switch path {
	case "profile":
		if err := acc.SetProfile(str(body, "nick"), str(body, "color")); err != nil {
			return nil, err
		}
		return s.state(), nil
	case "friendcode":
		return M{"code": acc.FriendCode()}, nil
	case "friends/add":
		fr, err := acc.AddFriend(str(body, "code"))
		if err != nil {
			return nil, err
		}
		return M{"friend": fr, "friends": acc.Friends()}, nil
	case "friends/remove":
		return M{"friends": acc.Friends()}, acc.RemoveFriend(str(body, "id"))
	case "friends/update":
		if err := acc.UpdateFriend(str(body, "id"), str(body, "nick"), str(body, "color")); err != nil {
			return nil, err
		}
		return M{"friends": acc.Friends()}, nil
	case "invite/make":
		code, nonce, err := acc.MakeInvite(str(body, "type"), str(body, "to"), str(body, "nonce"), str(body, "sdp"))
		if err != nil {
			return nil, err
		}
		return M{"code": code, "nonce": nonce}, nil
	case "invite/open":
		in, fr, err := acc.OpenInvite(str(body, "code"))
		if err != nil {
			return nil, err
		}
		return M{"type": in.T, "nonce": in.N, "sdp": in.SDP, "friend": fr}, nil
	case "history":
		limit, _ := body["limit"].(float64)
		if limit <= 0 {
			limit = 300
		}
		return M{"messages": acc.LoadHistory(str(body, "friend"), int(limit))}, nil
	case "history/add":
		var m Message
		b, _ := json.Marshal(body["msg"])
		if err := json.Unmarshal(b, &m); err != nil || m.ID == "" {
			return nil, errors.New("кривое сообщение")
		}
		return nil, acc.AppendHistory(str(body, "friend"), m)
	}
	return nil, errNotFound
}
