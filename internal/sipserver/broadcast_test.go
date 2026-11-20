package sipserver

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mcs/internal/config"
	"mcs/internal/store"
)

func broadcastWAV(seconds float64) []byte {
	samples := int(8000 * seconds)
	dataSize := samples * 2
	h := make([]byte, 44)
	copy(h[0:4], "RIFF")
	binary.LittleEndian.PutUint32(h[4:8], uint32(36+dataSize))
	copy(h[8:12], "WAVE")
	copy(h[12:16], "fmt ")
	binary.LittleEndian.PutUint32(h[16:20], 16)
	binary.LittleEndian.PutUint16(h[20:22], 1)
	binary.LittleEndian.PutUint16(h[22:24], 1)
	binary.LittleEndian.PutUint32(h[24:28], 8000)
	binary.LittleEndian.PutUint32(h[28:32], 16000)
	binary.LittleEndian.PutUint16(h[32:34], 2)
	binary.LittleEndian.PutUint16(h[34:36], 16)
	copy(h[36:40], "data")
	binary.LittleEndian.PutUint32(h[40:44], uint32(dataSize))
	return append(h, make([]byte, dataSize)...)
}

func startAutoAnswerUAS(t *testing.T, ctx context.Context, ext string, sipPort, rtpPort int) {
	t.Helper()
	ua, err := sipgo.NewUA()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ua.Close() })
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatal(err)
	}
	dua := &sipgo.DialogUA{Client: mustClient(t, ua), ContactHDR: sip.ContactHeader{Address: sip.Uri{
		Scheme: "sip", User: ext, Host: "127.0.0.1", Port: sipPort,
	}}}
	srv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		dss, err := dua.ReadInvite(req, tx)
		if err != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
			return
		}
		_ = dss.Respond(180, "Ringing", nil)
		_ = dss.RespondSDP(testSDP(rtpPort))
	})
	srv.OnAck(func(*sip.Request, sip.ServerTransaction) {})
	srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	go func() { _ = srv.ListenAndServe(ctx, "udp", fmt.Sprintf("127.0.0.1:%d", sipPort)) }()
}

func listenRTP(t *testing.T, port int) (*net.UDPConn, *atomic.Int32) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	go func() {
		buf := make([]byte, 2048)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, _, err := conn.ReadFromUDP(buf); err != nil {
				return
			}
			count.Add(1)
		}
	}()
	return conn, &count
}

func TestBroadcastTTSFanout(t *testing.T) {
	const mcsPort = 25075
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ttsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(broadcastWAV(1.0))
	}))
	defer ttsSrv.Close()

	st := store.New([]config.DeviceConfig{
		{Ext: "2001", Name: "speaker 1", Password: "pass-2001"},
		{Ext: "2002", Name: "speaker 2", Password: "pass-2002"},
	})
	cdr := store.NewCDRStore(10)
	alarms := store.NewAlarmStore(10)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := New(st, NewAuthenticator("mcs", "test-secret"), cdr, alarms, Options{
		Domain: "mcs.local", Advertise: fmt.Sprintf("127.0.0.1:%d", mcsPort), DataDir: t.TempDir(),
		Alarm: config.AlarmConfig{Number: "8000"},
		Broadcast: config.BroadcastConfig{
			AutoAnswer: "<http://127.0.0.1>;info=alert-autoanswer", InviteTimeoutS: 3, MaxConcurrency: 4,
		},
		TTS: config.TTSConfig{BaseURL: ttsSrv.URL, VoiceZH: "zf_001", VoiceEN: "af_heart", TimeoutS: 5},
		Zones: []config.ZoneConfig{{
			ID: "z1", Name: "测试分区", Members: []string{"2001", "2002"}, Mode: "unicast",
		}},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ctx, fmt.Sprintf("127.0.0.1:%d", mcsPort)) }()
	defer srv.Close()
	time.Sleep(150 * time.Millisecond)

	startAutoAnswerUAS(t, ctx, "2001", 28001, 28101)
	startAutoAnswerUAS(t, ctx, "2002", 28002, 28102)
	conn1, count1 := listenRTP(t, 28101)
	defer conn1.Close()
	conn2, count2 := listenRTP(t, 28102)
	defer conn2.Close()
	time.Sleep(150 * time.Millisecond)

	uaReg, _ := sipgo.NewUA()
	defer uaReg.Close()
	reg := mustClient(t, uaReg)
	registerForTest(t, reg, mcsPort, "2001", "pass-2001", "sip:2001@127.0.0.1:28001")
	registerForTest(t, reg, mcsPort, "2002", "pass-2002", "sip:2002@127.0.0.1:28002")

	view, err := srv.StartBroadcast(context.Background(), BroadcastRequest{ZoneID: "z1", Text: "测试广播", Priority: 2})
	if err != nil {
		t.Fatalf("start broadcast: %v", err)
	}
	if view.Total != 2 {
		t.Fatalf("total = %d", view.Total)
	}

	deadline := time.Now().Add(5 * time.Second)
	var final BroadcastView
	for time.Now().Before(deadline) {
		for _, v := range srv.BroadcastSessions(false) {
			if v.ID == view.ID {
				final = v
			}
		}
		if final.State == "finished" || final.State == "partial" || final.State == "failed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if final.State != "finished" {
		t.Fatalf("state = %q, delivered=%d failed=%d error=%q", final.State, final.Delivered, final.Failed, final.Error)
	}
	if final.Delivered != 2 {
		t.Fatalf("delivered = %d", final.Delivered)
	}
	time.Sleep(100 * time.Millisecond)
	if count1.Load() == 0 || count2.Load() == 0 {
		t.Fatalf("rtp packets: %d / %d", count1.Load(), count2.Load())
	}
}

