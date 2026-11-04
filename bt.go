package main

// TarBitTorrent - встроенное приложение «передать большой файл другу»
// (internal/btshare): движок BitTorrent из TarBitTorrent, приватная раздача
// только для друга, связь напрямую или через соединение pion в Zeno.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"zeno/internal/btshare"
)

// reexecForBT: движок должен стартовать с обычным вводом-выводом файлов
// (btshare.FileIOEnv), иначе держит скачанное до выхода. Переменную нельзя
// задать изнутри процесса - перезапускаемся с ней, это доли секунды. Служба и
// установщик virtualZeno не перезапускаются: их запускает Windows.
func reexecForBT() bool {
	if os.Getenv(btshare.FileIOEnv) == "classic" {
		return false
	}
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "-lan-") || strings.HasPrefix(a, "--lan-") {
			return false
		}
	}
	exe := exePath()
	if exe == "" {
		return false
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), btshare.FileIOEnv+"=classic")
	return cmd.Start() == nil
}

var (
	btOnce sync.Once
	btEng  *btshare.Engine
)

// bt - движок передач; сам торрент-клиент запускается при первой передаче.
func (a *App) bt() *btshare.Engine {
	btOnce.Do(func() { btEng = btshare.New(filepath.Join(a.paths.Data, "btshare"), logf) })
	return btEng
}

func (a *App) btStop() {
	if btEng != nil {
		btEng.Stop()
	}
}

func btICE(v any) []btshare.ICEServer {
	var out []btshare.ICEServer
	for _, x := range parseICE(v) {
		out = append(out, btshare.ICEServer{URLs: x.URLs, Username: x.Username, Credential: x.Credential})
	}
	return out
}

func strList(v any) []string {
	var out []string
	if list, ok := v.([]any); ok {
		for _, x := range list {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func (a *App) btRoute(path string, body M) (any, error) {
	e := a.bt()
	friend, ih := str(body, "friend"), str(body, "ih")
	addrs := func() []string { return e.Addrs(a.stun.Info().Direct) }
	switch path {
	case "bt/pick":
		folder, _ := body["folder"].(bool)
		title := "Что отправить"
		if str(body, "for") == "save" {
			title = "Куда сохранить"
		}
		p, err := btshare.Pick(a.window.hwnd(), folder, title)
		return M{"path": p}, err
	case "bt/share":
		return e.Share(friend, str(body, "path"))
	case "bt/list":
		links := M{}
		for _, id := range strList(body["friends"]) {
			links[id] = e.LinkState(id)
		}
		return M{"items": e.List(friend), "links": links, "addrs": addrs()}, nil
	case "bt/get":
		size, _ := body["size"].(float64)
		if err := e.Get(friend, ih, str(body, "name"), int64(size), str(body, "dir")); err != nil {
			return nil, err
		}
		e.AddPeers(ih, strList(body["addrs"]))
		return M{"addrs": addrs()}, nil
	case "bt/peers":
		e.AddPeers(ih, strList(body["addrs"]))
		return nil, nil
	case "bt/offered":
		e.MarkOffered(ih)
		return nil, nil
	case "bt/remove":
		e.Remove(ih)
		return nil, nil
	case "bt/reveal":
		p := e.Location(ih)
		if p == "" || !fileExists(p) {
			return nil, errors.New("файл удален или перемещен")
		}
		cmd := exec.Command("explorer.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + p + `"`}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		go cmd.Wait()
		return nil, nil
	case "bt/link-offer":
		sdp, err := e.LinkOffer(friend, btICE(body["ice"]))
		return M{"sdp": sdp}, err
	case "bt/link-accept":
		sdp, err := e.LinkAccept(friend, str(body, "sdp"), btICE(body["ice"]))
		return M{"sdp": sdp}, err
	case "bt/link-answer":
		return nil, e.LinkAnswer(friend, str(body, "sdp"))
	}
	return nil, errNotFound
}
