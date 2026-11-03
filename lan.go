package main

// virtualZeno - встроенное приложение «локальная сеть с другом» (как Hamachi):
// адаптер Wintun в службе (internal/vlan), адреса 10.77.x.y, пакеты идут
// отдельным соединением pion. Включается, только если запустили оба.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"zeno/internal/vlan"
)

// lanMain - запуски не для окна: служба и ее установка (с правами администратора).
// true - это был такой запуск, дальше ничего не делаем.
func lanMain(service, install string, uninstall bool, result string) bool {
	vlan.Version = appVersion
	var err error
	switch {
	case service != "":
		err = vlan.RunService(service)
	case install != "":
		err = vlan.Install(install)
	case uninstall:
		err = vlan.Uninstall()
	default:
		return false
	}
	if err != nil {
		if result != "" {
			_ = os.WriteFile(result, []byte(err.Error()), 0o644)
		}
		os.Exit(1)
	}
	return true
}

var (
	lanMu  sync.Mutex
	lanHub *vlan.Hub
	lanFor string // ID профиля, для которого создан хаб
)

// lan - хаб текущего профиля (адрес зависит от ключа).
func (a *App) lan() (*vlan.Hub, error) {
	acc := a.profiles.Current()
	if acc == nil {
		return nil, errors.New("сначала войдите")
	}
	lanMu.Lock()
	defer lanMu.Unlock()
	if lanHub == nil || lanFor != acc.ID() {
		if lanHub != nil {
			lanHub.Stop()
		}
		lanHub, lanFor = vlan.NewHub(acc.ID(), logf), acc.ID()
	}
	return lanHub, nil
}

func (a *App) lanStop() {
	lanMu.Lock()
	h := lanHub
	lanHub = nil
	lanMu.Unlock()
	if h != nil {
		h.Stop()
	}
}

func lanICE(v any) []vlan.ICEServer {
	var out []vlan.ICEServer
	for _, x := range parseICE(v) {
		out = append(out, vlan.ICEServer{URLs: x.URLs, Username: x.Username, Credential: x.Credential})
	}
	return out
}

// lanElevated запускает установщик с UAC и ждет его.
func (a *App) lanElevated(args string) error {
	res := filepath.Join(a.paths.Data, "lan-result.txt")
	_ = os.Remove(res)
	code, err := vlan.Elevated(a.window.hwnd(), args+` -lan-result "`+res+`"`, 2*time.Minute)
	if err != nil {
		return err
	}
	if code != 0 {
		b, _ := os.ReadFile(res)
		_ = os.Remove(res)
		if msg := strings.TrimSpace(string(b)); msg != "" {
			return errors.New(msg)
		}
		return fmt.Errorf("установщик завершился с кодом %d", code)
	}
	return nil
}

func (a *App) lanRoute(path string, body M) (any, error) {
	h, err := a.lan()
	if err != nil {
		return nil, err
	}
	friend := str(body, "friend")
	switch path {
	case "lan/status":
		var ids []string
		if list, ok := body["friends"].([]any); ok {
			for _, x := range list {
				if s, ok := x.(string); ok {
					ids = append(ids, s)
				}
			}
		}
		return h.Status(ids), nil
	case "lan/install":
		sid, err := vlan.CurrentUserSID()
		if err != nil {
			return nil, err
		}
		logf("virtualZeno: ставлю службу (UAC)")
		if err := a.lanElevated("-lan-install " + sid); err != nil {
			logf("virtualZeno: установка: %v", err)
			return nil, err
		}
		logf("virtualZeno: служба поставлена")
		return h.Status(nil), nil
	case "lan/uninstall":
		a.lanStop()
		logf("virtualZeno: убираю службу (UAC)")
		if err := a.lanElevated("-lan-uninstall"); err != nil {
			logf("virtualZeno: удаление: %v", err)
			return nil, err
		}
		return nil, nil
	case "lan/want":
		on, _ := body["on"].(bool)
		h.SetWant(on)
		return nil, nil
	case "lan/offer":
		sdp, err := h.Offer(friend, lanICE(body["ice"]))
		return M{"sdp": sdp}, err
	case "lan/accept":
		sdp, err := h.Accept(friend, str(body, "sdp"), lanICE(body["ice"]))
		return M{"sdp": sdp}, err
	case "lan/answer":
		return nil, h.Answer(friend, str(body, "sdp"))
	case "lan/close":
		h.Close(friend)
		return nil, nil
	}
	return nil, errNotFound
}
