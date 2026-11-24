package sipserver

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
)

// HandleConferenceWebRTC joins a browser as a conference participant. The
// browser sends PCMU/PCMA audio and H.264 video; the conference mixer returns
// PCMU audio and the currently selected H.264 video source.
func (s *Server) HandleConferenceWebRTC(ctx context.Context, roomNumber, username, offerSDP string) (string, string, error) {
	if s.conference == nil {
		return "", "", fmt.Errorf("conference disabled")
	}
	room, ok := s.conference.find(roomNumber)
	if !ok {
		return "", "", fmt.Errorf("会议房间不存在或已停用")
	}
	if username == "" {
		username = "web"
	}
	id := fmt.Sprintf("web-%s-%d", sanitizeWebUser(username), time.Now().UnixNano())
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return "", "", err
	}
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1,
	}, "audio", id)
	if err != nil {
		_ = pc.Close()
		return "", "", err
	}
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "packetization-mode=1",
	}, "video", id)
	if err != nil {
		_ = pc.Close()
		return "", "", err
	}
	if _, err := pc.AddTrack(audioTrack); err != nil {
		_ = pc.Close()
		return "", "", fmt.Errorf("audio codec: %w", err)
	}
	if _, err := pc.AddTrack(videoTrack); err != nil {
		_ = pc.Close()
		return "", "", fmt.Errorf("video codec: %w", err)
	}
	p := &conferenceParticipant{
		id: id, ext: "web:" + username, name: username + "（网页）", codec: "PCMU", pt: 0,
		server: s, stopCh: make(chan struct{}), joinedAt: time.Now(),
		webrtcPC: pc, webrtcAudio: audioTrack, webrtcVideo: videoTrack, isWebRTC: true,
		seq: uint16(time.Now().UnixNano()), ts: uint32(time.Now().UnixNano()), ssrc: uint32(time.Now().UnixNano()),
	}
	if !room.add(p) {
		_ = pc.Close()
		return "", "", fmt.Errorf("会议人数已满")
	}
	s.conference.addCall(p)
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		switch track.Kind() {
		case webrtc.RTPCodecTypeAudio:
			go p.pumpWebRTCAudio(track)
		case webrtc.RTPCodecTypeVideo:
			go p.pumpWebRTCVideo(track)
		}
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateDisconnected:
			p.stop(false)
		}
	})
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		p.stop(false)
		return "", "", fmt.Errorf("set offer: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		p.stop(false)
		return "", "", fmt.Errorf("create answer: %w", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		p.stop(false)
		return "", "", fmt.Errorf("set answer: %w", err)
	}
	select {
	case <-webrtc.GatheringCompletePromise(pc):
	case <-ctx.Done():
		p.stop(false)
		return "", "", ctx.Err()
	}
	local := pc.LocalDescription()
	if local == nil {
		p.stop(false)
		return "", "", fmt.Errorf("no local description")
	}
	s.log.Info("conference webrtc joined", "room", roomNumber, "user", username, "id", id)
	s.emit("conference.joined", map[string]any{"room": roomNumber, "id": id, "ext": p.ext, "name": p.name})
	return local.SDP, id, nil
}

func sanitizeWebUser(user string) string {
	user = strings.TrimSpace(user)
	if user == "" {
		return "guest"
	}
	var b strings.Builder
	for _, r := range user {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "user"
	}
	return b.String()
}

func (p *conferenceParticipant) pumpWebRTCAudio(track *webrtc.TrackRemote) {
	codec := "PCMU"
	if strings.Contains(strings.ToUpper(track.Codec().MimeType), "PCMA") {
		codec = "PCMA"
	} else if !strings.Contains(strings.ToUpper(track.Codec().MimeType), "PCMU") {
		p.server.log.Warn("conference webrtc audio codec unsupported", "id", p.id, "codec", track.Codec().MimeType)
		return
	}
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		raw := decodeG711(pkt.Payload, codec)
		samples := make([]int16, len(raw)/2)
		for i := range samples {
			samples[i] = int16(binary.LittleEndian.Uint16(raw[i*2 : i*2+2]))
		}
		p.enqueuePCM(samples)
		p.videoMu.Lock()
		hasVideo := p.hasVideo
		p.videoMu.Unlock()
		if hasVideo {
			p.room.selectVideo(p)
		}
	}
}

func (p *conferenceParticipant) pumpWebRTCVideo(track *webrtc.TrackRemote) {
	if !strings.Contains(strings.ToUpper(track.Codec().MimeType), "H264") {
		p.server.log.Warn("conference webrtc video codec unsupported", "id", p.id, "codec", track.Codec().MimeType)
		return
	}
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		p.room.publishVideo(p, pkt)
	}
}
