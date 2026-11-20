package sipserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/emiago/sipgo"

	"mcs/internal/config"
	"mcs/internal/store"
)

// Run with a local Kokoro service: MCS_REAL_TTS=1 go test ./internal/sipserver -run TestRealKokoroBroadcast -v
func TestRealKokoroBroadcast(t *testing.T) {
	if os.Getenv("MCS_REAL_TTS") == "" {
		t.Skip("set MCS_REAL_TTS=1 to test the local Kokoro service")
	}
	const mcsPort = 25077
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := store.New([]config.DeviceConfig{{Ext: "2004", Name: "local speaker", Password: "pass-2004"}})
	srv, err := New(st, NewAuthenticator("mcs", "test"), store.NewCDRStore(10), store.NewAlarmStore(10), Options{
		Domain: "mcs.local", Advertise: fmt.Sprintf("127.0.0.1:%d", mcsPort), DataDir: t.TempDir(),
		Alarm: config.AlarmConfig{Number: "8000"},
		Broadcast: config.BroadcastConfig{
			AutoAnswer: "<http://127.0.0.1>;info=alert-autoanswer", InviteTimeoutS: 3, MaxConcurrency: 2,
		},
		TTS:   config.TTSConfig{BaseURL: "http://127.0.0.1:8399", VoiceZH: "zf_001", VoiceEN: "af_heart", TimeoutS: 20},
		Zones: []config.ZoneConfig{{ID: "real", Name: "real", Members: []string{"2004"}, Mode: "unicast"}},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ctx, fmt.Sprintf("127.0.0.1:%d", mcsPort)) }()
	defer srv.Close()
	time.Sleep(150 * time.Millisecond)

	startAutoAnswerUAS(t, ctx, "2004", 28004, 28104)
	conn, count := listenRTP(t, 28104)
	defer conn.Close()
	time.Sleep(100 * time.Millisecond)

	uaReg, _ := sipgo.NewUA()
	defer uaReg.Close()
	registerForTest(t, mustClient(t, uaReg), mcsPort, "2004", "pass-2004", "sip:2004@127.0.0.1:28004")

	view, err := srv.StartBroadcast(context.Background(), BroadcastRequest{ZoneID: "real", Text: "你好，这是 MCS 本地语音广播测试。", Priority: 3})
	if err != nil {
		t.Fatalf("real tts broadcast: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	var final BroadcastView
	for time.Now().Before(deadline) {
		for _, v := range srv.BroadcastSessions(false) {
			if v.ID == view.ID {
				final = v
			}
		}
		if final.State == "finished" || final.State == "failed" || final.State == "partial" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if final.State != "finished" || final.Delivered != 1 {
		t.Fatalf("state=%q delivered=%d error=%q", final.State, final.Delivered, final.Error)
	}
	if count.Load() == 0 {
		t.Fatal("no RTP packets received by endpoint")
	}
}
