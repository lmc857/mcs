package sipserver

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mcs/internal/config"
	"mcs/internal/store"
)

func TestRegisterDigestFlow(t *testing.T) {
	st := store.New([]config.DeviceConfig{{
		Ext: "1001", Name: "值班室", Password: "pass-1001",
	}})
	auth := NewAuthenticator("mcs", "test-secret")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := New(st, auth, store.NewCDRStore(10), store.NewAlarmStore(10), Options{
		Domain: "mcs.local", Advertise: "127.0.0.1:15071", DataDir: t.TempDir(),
		Alarm: config.AlarmConfig{Number: "8000"},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, "127.0.0.1:15071") }()
	defer srv.Close()
	time.Sleep(150 * time.Millisecond)

	ua, err := sipgo.NewUA()
	if err != nil {
		t.Fatal(err)
	}
	defer ua.Close()
	client, err := sipgo.NewClient(ua)
	if err != nil {
		t.Fatal(err)
	}

	recipient := sip.Uri{Scheme: "sip", Host: "127.0.0.1", Port: 15071}
	req := sip.NewRequest(sip.REGISTER, recipient)
	req.AppendHeader(sip.NewHeader("From", "<sip:1001@mcs.local>;tag=regtest"))
	req.AppendHeader(sip.NewHeader("To", "<sip:1001@mcs.local>"))
	req.AppendHeader(sip.NewHeader("Call-ID", "regtest-1"))
	req.AppendHeader(sip.NewHeader("CSeq", "1 REGISTER"))
	req.AppendHeader(sip.NewHeader("Contact", "<sip:1001@127.0.0.1:5099>"))
	req.AppendHeader(sip.NewHeader("Expires", "60"))
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))

	res, err := client.Do(ctx, req)
	if err != nil {
		t.Fatalf("initial register: %v", err)
	}
	if res.StatusCode != 401 {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}

	res, err = client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{Username: "1001", Password: "pass-1001"})
	if err != nil {
		t.Fatalf("digest register: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, want 200: %s", res.StatusCode, res.String())
	}

	views := st.List()
	if len(views) != 1 || !views[0].Online {
		t.Fatalf("expected one online device, got %#v", views)
	}
	if views[0].Contact == "" {
		t.Fatalf("contact not stored: %#v", views[0])
	}
}

func TestRegisterWrongPassword(t *testing.T) {
	st := store.New([]config.DeviceConfig{{Ext: "1001", Password: "pass-1001"}})
	auth := NewAuthenticator("mcs", "test-secret")
	srv, err := New(st, auth, store.NewCDRStore(10), store.NewAlarmStore(10), Options{
		Domain: "mcs.local", Advertise: "127.0.0.1:15072", DataDir: t.TempDir(),
		Alarm: config.AlarmConfig{Number: "8000"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, "127.0.0.1:15072") }()
	defer srv.Close()
	time.Sleep(150 * time.Millisecond)

	ua, _ := sipgo.NewUA()
	defer ua.Close()
	client, _ := sipgo.NewClient(ua)
	req := sip.NewRequest(sip.REGISTER, sip.Uri{Scheme: "sip", Host: "127.0.0.1", Port: 15072})
	req.AppendHeader(sip.NewHeader("From", "<sip:1001@mcs.local>;tag=bad"))
	req.AppendHeader(sip.NewHeader("To", "<sip:1001@mcs.local>"))
	req.AppendHeader(sip.NewHeader("Call-ID", "regtest-2"))
	req.AppendHeader(sip.NewHeader("CSeq", "1 REGISTER"))
	req.AppendHeader(sip.NewHeader("Contact", "<sip:1001@127.0.0.1:5099>"))
	req.AppendHeader(sip.NewHeader("Expires", "60"))

	res, err := client.Do(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 401 {
		t.Fatalf("first status = %d", res.StatusCode)
	}
	res, err = client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{Username: "1001", Password: "wrong"})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 401 {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
	if v := st.List(); v[0].Online {
		t.Fatalf("device should be offline")
	}
}
