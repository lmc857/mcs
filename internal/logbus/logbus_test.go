package logbus

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestHubAndHandler(t *testing.T) {
	hub := New(3)
	ch, unsub := hub.Subscribe()
	defer unsub()
	logger := slog.New(NewHandler(hub, slog.NewTextHandler(io.Discard, nil)))
	logger.Info("call rejected", "call_id", "mcs-1", "code", 486)

	select {
	case e := <-ch:
		if e.Message != "call rejected" || e.Attrs["code"] != int64(486) {
			t.Fatalf("entry = %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no live log")
	}
	recent := hub.Recent()
	if len(recent) != 1 || recent[0].Level != "INFO" {
		t.Fatalf("recent = %+v", recent)
	}
	for i := 0; i < 5; i++ {
		logger.Info("x", "i", i)
	}
	if got := len(hub.Recent()); got != 3 {
		t.Fatalf("ring length = %d", got)
	}
}

func TestHandlerGroup(t *testing.T) {
	hub := New(10)
	logger := slog.New(NewHandler(hub, slog.NewTextHandler(io.Discard, nil)))
	logger.With("call_id", "c1").WithGroup("sip").Info("busy", "code", 486)
	e := hub.Recent()[0]
	if e.Attrs["call_id"] != "c1" || e.Attrs["sip.code"] != int64(486) {
		t.Fatalf("attrs = %+v", e.Attrs)
	}
	_ = context.Background()
}
