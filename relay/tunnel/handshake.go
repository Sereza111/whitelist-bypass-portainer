package tunnel

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"

	"whitelist-bypass/relay/common"
)

const (
	WireVersion        uint16 = 1
	MinimumWireVersion uint16 = 1
)

const (
	CapabilityMetricsV1       uint64 = 1 << 0
	CapabilityVideoKCP1       uint64 = 1 << 1
	CapabilityMuxFlowControl  uint64 = 1 << 2
	CapabilityPriorityControl uint64 = 1 << 3
	CapabilityReliableDNS     uint64 = 1 << 4
)

type ReliabilityMode byte

const (
	ReliabilityUnknown ReliabilityMode = iota
	ReliabilityRawVP8
	ReliabilityDataChannel
	ReliabilityKCP
)

func (m ReliabilityMode) String() string {
	switch m {
	case ReliabilityRawVP8:
		return "raw-vp8"
	case ReliabilityDataChannel:
		return "data-channel"
	case ReliabilityKCP:
		return "kcp"
	default:
		return "unknown"
	}
}

type HandshakeStatus uint16

const (
	HandshakeOK HandshakeStatus = iota
	HandshakeIncompatibleWire
)

func (s HandshakeStatus) String() string {
	switch s {
	case HandshakeOK:
		return "ok"
	case HandshakeIncompatibleWire:
		return "incompatible-wire"
	default:
		return fmt.Sprintf("unknown-%d", s)
	}
}

type Hello struct {
	WireVersion       uint16
	Capabilities      uint64
	MaxCarrierPayload uint16
	Reliability       ReliabilityMode
	TrackCount        uint8
	Nonce             [16]byte
	BuildVersion      string
	BuildCommit       string
}

type HelloAck struct {
	SelectedWireVersion uint16
	Status              HandshakeStatus
	Capabilities        uint64
	EchoNonce           [16]byte
	ResponderNonce      [16]byte
}

type HandshakeResult struct {
	Peer                Hello
	SelectedWireVersion uint16
	Capabilities        uint64
	Status              HandshakeStatus
	LegacyFallback      bool
}

func (r HandshakeResult) Supports(capability uint64) bool {
	return !r.LegacyFallback && r.Status == HandshakeOK && r.Capabilities&capability != 0
}

var (
	helloMagic = [4]byte{'W', 'L', 'B', '2'}
	ackMagic   = [4]byte{'W', 'L', 'B', 'A'}
)

func newLocalHello(t DataTunnel, readBuf int) Hello {
	maxPayload := readBuf
	if maxPayload < 1 {
		maxPayload = common.VP8BufSize
	}
	if maxPayload > 0xFFFF {
		maxPayload = 0xFFFF
	}

	reliability := ReliabilityRawVP8
	trackCount := 1
	switch typed := t.(type) {
	case *DCTunnel:
		reliability = ReliabilityDataChannel
	case *KCPTunnel:
		reliability = ReliabilityKCP
		maxPayload = kcpSegmentMTU
	case *MultiTrackTunnel:
		trackCount = typed.SubTunnelCount()
	}
	if trackCount < 1 {
		trackCount = 1
	}
	if trackCount > 0xFF {
		trackCount = 0xFF
	}

	return Hello{
		WireVersion:       WireVersion,
		Capabilities:      CapabilityMetricsV1,
		MaxCarrierPayload: uint16(maxPayload),
		Reliability:       reliability,
		TrackCount:        uint8(trackCount),
		Nonce:             newHandshakeNonce(),
		BuildVersion:      common.Version,
		BuildCommit:       common.BuildCommit,
	}
}

func newHandshakeNonce() (nonce [16]byte) {
	if _, err := rand.Read(nonce[:]); err == nil {
		return nonce
	}
	binary.BigEndian.PutUint64(nonce[0:8], uint64(time.Now().UnixNano()))
	binary.BigEndian.PutUint64(nonce[8:16], uint64(time.Now().Unix()))
	return nonce
}

func EncodeHello(h Hello) []byte {
	version := truncateWireString(h.BuildVersion)
	commit := truncateWireString(h.BuildCommit)
	payload := make([]byte, 38+len(version)+len(commit))
	copy(payload[0:4], helloMagic[:])
	binary.BigEndian.PutUint16(payload[4:6], h.WireVersion)
	binary.BigEndian.PutUint64(payload[6:14], h.Capabilities)
	binary.BigEndian.PutUint16(payload[14:16], h.MaxCarrierPayload)
	payload[16] = byte(h.Reliability)
	payload[17] = h.TrackCount
	copy(payload[18:34], h.Nonce[:])
	binary.BigEndian.PutUint16(payload[34:36], uint16(len(version)))
	binary.BigEndian.PutUint16(payload[36:38], uint16(len(commit)))
	copy(payload[38:38+len(version)], version)
	copy(payload[38+len(version):], commit)
	return EncodeFrame(ControlConnID, MsgHello, payload)
}

func DecodeHello(payload []byte) (Hello, bool) {
	var h Hello
	if len(payload) < 38 || !bytes.Equal(payload[0:4], helloMagic[:]) {
		return h, false
	}
	versionLen := int(binary.BigEndian.Uint16(payload[34:36]))
	commitLen := int(binary.BigEndian.Uint16(payload[36:38]))
	if versionLen > 255 || commitLen > 255 || 38+versionLen+commitLen != len(payload) {
		return h, false
	}
	h.WireVersion = binary.BigEndian.Uint16(payload[4:6])
	h.Capabilities = binary.BigEndian.Uint64(payload[6:14])
	h.MaxCarrierPayload = binary.BigEndian.Uint16(payload[14:16])
	h.Reliability = ReliabilityMode(payload[16])
	h.TrackCount = payload[17]
	copy(h.Nonce[:], payload[18:34])
	h.BuildVersion = string(payload[38 : 38+versionLen])
	h.BuildCommit = string(payload[38+versionLen:])
	return h, true
}

func EncodeHelloAck(ack HelloAck) []byte {
	payload := make([]byte, 48)
	copy(payload[0:4], ackMagic[:])
	binary.BigEndian.PutUint16(payload[4:6], ack.SelectedWireVersion)
	binary.BigEndian.PutUint16(payload[6:8], uint16(ack.Status))
	binary.BigEndian.PutUint64(payload[8:16], ack.Capabilities)
	copy(payload[16:32], ack.EchoNonce[:])
	copy(payload[32:48], ack.ResponderNonce[:])
	return EncodeFrame(ControlConnID, MsgHelloAck, payload)
}

func DecodeHelloAck(payload []byte) (HelloAck, bool) {
	var ack HelloAck
	if len(payload) != 48 || !bytes.Equal(payload[0:4], ackMagic[:]) {
		return ack, false
	}
	ack.SelectedWireVersion = binary.BigEndian.Uint16(payload[4:6])
	ack.Status = HandshakeStatus(binary.BigEndian.Uint16(payload[6:8]))
	ack.Capabilities = binary.BigEndian.Uint64(payload[8:16])
	copy(ack.EchoNonce[:], payload[16:32])
	copy(ack.ResponderNonce[:], payload[32:48])
	return ack, true
}

func truncateWireString(value string) []byte {
	data := []byte(value)
	if len(data) > 255 {
		data = data[:255]
	}
	return data
}
