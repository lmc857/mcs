package sipserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/pion/webrtc/v4"

	"mcs/internal/config"
	"mcs/internal/store"
)

func TestPagingWebRTCJoin(t *testing.T) {
	const mcsPort = 15081
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := store.New([]config.DeviceConfig{{Ext: "3001", Name: "speaker", Password: "pass-3001"}})
	srv, err := New(st, NewAuthenticator("mcs", "test"), store.NewCDRStore(10), store.NewAlarmStore(10), Options{
		Domain: "mcs.local", Advertise: "127.0.0.1:15081", DataDir: t.TempDir(),
		Paging: config.PagingConfig{Enabled: true},
		Zones:  []config.ZoneConfig{{ID: "a", Name: "A", Dial: "5001", Members: []string{"3001"}, Mode: "unicast"}},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ctx, fmt.Sprintf("127.0.0.1:%d", mcsPort)) }()
	defer srv.Close()
	time.Sleep(120 * time.Millisecond)
	startAutoAnswerUAS(t, ctx, "3001", 28011, 28111)
	uaReg, _ := sipgo.NewUA()
	defer uaReg.Close()
	reg := mustClient(t, uaReg)
	registerForTest(t, reg, mcsPort, "3001", "pass-3001", "sip:3001@127.0.0.1:28011")
	time.Sleep(100 * time.Millisecond)

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
		t.Fatal(err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-webrtc.GatheringCompletePromise(pc)
	callCtx, callCancel := context.WithTimeout(ctx, 5*time.Second)
	defer callCancel()
	answer, id, err := srv.HandlePagingWebRTC(callCtx, "a", "operator", "", pc.LocalDescription().SDP)
	if err != nil {
		t.Fatalf("paging offer: %v", err)
	}
	if id == "" || answer == "" {
		t.Fatal("empty answer/id")
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatalf("set answer: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pc.ConnectionState() == webrtc.PeerConnectionStateConnected {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
		t.Fatalf("state = %s", pc.ConnectionState())
	}
	if err := srv.PagingStop(id); err != nil {
		t.Fatalf("stop: %v", err)
	}
}
