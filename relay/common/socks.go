package common

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

const SocksLocalhostIP = "127.0.0.1"

const (
	Ver        = 0x05
	CmdTCP     = 0x01
	CmdUDP     = 0x03
	AtypIPv4   = 0x01
	AtypDomain = 0x03
	AtypIPv6   = 0x04

	AuthNone     = 0x00
	AuthUserPass = 0x02
	AuthNoMatch  = 0xFF

	HandshakeBuf = 258
	UDPBufSize   = 4096
	RTPBufSize   = 65536
	// VP8BufSize fits one RTP packet: 1200 MTU - 1 VP8 descriptor - 64 tunnel wrapper - 9 protocol frame
	// (tunnel wrapper = 20 vp8 keepalive header + 4 epoch + 24 XChaCha20 nonce + 16 Poly1305 tag)
	VP8BufSize = 1126
	DCBufSize  = 32768
)

var (
	NoAuth   = []byte{Ver, AuthNone}
	OK       = []byte{Ver, 0x00, 0x00, AtypIPv4, 0, 0, 0, 0, 0, 0}
	ConnFail = []byte{Ver, 0x05, 0x00, AtypIPv4, 0, 0, 0, 0, 0, 0}
	CmdErr   = []byte{Ver, 0x07, 0x00, AtypIPv4, 0, 0, 0, 0, 0, 0}
	AddrErr  = []byte{Ver, 0x08, 0x00, AtypIPv4, 0, 0, 0, 0, 0, 0}
	GenFail  = []byte{Ver, 0x01, 0x00, AtypIPv4, 0, 0, 0, 0, 0, 0}
)

func NegotiateAuth(conn net.Conn, wantUser, wantPass string) bool {
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil || header[0] != Ver || header[1] == 0 {
		return false
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return false
	}
	wantedMethod := byte(AuthNone)
	if wantUser != "" {
		wantedMethod = AuthUserPass
	}
	hasWantedMethod := false
	for _, method := range methods {
		if method == wantedMethod {
			hasWantedMethod = true
			break
		}
	}
	if !hasWantedMethod {
		_, _ = conn.Write([]byte{Ver, AuthNoMatch})
		return false
	}
	_, _ = conn.Write([]byte{Ver, wantedMethod})
	if wantedMethod == AuthNone {
		return true
	}

	if _, err := io.ReadFull(conn, header); err != nil || header[0] != 0x01 || header[1] == 0 {
		return false
	}
	userBytes := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, userBytes); err != nil {
		return false
	}
	length := make([]byte, 1)
	if _, err := io.ReadFull(conn, length); err != nil || length[0] == 0 {
		_, _ = conn.Write([]byte{0x01, 0x01})
		return false
	}
	passBytes := make([]byte, int(length[0]))
	if _, err := io.ReadFull(conn, passBytes); err != nil {
		return false
	}
	user := string(userBytes)
	pass := string(passBytes)
	if user != wantUser || pass != wantPass {
		_, _ = conn.Write([]byte{0x01, 0x01})
		return false
	}
	_, _ = conn.Write([]byte{0x01, 0x00})
	return true
}

// ReadSOCKSRequest reads one complete variable-length SOCKS5 request from a
// stream. TCP is allowed to split the request at any byte boundary, so callers
// must not assume that a single Read returns the whole header and address.
func ReadSOCKSRequest(conn net.Conn, buf []byte) (int, error) {
	if len(buf) < 4 {
		return 0, fmt.Errorf("SOCKS request buffer too small")
	}
	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		return 0, err
	}
	if buf[0] != Ver {
		return 0, fmt.Errorf("unsupported SOCKS version 0x%02x", buf[0])
	}
	n := 4
	var remaining int
	switch buf[3] {
	case AtypIPv4:
		remaining = 6
	case AtypIPv6:
		remaining = 18
	case AtypDomain:
		if len(buf) < 5 {
			return 0, fmt.Errorf("SOCKS request buffer too small for domain")
		}
		if _, err := io.ReadFull(conn, buf[4:5]); err != nil {
			return 0, err
		}
		n++
		remaining = int(buf[4]) + 2
	default:
		return 0, fmt.Errorf("unsupported address type 0x%02x", buf[3])
	}
	if n+remaining > len(buf) {
		return 0, fmt.Errorf("SOCKS request exceeds buffer")
	}
	if _, err := io.ReadFull(conn, buf[n:n+remaining]); err != nil {
		return 0, err
	}
	return n + remaining, nil
}

func ParseAddress(buf []byte, n int) (host string, headerLen int, err error) {
	if n < 7 {
		return "", 0, fmt.Errorf("too short")
	}
	switch buf[3] {
	case AtypIPv4:
		if n < 10 {
			return "", 0, fmt.Errorf("too short for IPv4")
		}
		host = fmt.Sprintf("%d.%d.%d.%d:%d", buf[4], buf[5], buf[6], buf[7],
			binary.BigEndian.Uint16(buf[8:10]))
		return host, 10, nil
	case AtypDomain:
		dlen := int(buf[4])
		if n < 5+dlen+2 {
			return "", 0, fmt.Errorf("too short for domain")
		}
		host = fmt.Sprintf("%s:%d", string(buf[5:5+dlen]),
			binary.BigEndian.Uint16(buf[5+dlen:7+dlen]))
		return host, 5 + dlen + 2, nil
	case AtypIPv6:
		if n < 22 {
			return "", 0, fmt.Errorf("too short for IPv6")
		}
		ip := net.IP(buf[4:20])
		host = fmt.Sprintf("[%s]:%d", ip.String(),
			binary.BigEndian.Uint16(buf[20:22]))
		return host, 22, nil
	default:
		return "", 0, fmt.Errorf("unsupported address type 0x%02x", buf[3])
	}
}
