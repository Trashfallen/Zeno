package main

// Звонок и звук сообщения играет сама программа через MCI Windows, а не окно:
// встроенный браузер в свернутом или спрятанном в трей окне может молчать.
// Файлы берутся из ui/ (вшиты в exe) и один раз выкладываются в папку данных.
// MCI держит устройства за потоком, поэтому все команды идут из одного потока.

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procMciSendStringW = windows.NewLazySystemDLL("winmm.dll").NewProc("mciSendStringW")

type Sounds struct {
	dir string
	cmd chan []string
}

func newSounds(dir string) *Sounds {
	s := &Sounds{dir: filepath.Join(dir, "sounds"), cmd: make(chan []string, 16)}
	_ = os.MkdirAll(s.dir, 0o755)
	for _, name := range []string{"ring.mp3", "message.mp3"} {
		if b, err := fs.ReadFile(uiFiles, "ui/"+name); err == nil {
			path := filepath.Join(s.dir, name)
			if fi, err := os.Stat(path); err != nil || fi.Size() != int64(len(b)) {
				_ = writeAtomic(path, b)
			}
		}
	}
	go s.loop()
	return s
}

func (s *Sounds) loop() {
	runtime.LockOSThread()
	for cmds := range s.cmd {
		for _, c := range cmds {
			p, _ := windows.UTF16PtrFromString(c)
			if r, _, _ := procMciSendStringW.Call(uintptr(unsafe.Pointer(p)), 0, 0, 0); r != 0 && !strings.HasPrefix(c, "close") {
				logf("Звук: %q -> ошибка MCI %d", c, r)
			}
		}
	}
}

func (s *Sounds) open(alias, name string) string {
	return `open "` + filepath.Join(s.dir, name) + `" type mpegvideo alias ` + alias
}

// Ring включает или выключает мелодию входящего звонка (по кругу).
func (s *Sounds) Ring(on bool) {
	if !on {
		s.cmd <- []string{"stop zring", "close zring"}
		return
	}
	s.cmd <- []string{"close zring", s.open("zring", "ring.mp3"), "setaudio zring volume to 850", "play zring repeat"}
}

// Message - короткий звук нового сообщения (первая секунда файла).
func (s *Sounds) Message() {
	s.cmd <- []string{"close zmsg", s.open("zmsg", "message.mp3"), "setaudio zmsg volume to 800", "play zmsg from 0 to 1000"}
}
