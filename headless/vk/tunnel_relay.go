package main

import (
	"log"
	"sync"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"whitelist-bypass/relay/common"
	"whitelist-bypass/relay/tunnel"
)

type TunnelRelay struct {
	pc          *webrtc.PeerConnection
	remoteSet   bool
	pending     []webrtc.ICECandidateInit
	externalICE func(*webrtc.ICECandidate)
	externalCSC func(webrtc.PeerConnectionState)

	dc *webrtc.DataChannel

	sampleTrack *webrtc.TrackLocalStaticSample
	tun         *tunnel.VP8DataTunnel
	obf         *tunnel.TunnelObfuscator
	OnConnected func(tunnel.DataTunnel)

	screenDC       *webrtc.DataChannel
	producerScreen *webrtc.DataChannel
	sym            *tunnel.SymmetricScreenTunnel

	readBufSize int
	maxDCBuf    uint64

	mode     string
	modeOnce sync.Once

	lifecycleMu  sync.Mutex
	closed       bool
	sessionClose func()
}

func (u *TunnelRelay) SetObfuscator(o *tunnel.TunnelObfuscator) { u.obf = o }

func NewTunnelRelay() *TunnelRelay {
	return &TunnelRelay{mode: "unknown"}
}

func (u *TunnelRelay) Init(iceServers []webrtc.ICEServer) error {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers})
	if err != nil {
		return err
	}
	u.pc = pc

	negotiated := true
	dcID := uint16(2)
	dc, err := pc.CreateDataChannel("tunnel", &webrtc.DataChannelInit{
		Negotiated: &negotiated,
		ID:         &dcID,
	})
	if err != nil {
		log.Printf("[relay] warning: could not create tunnel DC: %v", err)
	} else {
		u.dc = dc
		dc.OnOpen(func() {
			log.Printf("[relay] tunnel DC open (readyState=%v)", dc.ReadyState())
			u.modeOnce.Do(func() {
				u.mode = "dc"
				log.Println("[relay] === MODE: DC (RelayBridge) ===")
				direct := tunnel.NewDCTunnel(dc, u.obf, u.readBufSize, log.Printf)
				direct.SetMaxBufferedAmount(u.maxDCBuf)
				if u.OnConnected != nil {
					u.OnConnected(direct)
				}
			})
		})
		dc.OnClose(func() {
			log.Println("[relay] tunnel DC closed")
		})
	}

	sampleTrack, _ := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
		"video", "tunnel-video",
	)
	u.sampleTrack = sampleTrack

	audioTrack, _ := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		"audio", "tunnel-audio",
	)
	pc.AddTrack(audioTrack)
	pc.AddTrack(sampleTrack)

	ordered := true
	dcNotif, err := pc.CreateDataChannel("producerNotification", &webrtc.DataChannelInit{Ordered: &ordered})
	if err == nil {
		dcNotif.OnOpen(func() { log.Println("[relay] producerNotification DC opened") })
		dcNotif.OnMessage(func(msg webrtc.DataChannelMessage) {
			log.Printf("[relay] producerNotification msg len=%d", len(msg.Data))
		})
	}
	dcCmd, err := pc.CreateDataChannel("producerCommand", &webrtc.DataChannelInit{Ordered: &ordered})
	if err == nil {
		dcCmd.OnOpen(func() { log.Println("[relay] producerCommand DC opened") })
		dcCmd.OnMessage(func(msg webrtc.DataChannelMessage) {
			log.Printf("[relay] producerCommand msg len=%d", len(msg.Data))
		})
	}
	producerScreen, psErr := pc.CreateDataChannel("producerScreenShare", &webrtc.DataChannelInit{Ordered: &ordered})
	if psErr == nil {
		u.producerScreen = producerScreen
		producerScreen.OnOpen(func() { log.Printf("[relay] producerScreenShare DC open, reading uplink screen") })
		producerScreen.OnMessage(func(msg webrtc.DataChannelMessage) {
			if u.sym != nil {
				u.sym.HandleScreenFrame(msg.Data)
			}
		})
	}
	screenDC, scErr := pc.CreateDataChannel("consumerScreenShare", &webrtc.DataChannelInit{Ordered: &ordered})
	if scErr == nil {
		u.screenDC = screenDC
		screenDC.OnOpen(func() { log.Printf("[relay] consumerScreenShare DC open, writing downlink screen") })
	}

	pc.OnICECandidate(func(cand *webrtc.ICECandidate) {
		if cand == nil {
			return
		}
		if u.externalICE != nil {
			u.externalICE(cand)
		}
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[relay] connection state: %s (mode=%s)", state.String(), u.mode)
		if u.externalCSC != nil {
			u.externalCSC(state)
		}
	})

	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		log.Printf("[relay] remote track: %s", track.Codec().MimeType)
		u.modeOnce.Do(func() {
			u.mode = "video"
			log.Println("[relay] === MODE: VIDEO ===")
			u.tun = tunnel.NewVP8DataTunnel(sampleTrack, u.obf, log.Printf)
			u.tun.Start(0, 0)
			var downlink tunnel.DataTunnel = u.tun
			if u.screenDC != nil {
				writer := tunnel.NewScreenWriter(u.obf, "screen-down", log.Printf)
				dc := u.screenDC
				writer.SetSend(dc.Send)
				u.sym = tunnel.NewSymmetricScreenTunnel(u.tun, writer, u.obf, func() bool {
					return dc.ReadyState() == webrtc.DataChannelStateOpen
				}, log.Printf)
				downlink = u.sym
				log.Println("[relay] === MODE: VIDEO (with screenshare) ===")
			}
			if u.OnConnected != nil {
				u.OnConnected(downlink)
			}
		})
		go u.readTrack(track)
	})

	log.Printf("[relay] PC created (%d ICE servers)", len(iceServers))
	return nil
}

