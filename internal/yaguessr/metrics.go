package yaguessr

// Замеры загрузки от игроков (web/metrics.js): раз в раунд браузер присылает сводку
// по тайлам карт, пустой мини-карте и панораме, сервер пишет ее в журнал одной строкой.
// Смотреть: journalctl -u yaguessr | grep metrics

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

var (
	metricsMu   sync.Mutex
	metricsLast = map[string]time.Time{}
)

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
	if err != nil || !json.Valid(body) {
		http.Error(w, "плохой запрос", http.StatusBadRequest)
		return
	}
	login := authLogin(r)
	// не чаще раза в 2 с от игрока, чтобы журнал не забивался
	metricsMu.Lock()
	ok := time.Since(metricsLast[login]) > 2*time.Second
	if ok {
		metricsLast[login] = time.Now()
	}
	metricsMu.Unlock()
	if ok {
		var compact bytes.Buffer
		json.Compact(&compact, body)
		log.Printf("metrics %s %s", login, compact.String())
	}
	w.WriteHeader(http.StatusNoContent)
}