func TestBroadcastPriorityPreemption(t *testing.T) {
	const mcsPort = 25076
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ttsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		seconds := 1.0
		if req.Input == "long broadcast" {
			seconds = 4.0
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(broadcastWAV(seconds))
	}))
	defer ttsSrv.Close()

	st := store.New([]config.DeviceConfig{{Ext: "2003", Name: "speaker", Password: "pass-2003"}})
	cdr := store.NewCDRStore(10)
	alarms := store.NewAlarmStore(10)
	srv, err := New(st, NewAuthenticator("mcs", "test"), cdr, alarms, Options{
		Domain: "mcs.local", Advertise: fmt.Sprintf("127.0.0.1:%d", mcsPort), DataDir: t.TempDir(),
		Alarm: config.AlarmConfig{Number: "8000"},
		Broadcast: config.BroadcastConfig{
			AutoAnswer: "<http://127.0.0.1>;info=alert-autoanswer", InviteTimeoutS: 3, MaxConcurrency: 2,
		},
		TTS: config.TTSConfig{BaseURL: ttsSrv.URL, VoiceZH: "zf_001", VoiceEN: "af_heart", TimeoutS: 5},
		Zones: []config.ZoneConfig{{
			ID: "z2", Name: "抢占分区", Members: []string{"2003"}, Mode: "unicast",
		}},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ctx, fmt.Sprintf("127.0.0.1:%d", mcsPort)) }()
	defer srv.Close()
	time.Sleep(150 * time.Millisecond)

	startAutoAnswerUAS(t, ctx, "2003", 28003, 28103)
	time.Sleep(100 * time.Millisecond)

	uaReg, _ := sipgo.NewUA()
	defer uaReg.Close()
	registerForTest(t, mustClient(t, uaReg), mcsPort, "2003", "pass-2003", "sip:2003@127.0.0.1:28003")

	low, err := srv.StartBroadcast(context.Background(), BroadcastRequest{ZoneID: "z2", Text: "long broadcast", Priority: 3})
	if err != nil {
		t.Fatal(err)
	}
	waitState := func(id, want string, timeout time.Duration) BroadcastView {
		deadline := time.Now().Add(timeout)
		var last BroadcastView
		for time.Now().Before(deadline) {
			for _, v := range srv.BroadcastSessions(false) {
				if v.ID == id {
					last = v
					if v.State == want {
						return v
					}
				}
			}
			time.Sleep(30 * time.Millisecond)
		}
		return last
	}
	if v := waitState(low.ID, "running", 3*time.Second); v.State != "running" {
		t.Fatalf("long state = %q", v.State)
	}

	high, err := srv.StartBroadcast(context.Background(), BroadcastRequest{ZoneID: "z2", Text: "short broadcast", Priority: 0})
	if err != nil {
		t.Fatal(err)
	}
	if v := waitState(high.ID, "finished", 5*time.Second); v.State != "finished" {
		t.Fatalf("high state = %q, error=%q", v.State, v.Error)
	}
	if v := waitState(low.ID, "preempted", 3*time.Second); v.State != "preempted" {
		t.Fatalf("low state = %q", v.State)
	}
}

func TestMediaLibraryAndFileBroadcast(t *testing.T) {
	const mcsPort = 25078
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := store.New([]config.DeviceConfig{{Ext: "2005", Name: "speaker", Password: "pass-2005"}})
	srv, err := New(st, NewAuthenticator("mcs", "test"), store.NewCDRStore(10), store.NewAlarmStore(10), Options{
		Domain: "mcs.local", Advertise: fmt.Sprintf("127.0.0.1:%d", mcsPort), DataDir: t.TempDir(),
		Alarm: config.AlarmConfig{Number: "8000"},
		Broadcast: config.BroadcastConfig{
			AutoAnswer: "<http://127.0.0.1>;info=alert-autoanswer", InviteTimeoutS: 3, MaxConcurrency: 2,
		},
		TTS:   config.TTSConfig{BaseURL: "http://127.0.0.1:8399", VoiceZH: "zf_001", VoiceEN: "af_heart", TimeoutS: 5},
		Zones: []config.ZoneConfig{{ID: "z3", Name: "音频库分区", Members: []string{"2005"}, Mode: "unicast"}},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ctx, fmt.Sprintf("127.0.0.1:%d", mcsPort)) }()
	defer srv.Close()
	time.Sleep(150 * time.Millisecond)

	item, err := srv.AddMedia("测试音频", broadcastWAV(0.5))
	if err != nil {
		t.Fatal(err)
	}
	if len(srv.ListMedia()) != 1 {
		t.Fatal("media not listed")
	}
	if _, data, ok := srv.MediaData(item.ID); !ok || len(data) < 44 {
		t.Fatal("media data missing")
	}

	startAutoAnswerUAS(t, ctx, "2005", 28005, 28105)
	conn, count := listenRTP(t, 28105)
	defer conn.Close()
	time.Sleep(100 * time.Millisecond)

	uaReg, _ := sipgo.NewUA()
	defer uaReg.Close()
	registerForTest(t, mustClient(t, uaReg), mcsPort, "2005", "pass-2005", "sip:2005@127.0.0.1:28005")

	view, err := srv.StartBroadcast(context.Background(), BroadcastRequest{ZoneID: "z3", MediaID: item.ID, Priority: 2})
	if err != nil {
		t.Fatalf("start media broadcast: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
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
		time.Sleep(50 * time.Millisecond)
	}
	if final.State != "finished" || final.Delivered != 1 || final.Source != "media" {
		t.Fatalf("state=%q delivered=%d source=%q", final.State, final.Delivered, final.Source)
	}
	if count.Load() == 0 {
		t.Fatal("no RTP packets")
	}
	if !srv.DeleteMedia(item.ID) {
		t.Fatal("delete media failed")
	}
}
