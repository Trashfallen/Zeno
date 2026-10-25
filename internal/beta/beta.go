// Package beta - доступ к закрытой бете по логину и паролю без сервера.
//
// Для каждого участника администратор подписывает своим закрытым ключом
// сертификат «этот логин в бете» и кладет его в программу зашифрованным ключом,
// выведенным из пароля (Argon2id). В репозитории и в exe лежат только соль и
// шифротекст: пароля там нет, подобрать его перебором при случайном пароле из
// 12 знаков нереально. Публичный ключ администратора тоже в программе - по нему
// пиры проверяют сертификаты друг друга, подделать их без закрытого ключа нельзя.
package beta

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"golang.org/x/crypto/argon2"
)

// File - содержимое beta_users.json.
type File struct {
	Master  string   `json:"master"`  // публичный ключ администратора, base64
	Users   []Entry  `json:"users"`   //
	Revoked []string `json:"revoked"` // отозванные логины: их сертификаты больше не принимаются
}

type Entry struct {
	Login string `json:"login"`
	Salt  string `json:"salt"`
	Nonce string `json:"nonce"`
	Box   string `json:"box"` // AES-GCM(ключ из пароля, сертификат)
}

var (
	ErrUnknown = errors.New("неверный логин или пароль")
	ErrRevoked = errors.New("доступ для этого логина отозван")
	b64        = base64.RawStdEncoding
)

// NormLogin: логины без учета регистра и пробелов по краям.
func NormLogin(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func message(login string) []byte { return []byte("zeno-beta-v1|" + NormLogin(login)) }

func kek(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
}

func Parse(b []byte) (*File, error) {
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

func (f *File) masterKey() ed25519.PublicKey {
	k, err := b64.DecodeString(f.Master)
	if err != nil || len(k) != ed25519.PublicKeySize {
		return nil
	}
	return k
}

// Login проверяет пароль и возвращает сертификат участника.
func (f *File) Login(login, password string) ([]byte, error) {
	login = NormLogin(login)
	if slices.Contains(f.Revoked, login) {
		return nil, ErrRevoked
	}
	for _, e := range f.Users {
		if e.Login != login {
			continue
		}
		salt, err1 := b64.DecodeString(e.Salt)
		nonce, err2 := b64.DecodeString(e.Nonce)
		box, err3 := b64.DecodeString(e.Box)
		if err := errors.Join(err1, err2, err3); err != nil {
			return nil, ErrUnknown
		}
		gcm, err := newGCM(kek(password, salt))
		if err != nil {
			return nil, err
		}
		cert, err := gcm.Open(nil, nonce, box, []byte(login))
		if err != nil || !f.Verify(login, cert) {
			return nil, ErrUnknown
		}
		return cert, nil
	}
	// время ответа не должно выдавать, есть ли такой логин
	kek(password, make([]byte, 16))
	return nil, ErrUnknown
}

// Verify: сертификат подписан администратором и логин не отозван.
func (f *File) Verify(login string, cert []byte) bool {
	k := f.masterKey()
	if k == nil || len(cert) != ed25519.SignatureSize || slices.Contains(f.Revoked, NormLogin(login)) {
		return false
	}
	return ed25519.Verify(k, message(login), cert)
}

// Seal выпускает запись участника (для утилиты администратора).
func Seal(master ed25519.PrivateKey, login, password string) (Entry, error) {
	login = NormLogin(login)
	cert := ed25519.Sign(master, message(login))
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return Entry{}, err
	}
	gcm, err := newGCM(kek(password, salt))
	if err != nil {
		return Entry{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Entry{}, err
	}
	box := gcm.Seal(nil, nonce, cert, []byte(login))
	return Entry{Login: login, Salt: b64.EncodeToString(salt), Nonce: b64.EncodeToString(nonce), Box: b64.EncodeToString(box)}, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
