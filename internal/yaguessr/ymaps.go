package yaguessr

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// ymapsStatus объясняет, почему у браузера не загрузился API Яндекс Карт.
// Браузер текст ошибки загрузчика прочитать не может (CORS), а сервер может.
// Вызывается только после неудачной загрузки, поэтому лишних запросов к лимиту почти нет.
func ymapsStatus(w http.ResponseWriter, r *http.Request, key string) {
	reason := "unknown"
	req, _ := http.NewRequest(http.MethodGet, "https://api-maps.yandex.ru/2.1/?apikey="+key+"&lang=ru_RU", nil)
	// ключ ограничен по Referer, поэтому представляемся страницей игры
	req.Header.Set("Referer", "https://"+r.Host+"/")
	client := &http.Client{Timeout: 8 * time.Second}
	if res, err := client.Do(req); err == nil {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		res.Body.Close()
		text := string(body)
		switch {
		case strings.Contains(text, "Limit is exceeded") || res.StatusCode == http.StatusTooManyRequests:
			reason = "limit"
		case strings.Contains(text, "Invalid api key") || strings.Contains(text, `"apikeyValid":false`):
			reason = "key"
		case res.StatusCode == http.StatusOK:
			reason = "ok"
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"reason": reason})
}
