package sipserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/webrtc/v4"

	"mcs/internal/config"
	"mcs/internal/store"
)

func testAVSDP(audioPort, videoPort int) []byte {
	return []byte(fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n"+
		"m=audio %d RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n"+
		"m=video %d RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=fmtp:96 packetization-mode=1\r\n", audioPort, videoPort))
}

func videoRTP(seq uint16) []byte {
	payload := make([]byte, 1000)
	for i := range payload {
		payload[i] = byte(i)
	}
	pkt := make([]byte, 12+len(payload))
	pkt[0] = 0x80
	pkt[1] = 96
	pkt[2] = byte(seq >> 8)
	pkt[3] = byte(seq)
	pkt[4] = byte(seq)
	pkt[8] = 0x55
	pkt[9] = 0x66
	pkt[10] = 0x77
	pkt[11] = 0x88
	copy(pkt[12:], payload)
	return pkt
}

func TestCallVideoRelay(t *testing.T) {
	const mcsPort = 25080
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := store.New([]config.DeviceConfig{{Ext: "1002", Name: "door", Password: "pass-1002"}})
	srv, err := New(st, NewAuthenticator("mcs", "test"), store.NewCDRStore(10), store.NewAlarmStore(10), Options{
		Domain: "mcs.local", Advertise: fmt.Sprintf("127.0.0.1:%d", mcsPort), DataDir: t.TempDir(),
		Alarm: config.AlarmConfig{Number: "8000"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ctx, fmt.Sprintf("127.0.0.1:%d", mcsPort)) }()
	defer srv.Close()
	time.Sleep(150 * time.Millisecond)

	uaB, _ := sipgo.NewUA()
	defer uaB.Close()
	srvB, _ := sipgo.NewServer(uaB)
	duaB := &sipgo.DialogUA{Client: mustClient(t, uaB), ContactHDR: sip.ContactHeader{Address: sip.Uri{
		Scheme: "sip", User: "1002", Host: "127.0.0.1", Port: 28008,
	}}}
	var (
		mu          sync.Mutex
		offeredPort int
	)
	srvB.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		dss, err := duaB.ReadInvite(req, tx)
		if err != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
			return
		}
		mu.Lock()
		offeredPort = parseSDP(req.Body()).VideoPort
		mu.Unlock()
		_ = dss.Respond(180, "Ringing", nil)
		_ = dss.RespondSDP(testAVSDP(28111, 28113))
	})
	srvB.OnAck(func(*sip.Request, sip.ServerTransaction) {})
	srvB.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	go func() { _ = srvB.ListenAndServe(ctx, "udp", "127.0.0.1:28008") }()
	time.Sleep(150 * time.Millisecond)

	uaReg, _ := sipgo.NewUA()
	defer uaReg.Close()
	registerForTest(t, mustClient(t, uaReg), mcsPort, "1002", "pass-1002", "sip:1002@127.0.0.1:28008")

	uaA, _ := sipgo.NewUA()
	defer uaA.Close()
	duaA := &sipgo.DialogUA{Client: mustClient(t, uaA), ContactHDR: sip.ContactHeader{Address: sip.Uri{
		Scheme: "sip", User: "1001", Host: "127.0.0.1", Port: 16017,
	}}}
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: "1002", Host: "127.0.0.1", Port: mcsPort})
	req.AppendHeader(sip.NewHeader("From", "<sip:1001@mcs.local>;tag=video"))
	req.AppendHeader(sip.NewHeader("To", "<sip:1002@mcs.local>"))
	req.AppendHeader(sip.NewHeader("Call-ID", "video-call"))
	req.AppendHeader(sip.NewHeader("CSeq", "1 INVITE"))
	req.AppendHeader(sip.NewHeader("Contact", "<sip:1001@127.0.0.1:16017>"))
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody(testAVSDP(28110, 28112))

	callCtx, callCancel := context.WithTimeout(ctx, 8*time.Second)
	defer callCancel()
	dcs, err := duaA.WriteInvite(callCtx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(callCtx, sipgo.AnswerOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := dcs.Ack(callCtx); err != nil {
		t.Fatal(err)
	}
	answer := parseSDP(dcs.InviteResponse.Body())
	if answer.VideoPort == 0 {
		t.Fatal("no video relay port in answer")
	}
	mu.Lock()
	bVideoRelay := offeredPort
	mu.Unlock()
	if bVideoRelay == 0 {
		t.Fatal("no video relay port offered to callee")
	}
	// Attach a browser-style recvonly WebRTC viewer to the call.
	active := srv.ActiveCalls()
	if len(active) != 1 || !active[0].Video {
		t.Fatalf("active video calls = %+v", active)
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionRecvonly,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionRecvonly,
	}); err != nil {
		t.Fatal(err)
	}
	connected := make(chan struct{})
	var connectedOnce sync.Once
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			connectedOnce.Do(func() { close(connected) })
		}
	})
	var viewerPackets, viewerAudio atomic.Int32
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			if _, _, err := track.ReadRTP(); err != nil {
				return
			}
			if track.Kind() == webrtc.RTPCodecTypeAudio {
				viewerAudio.Add(1)
			} else {
				viewerPackets.Add(1)
			}
		}
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-webrtc.GatheringCompletePromise(pc)
	viewerAnswer, err := srv.HandleWebRTCOffer(context.Background(), active[0].CallID, pc.LocalDescription().SDP, false)
	if err != nil {
		t.Fatalf("webrtc offer: %v", err)
	}
	if !strings.Contains(viewerAnswer, "H264") {
		t.Fatalf("answer has no H264: %s", viewerAnswer)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: viewerAnswer}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(200 * time.Millisecond)

	callerAudio, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 28110})
	if err != nil {
		t.Fatal(err)
	}
	defer callerAudio.Close()
	callerVideo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 28112})
	if err != nil {
		t.Fatal(err)
	}
	defer callerVideo.Close()
	doorVideo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 28113})
	if err != nil {
		t.Fatal(err)
	}
	defer doorVideo.Close()
	var callerGot, doorGot atomic.Int32
	go func() {
		buf := make([]byte, 2048)
		for {
			_ = callerVideo.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, _, err := callerVideo.ReadFromUDP(buf); err != nil {
				return
			}
			callerGot.Add(1)
		}
	}()
	go func() {
		buf := make([]byte, 2048)
		for {
			_ = doorVideo.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, _, err := doorVideo.ReadFromUDP(buf); err != nil {
				return
			}
			doorGot.Add(1)
		}
	}()

	dstA := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: answer.VideoPort}
	dstB := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: bVideoRelay}
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("WebRTC viewer did not connect")
	}
	time.Sleep(200 * time.Millisecond)

	audioDst := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: answer.AudioPort}
	for i := 0; i < 20; i++ {
		_, _ = callerVideo.WriteToUDP(videoRTP(uint16(i)), dstA)
		_, _ = doorVideo.WriteToUDP(videoRTP(uint16(i)), dstB)
		_, _ = callerAudio.WriteToUDP(rtpPacket(uint16(i), 0xFF), audioDst)
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if callerGot.Load() == 0 || doorGot.Load() == 0 {
		t.Fatalf("video packets caller=%d door=%d", callerGot.Load(), doorGot.Load())
	}
	time.Sleep(300 * time.Millisecond)
	if viewerPackets.Load() == 0 {
		t.Fatal("WebRTC viewer received no video packets")
	}
	if viewerAudio.Load() == 0 {
		t.Fatal("WebRTC viewer received no audio packets")
	}
	if err := dcs.Bye(callCtx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(srv.ActiveCalls()) > 0 {
		time.Sleep(30 * time.Millisecond)
	}
}
