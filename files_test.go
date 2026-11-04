package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// Имя файла от друга не должно уводить запись из папки Zeno.
func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"фото.png":                        "фото.png",
		`..\..\Windows\evil.dll`:          "evil.dll",
		"../../etc/passwd":                "passwd",
		"a:b*c?.txt":                      "a_b_c_.txt",
		"   .":                            "файл",
		"":                                "файл",
		"con\x00trol.txt":                 "con_trol.txt",
		`C:\Users\x\Desktop\a.zip`:        "a.zip",
		strings.Repeat("я", 300) + ".mp4": strings.Repeat("я", 116) + ".mp4",
	}
	for in, want := range cases {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, нужно %q", in, got, want)
		}
		if got := safeName(in); filepath.Base(got) != got {
			t.Errorf("safeName(%q) = %q содержит путь", in, got)
		}
	}
}

func TestInFilesDir(t *testing.T) {
	dir, err := filesDir()
	if err != nil {
		t.Skip(err)
	}
	if _, err := inFilesDir(filepath.Join(dir, "a.png")); err != nil {
		t.Errorf("файл в папке Zeno отвергнут: %v", err)
	}
	for _, bad := range []string{dir, filepath.Join(dir, "..", "x.exe"), `C:\Windows\notepad.exe`} {
		if _, err := inFilesDir(bad); err == nil {
			t.Errorf("%s пропущен, а не должен", bad)
		}
	}
}
