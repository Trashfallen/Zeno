package btshare

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestMain перезапускает тесты с обычным вводом-выводом файлов, как Zeno.
func TestMain(m *testing.M) {
	if os.Getenv(FileIOEnv) != "classic" {
		cmd := exec.Command(os.Args[0], os.Args[1:]...)
		cmd.Env = append(os.Environ(), FileIOEnv+"=classic")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		err := cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestTransfer: два движка в одном процессе, файл идет только через
// соединение pion (прямых адресов не даем), содержимое совпадает.
func TestTransfer(t *testing.T) {
	size := 64 << 20
	if os.Getenv("BT_BIG") != "" {
		size = 1 << 30
	}
	src := t.TempDir()
	data := make([]byte, size)
	rand.Read(data)
	path := filepath.Join(src, "большой файл.bin")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	a := New(filepath.Join(t.TempDir(), "a"), t.Logf)
	b := New(filepath.Join(t.TempDir(), "b"), t.Logf)
	defer a.Stop()
	defer b.Stop()

	// соединение pion между ними (в Zeno SDP передают страницы)
	offer, err := a.LinkOffer("b", nil)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := b.LinkAccept("a", offer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.LinkAnswer("b", answer); err != nil {
		t.Fatal(err)
	}

	st, err := a.Share("b", path)
	if err != nil {
		t.Fatal(err)
	}
	var ih string
	for deadline := time.Now().Add(time.Minute); ; {
		list := a.List("b")
		if len(list) == 1 && list[0].State == "seeding" {
			ih = list[0].IH
			break
		}
		if len(list) == 1 && list[0].State == "error" {
			t.Fatal(list[0].Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("не прохешировалось: %+v", list)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("раздача %s, %q, %d байт", ih, st.Name, st.Size)

	dst := t.TempDir()
	if err := b.Get("a", ih, st.Name, st.Size, dst); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for deadline := time.Now().Add(5 * time.Minute); ; {
		s := b.List("a")[0]
		if s.State == "done" {
			if s.Via != "zeno" && s.Via != "" {
				t.Fatalf("шло не через Zeno: %s", s.Via)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("не скачалось: %+v", s)
		}
		time.Sleep(200 * time.Millisecond)
	}
	el := time.Since(start)
	t.Logf("скачано за %v, %.0f Мбит/с", el.Round(time.Millisecond), float64(size)*8/el.Seconds()/1e6)
	// .part переименовывается в итоговое имя чуть позже последнего куска
	final := filepath.Join(dst, "большой файл.bin")
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if _, err := os.Stat(final); err == nil || time.Now().After(deadline) {
			break
		}
	}
	b.Stop() // файл закрыт движком - можно читать
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("содержимое не совпало")
	}
}