func (u *TunnelRelay) CreateOffer() (webrtc.SessionDescription, error) {
	offer, err := u.pc.CreateOffer(nil)
	if err != nil {
		return offer, err
	}
	u.pc.SetLocalDescription(offer)
	return offer, nil
}

func (u *TunnelRelay) CreateAnswer() (webrtc.SessionDescription, error) {
	answer, err := u.pc.CreateAnswer(nil)
	if err != nil {
		return answer, err
	}
	u.pc.SetLocalDescription(answer)
	return answer, nil
}

func (u *TunnelRelay) SetRemoteDescription(sdpType webrtc.SDPType, sdp string) error {
	err := u.pc.SetRemoteDescription(webrtc.SessionDescription{Type: sdpType, SDP: sdp})
	if err != nil {
		return err
	}
	u.remoteSet = true
	for _, cand := range u.pending {
		u.pc.AddICECandidate(cand)
	}
	u.pending = nil
	return nil
}

func (u *TunnelRelay) AddICECandidate(candidate webrtc.ICECandidateInit) error {
	if !u.remoteSet {
		u.pending = append(u.pending, candidate)
		return nil
	}
	return u.pc.AddICECandidate(candidate)
}

func (u *TunnelRelay) OnICECandidate(fn func(*webrtc.ICECandidate)) {
	u.externalICE = fn
}

func (u *TunnelRelay) OnConnectionStateChange(fn func(webrtc.PeerConnectionState)) {
	u.externalCSC = fn
}

func (u *TunnelRelay) SetSessionClose(fn func()) {
	u.lifecycleMu.Lock()
	if u.closed {
		u.lifecycleMu.Unlock()
		if fn != nil {
			fn()
		}
		return
	}
	u.sessionClose = fn
	u.lifecycleMu.Unlock()
}

func (u *TunnelRelay) Close() {
	u.lifecycleMu.Lock()
	if u.closed {
		u.lifecycleMu.Unlock()
		return
	}
	u.closed = true
	closeSession := u.sessionClose
	u.sessionClose = nil
	u.lifecycleMu.Unlock()

	if u.sym != nil {
		u.sym.Stop()
		u.sym = nil
	}
	if u.tun != nil {
		u.tun.Stop()
		u.tun = nil
	}
	u.dc = nil
	if u.pc != nil {
		u.pc.OnConnectionStateChange(nil)
		u.pc.OnICECandidate(nil)
		u.pc.OnTrack(nil)
		oldPC := u.pc
		u.pc = nil
		go oldPC.Close()
	}
	u.remoteSet = false
	u.pending = nil
	u.sampleTrack = nil
	if closeSession != nil {
		closeSession()
	}
}

func (u *TunnelRelay) readTrack(track *webrtc.TrackRemote) {
	if track.Codec().MimeType != webrtc.MimeTypeVP8 {
		buf := make([]byte, common.UDPBufSize)
		for {
			if _, _, err := track.Read(buf); err != nil {
				return
			}
		}
	}

	var vp8Pkt codecs.VP8Packet
	var frameBuf []byte
	var lastSeq uint16
	var haveLastSeq bool
	frameValid := false
	var recvCount int
	buf := make([]byte, common.RTPBufSize)
	for {
		n, _, err := track.Read(buf)
		if err != nil {
			return
		}
		pkt := &rtp.Packet{}
		if pkt.Unmarshal(buf[:n]) != nil {
			continue
		}
		if haveLastSeq && pkt.SequenceNumber != lastSeq+1 {
			frameValid = false
			frameBuf = frameBuf[:0]
		}
		lastSeq = pkt.SequenceNumber
		haveLastSeq = true

		vp8Payload, err := vp8Pkt.Unmarshal(pkt.Payload)
		if err != nil {
			frameValid = false
			frameBuf = frameBuf[:0]
			continue
		}
		if vp8Pkt.S == 1 {
			frameBuf = frameBuf[:0]
			frameValid = true
		}
		if !frameValid {
			continue
		}
		frameBuf = append(frameBuf, vp8Payload...)
		if !pkt.Marker {
			continue
		}
		recvCount++
		if recvCount <= 3 || recvCount%200 == 0 {
			log.Printf("[video] recv vp8 frame #%d %d bytes", recvCount, len(frameBuf))
		}

		if u.tun != nil {
			u.tun.HandleFrame(frameBuf)
		}
		frameBuf = frameBuf[:0]
		frameValid = false
	}
}
