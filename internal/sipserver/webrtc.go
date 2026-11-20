package sipserver

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// mediaTap fans H.264 RTP packets from a SIP video call out to browser
// PeerConnections.
type mediaTap struct {
	mu     sync.RWMutex
	tracks map[*webrtc.TrackLocalStaticRTP]struct{}
	pcs    map[*webrtc.PeerConnection]struct{}
	closed bool
}

func newMediaTap() *mediaTap {
	return &mediaTap{
		tracks: map[*webrtc.TrackLocalStaticRTP]struct{}{},
		pcs:    map[*webrtc.PeerConnection]struct{}{},
	}
}

func (t *mediaTap) push(raw []byte) {
	t.mu.RLock()
	if t.closed || len(t.tracks) == 0 {
		t.mu.RUnlock()
		return
	}
	tracks := make([]*webrtc.TrackLocalStaticRTP, 0, len(t.tracks))
	for track := range t.tracks {
		tracks = append(tracks, track)
	}
	t.mu.RUnlock()

	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return
	}
	for _, track := range tracks {
		_ = track.WriteRTP(&pkt)
	}
}

func (t *mediaTap) add(pc *webrtc.PeerConnection, track *webrtc.TrackLocalStaticRTP) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.tracks[track] = struct{}{}
	t.pcs[pc] = struct{}{}
}

func (t *mediaTap) remove(pc *webrtc.PeerConnection, track *webrtc.TrackLocalStaticRTP) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.tracks, track)
	delete(t.pcs, pc)
}

func (t *mediaTap) close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	pcs := make([]*webrtc.PeerConnection, 0, len(t.pcs))
	for pc := range t.pcs {
		pcs = append(pcs, pc)
	}
	t.tracks = map[*webrtc.TrackLocalStaticRTP]struct{}{}
	t.pcs = map[*webrtc.PeerConnection]struct{}{}
	t.mu.Unlock()
	for _, pc := range pcs {
		_ = pc.Close()
	}
}

func (c *call) browserAudioRelay() *relay {
	c.stateMu.RLock()
	from, to := c.from, c.to
	ra, rb := c.relayA, c.relayB
	c.stateMu.RUnlock()
	if strings.HasPrefix(to, "6") && !strings.HasPrefix(from, "6") {
		return rb
	}
	return ra
}

func (c *call) pumpBrowserAudio(track *webrtc.TrackRemote) {
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		c.stateMu.RLock()
		pt := c.audioPT
		c.stateMu.RUnlock()
		dst := c.browserAudioRelay()
		if dst == nil {
			return
		}
		pkt.PayloadType = pt
		pkt.SSRC = 0x4d4353
		raw, err := pkt.Marshal()
		if err != nil {
			continue
		}
		_ = dst.writeToPeer(raw)
	}
}

// HandleWebRTCOffer creates a sendrecv audio + recvonly H.264 browser viewer.
func (s *Server) HandleWebRTCOffer(ctx context.Context, callID, offerSDP string) (string, error) {
	c := s.calls.getByID(callID)
	if c == nil {
		return "", fmt.Errorf("call not found")
	}
	c.stateMu.RLock()
	tap := c.videoTap
	audioTap := c.audioTap
	audioCodec := c.codecA
	c.stateMu.RUnlock()
	if tap == nil || audioTap == nil {
		return "", fmt.Errorf("call has no video")
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return "", err
	}
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "packetization-mode=1",
	}, "video", callID)
	if err != nil {
		_ = pc.Close()
		return "", err
	}
	audioMime := webrtc.MimeTypePCMU
	if strings.EqualFold(audioCodec, "PCMA") {
		audioMime = webrtc.MimeTypePCMA
	}
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: audioMime, ClockRate: 8000,
	}, "audio", callID)
	if err != nil {
		_ = pc.Close()
		return "", err
	}
	if _, err := pc.AddTrack(audioTrack); err != nil {
		_ = pc.Close()
		return "", err
	}
	if _, err := pc.AddTrack(track); err != nil {
		_ = pc.Close()
		return "", err
	}
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if track.Kind() == webrtc.RTPCodecTypeAudio {
			go c.pumpBrowserAudio(track)
		}
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateDisconnected:
			tap.remove(pc, track)
			audioTap.remove(pc, audioTrack)
			_ = pc.Close()
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("set offer: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("create answer: %w", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("set answer: %w", err)
	}
	select {
	case <-webrtc.GatheringCompletePromise(pc):
	case <-ctx.Done():
		_ = pc.Close()
		return "", ctx.Err()
	}
	local := pc.LocalDescription()
	if local == nil {
		_ = pc.Close()
		return "", fmt.Errorf("no local description")
	}
	tap.add(pc, track)
	audioTap.add(pc, audioTrack)
	s.log.Info("webrtc viewer attached", "call_id", callID)
	return local.SDP, nil
}
