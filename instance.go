package main

// Одна копия программы: вторая просит первую показать окно и выходит.
// Именованный мьютекс ловит одновременный запуск, instance.json хранит порт и
// токен окна.

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

type instanceInfo struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

var instanceMutex windows.Handle

// acquireInstance: true - мы первая копия. Замок привязан к папке данных: копии
// с разными данными (ZENO_HOME для отладки) друг другу не мешают.
func acquireInstance(p Paths) bool {
	sum := sha1.Sum([]byte(strings.ToLower(filepath.Clean(p.Data))))
	name, _ := windows.UTF16PtrFromString(`Local\` + appName + "-" + hex.EncodeToString(sum[:8]))
	h, err := windows.CreateMutex(nil, false, name)
	instanceMutex = h
	return err != windows.ERROR_ALREADY_EXISTS
}

func writeInstance(p Paths, port int, token string) {
	b, _ := json.Marshal(instanceInfo{Port: port, Token: token, PID: os.Getpid()})
	_ = writeAtomic(p.file("instance.json"), b)
}

func removeInstance(p Paths) { _ = os.Remove(p.file("instance.json")) }

// forwardToRunning просит запущенную копию показать окно; ждет до wait, пока она поднимется.
func forwardToRunning(p Paths, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		var info instanceInfo
		b, err := os.ReadFile(p.file("instance.json"))
		if err == nil {
			err = json.Unmarshal(b, &info)
		}
		if err == nil {
			allowForeground(info.PID) // чтобы окно первой копии могло выйти вперед
			req, _ := http.NewRequest("POST", "http://127.0.0.1:"+strconv.Itoa(info.Port)+"/api/show", bytes.NewReader([]byte("{}")))
			req.Header.Set("X-Token", info.Token)
			req.Header.Set("Content-Type", "application/json")
			if resp, err := client.Do(req); err == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					return true
				}
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(300 * time.Millisecond)
	}
}
