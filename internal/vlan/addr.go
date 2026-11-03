package vlan

// Адреса virtualZeno: 10.77.x.y/16. Адрес выводится из публичного ключа
// друга, поэтому его не нужно никому сообщать и нельзя подменить: пакет
// от друга с чужим адресом отправителя отбрасывается.

import (
	"crypto/sha256"
	"net/netip"
)

const (
	MTU = 1200 // один пакет - один кусок SCTP (pion режет сообщения по 1200 байт)

	netMask   = "255.255.0.0"
	netPrefix = "10.77.0.0/16"
)

var (
	subnet    = netip.MustParsePrefix(netPrefix)
	bcastNet  = netip.AddrFrom4([4]byte{10, 77, 255, 255})
	bcastAll  = netip.AddrFrom4([4]byte{255, 255, 255, 255})
	multicast = netip.MustParsePrefix("224.0.0.0/4")
)

// Addr - адрес пользователя с таким ID (публичным ключом).
func Addr(id string) netip.Addr {
	h := sha256.Sum256([]byte("virtualZeno:" + id))
	b := h[1]
	switch b {
	case 0:
		b = 1
	case 255:
		b = 254
	}
	return netip.AddrFrom4([4]byte{10, 77, h[0], b})
}

// header - адреса из заголовка IPv4; ok=false - не IPv4 или обрезан.
func header(pkt []byte) (src, dst netip.Addr, ok bool) {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return src, dst, false
	}
	src = netip.AddrFrom4([4]byte(pkt[12:16]))
	dst = netip.AddrFrom4([4]byte(pkt[16:20]))
	return src, dst, true
}

// toAll: пакет для всех - широковещательный или групповой (так игры ищут сервер).
func toAll(dst netip.Addr) bool {
	return dst == bcastAll || dst == bcastNet || multicast.Contains(dst)
}
