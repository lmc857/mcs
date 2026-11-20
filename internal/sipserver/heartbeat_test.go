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

func TestHeartbeatOptions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ua, err := sipgo.NewUA()
	if err != nil {
		t.Fatal(err)
	}
	defer ua.Close()
	uas, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatal(err)
	}
	uas.OnOptions(func(req *sip.Request, tx sip.ServerTransaction) {
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		_ = tx.Respond(res)
	})
	go func() { _ = uas.ListenAndServe(ctx, "udp", "127.0.0.1:15074") }()
	time.Sleep(150 * time.Millisecond)
	defer uas.Close()

	st := store.New([]config.DeviceConfig{{Ext: "1001", Name: "test", Password: "pass-1001"}})
	srv, err := New(st, NewAuthenticator("mcs", "secret"), store.NewCDRStore(10), store.NewAlarmStore(10), Options{
		Domain: "mcs.local", Advertise: "127.0.0.1:15075", DataDir: t.TempDir(),
		Registration: config.RegistrationConfig{MaxExpiresS: 60, DefaultExpiresS: 60, Heartbeat: config.HeartbeatConfig{Enabled: true, IntervalS: 5, TimeoutS: 1, Failures: 1}},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if !st.Register("1001", "<sip:1001@127.0.0.1:15074>", "127.0.0.1:15074", "", "", "", time.Minute) {
		t.Fatal("register failed")
	}
	view, _ := st.Get("1001")
	srv.heartbeatOne(ctx, view, time.Second, 1)
	if v, _ := st.Get("1001"); !v.Online {
		t.Fatal("device should stay online after successful OPTIONS")
	}

	// Stop the fake UAS and force a failure.
	cancel()
	_ = uas.Close()
	time.Sleep(100 * time.Millisecond)
	view, _ = st.Get("1001")
	srv.heartbeatOne(ctx, view, 100*time.Millisecond, 1)
	if v, _ := st.Get("1001"); v.Online {
		t.Fatal("device should be offline after failed OPTIONS")
	}
}
