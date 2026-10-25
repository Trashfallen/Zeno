package main

// Коды-приглашения: так два ПК обмениваются параметрами соединения WebRTC (SDP)
// без сервера. Первый делает код «предложение», второй вставляет его и получает
// код «ответ», первый вставляет ответ - соединение установлено.
//
// Код подписан ключом отправителя. В SDP лежит отпечаток DTLS-сертификата, так
// что подпись гарантирует: на том конце именно этот друг, а не тот, кто
// перехватил код по дороге. Код сжат (deflate со словарем частых строк SDP),
// чтобы влезал в одно сообщение мессенджера.

import (
	"bytes"
	"compress/flate"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	invitePrefix = "ZENO:"
	inviteMaxAge = time.Hour
)

const sdpDict = "v=0\r\no=- 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=group:BUNDLE 0\r\na=extmap-allow-mixed\r\n" +
	"a=msid-semantic: WMS\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\nc=IN IP4 0.0.0.0\r\n" +
	"a=ice-options:trickle\r\na=fingerprint:sha-256 \r\na=setup:actpass\r\na=setup:active\r\na=mid:0\r\n" +
	"a=sctp-port:5000\r\na=max-message-size:262144\r\na=ice-ufrag:\r\na=ice-pwd:\r\n" +
	"a=candidate: 1 udp 2122260223  typ host generation 0 network-id 1 network-cost 10\r\n" +
	"a=candidate: 1 udp 1686052607  typ srflx raddr  rport  generation 0 network-id 1 network-cost 10\r\n" +
	"a=candidate: 1 tcp 1518280447  9 typ host tcptype active generation 0 network-id 1\r\n" +
	".local udp tcp typ relay a=end-of-candidates\r\n"

type invite struct {
	T    string `json:"t"` // offer | answer
	From string `json:"f"`
	To   string `json:"o"`
	N    string `json:"n"` // номер предложения; ответ повторяет его
	TS   int64  `json:"s"`
	SDP  string `json:"d"`
	Sig  string `json:"g"`
}

func (in invite) signed() []byte {
	return []byte(strings.Join([]string{"zeno-invite-v1", in.T, in.From, in.To, in.N, strconv.FormatInt(in.TS, 10), in.SDP}, "\n"))
}

// MakeInvite подписывает и упаковывает SDP для друга to. Для предложения номер
// создается здесь, ответ передает номер предложения.
func (a *Account) MakeInvite(kind, to, nonce, sdp string) (string, string, error) {
	if kind != "offer" && kind != "answer" {
		return "", "", errors.New("неизвестный тип кода")
	}
	if _, ok := a.friend(to); !ok {
		return "", "", errNotFound
	}
	if kind == "offer" {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		nonce = b64.EncodeToString(b)
	}
	in := invite{T: kind, From: a.ID(), To: to, N: nonce, TS: time.Now().Unix(), SDP: sdp}
	in.Sig = b64.EncodeToString(ed25519.Sign(a.priv, in.signed()))
	js, _ := json.Marshal(in)
	var buf bytes.Buffer
	zw, _ := flate.NewWriterDict(&buf, flate.BestCompression, []byte(sdpDict))
	_, _ = zw.Write(js)
	_ = zw.Close()
	return invitePrefix + b64.EncodeToString(buf.Bytes()), nonce, nil
}

// OpenInvite проверяет код: адресован нам, от друга, подпись верна, не устарел.
func (a *Account) OpenInvite(code string) (invite, Friend, error) {
	code = strings.Join(strings.Fields(code), "")
	if !strings.HasPrefix(code, invitePrefix) {
		return invite{}, Friend{}, errors.New("это не код соединения Zeno")
	}
	raw, err := b64.DecodeString(strings.TrimPrefix(code, invitePrefix))
	var in invite
	if err == nil {
		var js []byte
		js, err = io.ReadAll(io.LimitReader(flate.NewReaderDict(bytes.NewReader(raw), []byte(sdpDict)), 1<<20))
		if err == nil {
			err = json.Unmarshal(js, &in)
		}
	}
	if err != nil {
		return invite{}, Friend{}, errors.New("код поврежден - скопируйте его целиком")
	}
	if in.To != a.ID() {
		return invite{}, Friend{}, errors.New("этот код сделан не для вас")
	}
	fr, ok := a.friend(in.From)
	if !ok {
		return invite{}, Friend{}, errors.New("код от того, кого нет в друзьях - сначала добавьте его по коду дружбы")
	}
	pub, _ := b64.DecodeString(in.From)
	sig, _ := b64.DecodeString(in.Sig)
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, in.signed(), sig) {
		return invite{}, Friend{}, errors.New("подпись кода не сходится - код подделан или испорчен")
	}
	if age := time.Since(time.Unix(in.TS, 0)); age > inviteMaxAge || age < -inviteMaxAge {
		return invite{}, Friend{}, errors.New("код устарел - попросите новый")
	}
	return in, fr, nil
}
