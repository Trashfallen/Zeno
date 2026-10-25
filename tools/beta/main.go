// Утилита администратора закрытой беты: выдает и отзывает доступ.
//
//	go run ./tools/beta add <логин>...   новый участник, печатает пароль
//	go run ./tools/beta passwd <логин>   новый пароль (старый перестает работать в новых сборках)
//	go run ./tools/beta remove <логин>   отозвать доступ
//	go run ./tools/beta list
//
// Меняет beta_users.json в корне проекта (он вшивается в программу при сборке).
// Закрытый ключ администратора лежит вне репозитория: %APPDATA%\Zeno-admin\master.key
// (или ZENO_ADMIN_KEY). Без него новых участников не выпустить - сделайте копию.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"

	"zeno/internal/beta"
)

const usersFile = "beta_users.json"

func keyPath() string {
	if p := os.Getenv("ZENO_ADMIN_KEY"); p != "" {
		return p
	}
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "Zeno-admin", "master.key")
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("команды: add <логин>..., passwd <логин>, remove <логин>, list")
		os.Exit(2)
	}
	f, err := load()
	if err != nil {
		fail(err)
	}
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "list":
		for _, u := range f.Users {
			fmt.Println(u.Login)
		}
		for _, r := range f.Revoked {
			fmt.Println(r, "(отозван)")
		}
		return
	case "add", "passwd":
		master, err := masterKey(f)
		if err != nil {
			fail(err)
		}
		if len(args) == 0 {
			fail(errors.New("укажите логин"))
		}
		for _, login := range args {
			login = beta.NormLogin(login)
			i := slices.IndexFunc(f.Users, func(e beta.Entry) bool { return e.Login == login })
			if cmd == "add" && i >= 0 {
				fail(fmt.Errorf("%s уже есть, для нового пароля: passwd", login))
			}
			if cmd == "passwd" && i < 0 {
				fail(fmt.Errorf("%s нет в списке", login))
			}
			pw := password()
			e, err := beta.Seal(master, login, pw)
			if err != nil {
				fail(err)
			}
			if i >= 0 {
				f.Users[i] = e
			} else {
				f.Users = append(f.Users, e)
			}
			f.Revoked = slices.DeleteFunc(f.Revoked, func(s string) bool { return s == login })
			fmt.Printf("%-16s %s\n", login, pw)
		}
	case "remove":
		for _, login := range args {
			login = beta.NormLogin(login)
			f.Users = slices.DeleteFunc(f.Users, func(e beta.Entry) bool { return e.Login == login })
			if !slices.Contains(f.Revoked, login) {
				f.Revoked = append(f.Revoked, login)
			}
			fmt.Println("отозван:", login)
		}
	default:
		fail(errors.New("неизвестная команда " + cmd))
	}
	if err := save(f); err != nil {
		fail(err)
	}
	fmt.Println("beta_users.json обновлен - соберите и выпустите новую версию")
}

func load() (*beta.File, error) {
	b, err := os.ReadFile(usersFile)
	if errors.Is(err, os.ErrNotExist) {
		return &beta.File{}, nil
	}
	if err != nil {
		return nil, err
	}
	return beta.Parse(b)
}

func save(f *beta.File) error {
	if f.Users == nil {
		f.Users = []beta.Entry{}
	}
	if f.Revoked == nil {
		f.Revoked = []string{}
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(usersFile, append(b, '\n'), 0o644)
}

// masterKey читает закрытый ключ; при первом запуске создает его.
func masterKey(f *beta.File) (ed25519.PrivateKey, error) {
	p := keyPath()
	b, err := os.ReadFile(p)
	switch {
	case err == nil:
		seed, err := base64.RawStdEncoding.DecodeString(string(b))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, errors.New("ключ администратора поврежден: " + p)
		}
		k := ed25519.NewKeyFromSeed(seed)
		pub := base64.RawStdEncoding.EncodeToString(k.Public().(ed25519.PublicKey))
		if f.Master != "" && f.Master != pub {
			return nil, errors.New("ключ " + p + " не от этой беты")
		}
		f.Master = pub
		return k, nil
	case errors.Is(err, os.ErrNotExist) && f.Master == "":
		pub, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(base64.RawStdEncoding.EncodeToString(k.Seed())), 0o600); err != nil {
			return nil, err
		}
		f.Master = base64.RawStdEncoding.EncodeToString(pub)
		fmt.Println("создан ключ администратора:", p, "- сделайте копию")
		return k, nil
	case errors.Is(err, os.ErrNotExist):
		return nil, errors.New("нет ключа администратора " + p + " (бета уже создана другим ключом)")
	}
	return nil, err
}

// password: 12 случайных знаков без похожих (0/O, 1/l/I), группами по 4.
func password() string {
	const abc = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, 0, 14)
	for i := range 12 {
		if i > 0 && i%4 == 0 {
			out = append(out, '-')
		}
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(abc))))
		if err != nil {
			fail(err)
		}
		out = append(out, abc[n.Int64()])
	}
	return string(out)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ошибка:", err)
	os.Exit(1)
}
