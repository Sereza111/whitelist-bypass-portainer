package common

import (
	"io"
	"net"
	"testing"
)

func TestNegotiateAuthAcceptsFragmentedHandshake(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	result := make(chan bool, 1)
	go func() { result <- NegotiateAuth(server, "alice", "secret") }()

	writeFragments(t, client, []byte{5}, []byte{1}, []byte{2})
	assertBytes(t, client, []byte{5, 2})
	writeFragments(t, client,
		[]byte{1, 5}, []byte("al"), []byte("ice"),
		[]byte{6}, []byte("sec"), []byte("ret"),
	)
	assertBytes(t, client, []byte{1, 0})
	if !<-result {
		t.Fatal("fragmented username/password handshake was rejected")
	}
}

func TestReadSOCKSRequestAcceptsFragmentedDomain(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	type result struct {
		n   int
		err error
		buf []byte
	}
	resultCh := make(chan result, 1)
	go func() {
		buf := make([]byte, HandshakeBuf)
		n, err := ReadSOCKSRequest(server, buf)
		resultCh <- result{n: n, err: err, buf: buf}
	}()

	writeFragments(t, client,
		[]byte{5, 1}, []byte{0, AtypDomain}, []byte{11},
		[]byte("example"), []byte(".com"), []byte{0x01}, []byte{0xbb},
	)
	got := <-resultCh
	if got.err != nil {
		t.Fatalf("ReadSOCKSRequest: %v", got.err)
	}
	host, _, err := ParseAddress(got.buf, got.n)
	if err != nil || host != "example.com:443" {
		t.Fatalf("unexpected address host=%q err=%v", host, err)
	}
}

func writeFragments(t *testing.T, conn net.Conn, fragments ...[]byte) {
	t.Helper()
	for _, fragment := range fragments {
		if _, err := conn.Write(fragment); err != nil {
			t.Fatalf("write fragment: %v", err)
		}
	}
}

func assertBytes(t *testing.T, conn net.Conn, want []byte) {
	t.Helper()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read response: %v", err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("response mismatch got=%v want=%v", got, want)
		}
	}
}
