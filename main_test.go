package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zeno/internal/beta"
)

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		tag, cur string
		want     bool
	}{
		{"v0.1.1", "0.1.0", true},
		{"v1.0", "0.9.9", true},
		{"0.2.0", "0.1.10", true},
		{"v0.1.0", "0.1.0", false},
		{"v0.0.9", "0.1.0", false},
		{"garbage", "0.1.0", false},
		{"v0.2.0", "dev", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.tag, c.cur); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v", c.tag, c.cur, got)
		}
	}
}

// testBeta подменяет список беты на временный с двумя участниками.
func testBeta(t *testing.T) {
	t.Helper()
	_, master, _ := ed25519.GenerateKey(rand.Reader)
	f := &beta.File{Master: base64.RawStdEncoding.EncodeToString(master.Public().(ed25519.PublicKey))}
	for _, l := range []string{"ann", "bob"} {
		e, err := beta.Seal(master, l, "pw-"+l)
		if err != nil {
			t.Fatal(err)
		}
		f.Users = append(f.Users, e)
	}
	old := betaUsers
	betaUsers = f
	t.Cleanup(func() { betaUsers = old })
}

func testAccount(t *testing.T, login string) *Account {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	_, _ = rand.Read(seed)
	cert, err := betaUsers.Login(login, "pw-"+login)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	f := accountFile{V: 1, Login: login, Nick: strings.ToUpper(login), Cert: b64.EncodeToString(cert), Friends: []Friend{}}
	return newAccount(dir, filepath.Join(dir, "account.json"), f, seed)
}

func TestBetaLogin(t *testing.T) {
	testBeta(t)
	if _, err := betaUsers.Login("ANN ", "pw-ann"); err != nil {
		t.Fatalf("верный пароль не принят: %v", err)
	}
	if _, err := betaUsers.Login("ann", "pw-bob"); err == nil {
		t.Fatal("чужой пароль принят")
	}
	if _, err := betaUsers.Login("nobody", "x"); err == nil {
		t.Fatal("неизвестный логин принят")
	}
	betaUsers.Revoked = []string{"bob"}
	if _, err := betaUsers.Login("bob", "pw-bob"); err != beta.ErrRevoked {
		t.Fatalf("отозванный логин: %v", err)
	}
}

func TestFriendsAndInvites(t *testing.T) {
	testBeta(t)
	ann, bob := testAccount(t, "ann"), testAccount(t, "bob")
	if _, err := ann.AddFriend(ann.FriendCode()); err == nil {
		t.Fatal("свой код принят")
	}
	if _, err := ann.AddFriend(bob.FriendCode()); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.AddFriend(ann.FriendCode()); err != nil {
		t.Fatal(err)
	}
	sdp := "v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=fingerprint:sha-256 AA:BB\r\n"
	code, nonce, err := ann.MakeInvite("offer", bob.ID(), "", sdp)
	if err != nil {
		t.Fatal(err)
	}
	in, from, err := bob.OpenInvite(code)
	if err != nil {
		t.Fatal(err)
	}
	if in.SDP != sdp || in.N != nonce || from.ID != ann.ID() || in.T != "offer" {
		t.Fatalf("код разобран неверно: %+v", in)
	}
	if _, _, err := ann.OpenInvite(code); err == nil {
		t.Fatal("код, сделанный для Боба, открылся у Анны")
	}
	// подмена SDP ломает подпись
	in.SDP = strings.Replace(in.SDP, "AA:BB", "CC:DD", 1)
	if ed25519.Verify(ed25519.PublicKey(mustB64(t, ann.ID())), in.signed(), mustB64(t, in.Sig)) {
		t.Fatal("подпись приняла подмененный SDP")
	}
	if _, _, err := bob.OpenInvite(code[:len(code)-10]); err == nil {
		t.Fatal("обрезанный код принят")
	}
}

func mustB64(t *testing.T, s string) []byte {
	b, err := b64.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHistory(t *testing.T) {
	testBeta(t)
	ann := testAccount(t, "ann")
	for i, text := range []string{"привет", "как дела", "привет"} {
		m := Message{ID: string(rune('a' + i)), From: ann.ID(), TS: time.Now().UnixMilli(), Text: text}
		if err := ann.AppendHistory("friend", m); err != nil {
			t.Fatal(err)
		}
	}
	_ = ann.AppendHistory("friend", Message{ID: "a", Text: "повтор"})
	got := ann.LoadHistory("friend", 10)
	if len(got) != 3 || got[1].Text != "как дела" {
		t.Fatalf("история: %+v", got)
	}
	if len(ann.LoadHistory("other", 10)) != 0 {
		t.Fatal("чужая история не пустая")
	}
	if got := ann.LoadHistory("friend", 2); len(got) != 2 || got[0].Text != "как дела" {
		t.Fatalf("limit: %+v", got)
	}
}

func TestEmbeddedBetaUsers(t *testing.T) {
	f, err := beta.Parse(betaUsersJSON)
	if err != nil {
		t.Fatal(err)
	}
	if f.Master == "" || len(f.Users) == 0 {
		t.Fatal("в beta_users.json нет участников")
	}
	if strings.Contains(string(betaUsersJSON), "password") {
		t.Fatal("в beta_users.json что-то похожее на пароль")
	}
}
