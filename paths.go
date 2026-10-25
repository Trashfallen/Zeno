package main

// Где лежат данные программы. По умолчанию %APPDATA%\Zeno, для отладки
// (две копии на одном ПК) можно переопределить ZENO_HOME.

import (
	"os"
	"path/filepath"
)

type Paths struct {
	Data     string // корень
	Profiles string // профили: ключи, друзья, история
	Webview  string // профиль WebView2
	Logs     string
}

func newPaths() (Paths, error) {
	root := os.Getenv("ZENO_HOME")
	if root == "" {
		base := os.Getenv("APPDATA")
		if base == "" {
			base, _ = os.UserHomeDir()
		}
		root = filepath.Join(base, appName)
	}
	p := Paths{
		Data:     root,
		Profiles: filepath.Join(root, "profiles"),
		Webview:  filepath.Join(root, "webview"),
		Logs:     filepath.Join(root, "logs"),
	}
	for _, d := range []string{p.Data, p.Profiles, p.Webview, p.Logs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return p, err
		}
	}
	return p, nil
}

func (p Paths) file(name string) string { return filepath.Join(p.Data, name) }

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func exePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		return r
	}
	return exe
}

// writeAtomic пишет через временный файл, чтобы при сбое не остался обрезанный файл.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
