package main

// Zeno - голос, видео, трансляция экрана и чат на двоих напрямую между ПК (WebRTC).
// Свое окно на WebView2, значок в трее, самообновление из релизов GitHub.
// Сервера нет: звук, видео и сообщения идут P2P, первое соединение ставится
// обменом подписанными кодами-приглашениями.

import (
	_ "embed"
	"flag"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/systray"
)

const appName = "Zeno"

// appVersion задается при сборке: -ldflags "-X main.appVersion=0.1.0"
var appVersion = "dev"

//go:embed assets/icon.ico
var trayIcon []byte

// Флаги движка браузера внутри окна: звонок не должен замирать, когда окно
// свернуто или спрятано в трей, а звук собеседника - ждать клика по странице.
const browserArgs = "--autoplay-policy=no-user-gesture-required " +
	"--disable-background-timer-throttling --disable-renderer-backgrounding " +
	"--disable-backgrounding-occluded-windows"

var logger *log.Logger

func logf(format string, args ...any) {
	if logger != nil {
		logger.Printf(format, args...)
	}
}

func setupLogging(p Paths) {
	name := filepath.Join(p.Logs, "zeno.log")
	if fi, err := os.Stat(name); err == nil && fi.Size() > 2<<20 {
		_ = os.Rename(name, name+".1")
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	logger = log.New(f, "", log.LstdFlags)
}

type App struct {
	paths     Paths
	cfg       *Config
	profiles  *Profiles
	server    *Server
	window    *Window
	updater   *Updater
	ptt       *PTT
	rdv       *Rendezvous
	auto      *Auto
	sounds    *Sounds
	stun      *StunPicker
	quitCh    chan struct{}
	quitOnce  sync.Once
	trayReady atomic.Bool
}

func main() {
	minimized := flag.Bool("minimized", false, "запуститься в трее без окна")
	afterUpdate := flag.Int("after-update", 0, "PID старой копии после обновления")
	flag.Parse()

	// первый запуск из архива: копируемся в свою папку, ставим ярлыки и запускаемся оттуда
	if *afterUpdate == 0 && installIfNeeded() {
		return
	}
	paths, err := newPaths()
	if err != nil {
		messageBox(appName, "Не удалось создать папку данных: "+err.Error())
		os.Exit(1)
	}
	if *afterUpdate > 0 {
		waitForExit(*afterUpdate, 30*time.Second)
		cleanupOldExe()
	}
	if !acquireInstance(paths) {
		if !forwardToRunning(paths, 30*time.Second) {
			messageBox(appName, appName+" уже запущен, но не отвечает. Закройте его через значок в трее или в диспетчере задач и запустите снова.")
		}
		return
	}
	setupLogging(paths)
	logf("%s %s запускается", appName, appVersion)
	os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", browserArgs)

	app := &App{paths: paths, quitCh: make(chan struct{})}
	app.cfg = loadConfig(paths.file("settings.json"))
	app.profiles = newProfiles(paths.Profiles)
	app.updater = newUpdater()
	app.window = newWindow(app)
	app.ptt = newPTT(app)
	app.rdv = newRendezvous(app)
	app.auto = newAuto(app)
	app.sounds = newSounds(paths.Data)
	app.stun = newStunPicker(app.quitCh)
	app.server, err = newServer(app)
	if err != nil {
		messageBox(appName, "Не удалось открыть локальный порт для окна: "+err.Error())
		os.Exit(1)
	}
	app.server.Start()
	writeInstance(paths, app.server.Port(), app.server.token)
	if exe := exePath(); appVersion != "dev" && exe != "" && os.Getenv("ZENO_HOME") == "" {
		go ensureStartMenu(exe)
	}
	app.updater.startBackground(func() bool { return app.cfg.Bool("check_updates") })
	go app.ptt.run()
	if acc := app.profiles.Current(); acc != nil {
		app.auto.Start(acc) // друзья найдут нас, даже пока окно спрятано
	}

	// окно создается всегда: в нем живет WebRTC. Из автозапуска - сразу спрятанным в трей
	app.window.Open(*minimized)
	app.runTray() // до выхода
	app.shutdown()
}

func (a *App) notify(title, text string) {
	if a.trayReady.Load() {
		notifyBalloon(title, text)
	}
}

// Quit закрывает окно и выходит из цикла трея; остальное доделывает shutdown.
func (a *App) Quit() {
	a.quitOnce.Do(func() {
		logf("Выход")
		close(a.quitCh)
		a.window.Close()
		if a.trayReady.Load() {
			systray.Quit()
		}
	})
}

func (a *App) shutdown() {
	a.sounds.Ring(false)
	a.auto.Stop()
	removeInstance(a.paths)
	a.server.Stop()
}
