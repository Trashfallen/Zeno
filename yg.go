package main

// ЯГессер - встроенное приложение «угадай место» (internal/yaguessr). Сервер
// комнат живет в Zeno того, кто запустил игру; друг играет через него: его
// окно игры шлет сообщения странице своего Zeno, та - по каналу данных
// странице хоста, а страница хоста держит за друга WebSocket к /yg/ws?as=<ID>.
// Свой сервер не нужен.

import (
	"crypto/subtle"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"zeno/internal/yaguessr"
)

const ygCookie = "zeno_yg"

var (
	ygOnce    sync.Once
	ygHandler http.Handler
)

// ygServe отдает /yg/...: страницы игры, ее API и WebSocket.
func (s *Server) ygServe(w http.ResponseWriter, r *http.Request) {
	ygOnce.Do(func() {
		h := yaguessr.Handler(yaguessr.Options{
			Data:  filepath.Join(s.app.paths.Data, "yaguessr"),
			Key:   func() string { return "" }, // ключ Яндекса игра спрашивает сама и хранит у себя
			Ident: s.ygIdent,
		})
		ygHandler = http.StripPrefix("/yg", h)
	})
	if r.URL.Path == "/yg" {
		http.Redirect(w, r, "/yg/", http.StatusFound)
		return
	}
	ygHandler.ServeHTTP(w, r)
}

// ygIdent: кто пришел в игру. Пускаем только с печеньем, которое страница
// Zeno получила через API (там нужен токен окна). ?as=<ID друга> - страница
// хоста пришла за друга.
func (s *Server) ygIdent(r *http.Request) (string, string) {
	c, err := r.Cookie(ygCookie)
	if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) != 1 {
		return "", ""
	}
	acc := s.app.profiles.Current()
	if acc == nil {
		return "", ""
	}
	if as := r.URL.Query().Get("as"); as != "" {
		for _, f := range acc.Friends() {
			if f.ID == as {
				return "z:" + f.ID, f.Nick
			}
		}
		return "", ""
	}
	nick, _ := acc.Info()["nick"].(string)
	return "z:" + acc.ID(), nick
}

// ygCookieSet - ответ на /api/yg/open: печенье для окна игры.
func (s *Server) ygCookieSet(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: ygCookie, Value: s.token, Path: "/yg", HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func isYG(path string) bool { return path == "/yg" || strings.HasPrefix(path, "/yg/") }
