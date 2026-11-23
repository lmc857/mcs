package sipserver

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
)

// HandlePagingWebRTC starts a one-way web microphone paging session. The
// browser only sends audio; the server feeds it into the existing live
// broadcast fan-out for the selected zone.
func (s *Server) HandlePagingWebRTC(ctx context.Context, zoneID, username, pin, offerSDP string) (string, string, error) {
	if !s.pagingCfg.Enabled {
		return "", "", fmt.Errorf("paging disabled")
	}
	if username == "" {
		username = "web"
	}
	if s.pagingCfg.PIN != "" {
		if subtle.ConstantTimeCompare([]byte(pin), []byte(s.pagingCfg.PIN)) != 1 {
			return "", "", fmt.Errorf("寻呼 PIN 不正确")
		}
	}
	z := s.zoneByID(zoneID)
	if z.id == "" || z.dial == "" {
		return "", "", fmt.Errorf("寻呼分区不存在")
	}
	bc, err := s.StartLiveBroadcast(z.id, 2)
	if err != nil {
		return "", "", fmt.Errorf("分区忙：%w", err)
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		bc.stop("failed")
		return "", "", err
	}
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		_ = pc.Close()
		bc.stop("failed")
		return "", "", err
	}
	id := fmt.Sprintf("page-web-%d", time.Now().UnixNano())
	ps := &pagingSession{
		id: id, zoneID: z.id, bc: bc, source: "web:" + username, sourceName: username + "（网页）",
		kind: "web", webPC: pc, startedAt: time.Now(), byeCh: make(chan struct{}), server: s,
	}
	s.pagingAdd(ps)
	s.pagingRecordStart(ps)
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if track.Kind() == webrtc.RTPCodecTypeAudio {
			go ps.pumpWebAudio(track)
		}
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateDisconnected:
			ps.stop()
		}
	})
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		ps.stop()
		return "", "", fmt.Errorf("set offer: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		ps.stop()
		return "", "", fmt.Errorf("create answer: %w", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		ps.stop()
		return "", "", fmt.Errorf("set answer: %w", err)
	}
	select {
	case <-webrtc.GatheringCompletePromise(pc):
	case <-ctx.Done():
		ps.stop()
		return "", "", ctx.Err()
	}
	local := pc.LocalDescription()
	if local == nil {
		ps.stop()
		return "", "", fmt.Errorf("no local description")
	}
	go func() {
		select {
		case <-bc.doneCh:
			ps.stop()
		case <-ps.byeCh:
		}
	}()
	s.log.Info("web paging started", "zone", z.id, "user", username, "id", id)
	return local.SDP, id, nil
}

func (p *pagingSession) pumpWebAudio(track *webrtc.TrackRemote) {
	codec := "PCMU"
	mime := strings.ToUpper(track.Codec().MimeType)
	switch {
	case strings.Contains(mime, "PCMA"):
		codec = "PCMA"
	case strings.Contains(mime, "PCMU"):
	default:
		p.server.log.Warn("web paging codec unsupported", "codec", track.Codec().MimeType, "id", p.id)
		return
	}
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		raw := decodeG711(pkt.Payload, codec)
		for off := 0; off < len(raw); off += 320 {
			frame := make([]byte, 160)
			end := off + 320
			if end > len(raw) {
				end = len(raw)
			}
			n := copy(frame, raw[off:end])
			for i := n; i < 160; i++ {
				frame[i] = 0xFF
			}
			if p.bc == nil || p.bc.liveCh == nil {
				return
			}
			select {
			case p.bc.liveCh <- frame:
			default:
			}
		}
	}
}
