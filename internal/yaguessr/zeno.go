package yaguessr

// Встраивание в Zeno: сервер комнат живет в программе того, кто запустил игру.
// Входа по паролю нет - кто играет, говорит Zeno (Options.Ident): сам
// пользователь или друг, чьи сообщения страница Zeno передает по каналу
// данных. Свои карты и запас мест лежат в папке данных Zeno.

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"sync"
)

// Options - что игре нужно от Zeno.
type Options struct {
	Data  string                                     // папка для pool.json и maps.json
	Key   func() string                              // ключ Яндекс Карт; пусто - только панорамы Google
	Ident func(r *http.Request) (login, nick string) // кто пришел; пустой login - чужой
}

var (
	identFn   func(r *http.Request) (string, string)
	startOnce sync.Once
)

// Handler - вся игра: статика, API и /ws. Пути относительные, поэтому
// монтировать можно под любым префиксом (http.StripPrefix).
func Handler(o Options) http.Handler {
	startOnce.Do(func() {
		_ = os.MkdirAll(o.Data, 0o755)
		authMu.Lock()
		dataDir = o.Data
		authMu.Unlock()
		identFn = o.Ident
		loadPool()
		loadMaps()
		go janitor()
	})
	sub, _ := fs.Sub(webFS, "web")
	static := withETags(sub, http.FileServer(http.FS(sub)))

	mux := http.NewServeMux()
	mux.Handle("/api/me", requireAuth(http.HandlerFunc(handleMe)))
	mux.Handle("/api/maps", requireAuth(http.HandlerFunc(handleMaps)))
	mux.Handle("/api/gsv/find", requireAuth(http.HandlerFunc(handleGsvFind)))
	mux.Handle("/api/gsv/near", requireAuth(http.HandlerFunc(handleGsvNear)))
	mux.Handle("/api/metrics", requireAuth(http.HandlerFunc(handleMetrics)))
	mux.Handle("/api/ymaps-status", requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ymapsStatus(w, r, o.Key())
	})))
	mux.Handle("/config.js", requireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		keyJS, _ := json.Marshal(o.Key())
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write([]byte("window.YA_API_KEY = " + string(keyJS) + ";\nwindow.ZENO = true;\n"))
	})))
	mux.HandleFunc("/ws", serveWS)
	mux.Handle("/", requireAuth(static))
	return mux
}

// Rooms - сколько комнат сейчас открыто (для журнала Zeno).
func Rooms() int {
	mu.Lock()
	defer mu.Unlock()
	return len(rooms)
}
