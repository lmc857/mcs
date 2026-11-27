package sipserver

import (
	"context"
	"fmt"
	"time"

	"github.com/emiago/sipgo/sip"

	"mcs/internal/store"
)

// StartHeartbeat periodically probes online registrations with SIP OPTIONS.
// It is disabled by default because some devices/NAT paths do not answer
// server-initiated OPTIONS reliably.
func (s *Server) StartHeartbeat(ctx context.Context) {
	cfg := s.registrationCfg.Heartbeat
	if !cfg.Enabled {
		return
	}
	interval := time.Duration(cfg.IntervalS) * time.Second
	if interval < 5*time.Second {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	s.log.Info("registration heartbeat enabled", "interval", interval, "timeout_s", cfg.TimeoutS, "failures", cfg.Failures)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.heartbeatRound(ctx)
		}
	}
}

func (s *Server) heartbeatRound(ctx context.Context) {
	cfg := s.registrationCfg.Heartbeat
	timeout := time.Duration(cfg.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	failLimit := cfg.Failures
	if failLimit <= 0 {
		failLimit = 3
	}
	devices := s.store.List()
	sem := make(chan struct{}, 8)
	for _, d := range devices {
		if !d.Online || d.Contact == "" {
			continue
		}
		d := d
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			s.heartbeatOne(ctx, d, timeout, failLimit)
		}()
	}
}

func (s *Server) heartbeatOne(ctx context.Context, d store.View, timeout time.Duration, failLimit int) {
	uri, err := contactURIForView(d)
	if err != nil {
		s.recordHeartbeatFailure(d, failLimit, err)
		return
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req := sip.NewRequest(sip.OPTIONS, uri)
	req.AppendHeader(sip.NewHeader("From", fmt.Sprintf("<sip:mcs@%s>;tag=hb-%d", s.domain, time.Now().UnixNano())))
	req.AppendHeader(sip.NewHeader("To", fmt.Sprintf("<sip:%s@%s>", d.Ext, s.domain)))
	req.AppendHeader(sip.NewHeader("Call-ID", fmt.Sprintf("hb-%d-%s", time.Now().UnixNano(), d.Ext)))
	req.AppendHeader(sip.NewHeader("CSeq", "1 OPTIONS"))
	req.AppendHeader(sip.NewHeader("Max-Forwards", "69"))
	req.AppendHeader(s.contactForTransport(transportForView(d, uri)))
	req.AppendHeader(sip.NewHeader("User-Agent", serverName))
	res, err := s.client.Do(hctx, req)
	if err != nil || res == nil {
		s.recordHeartbeatFailure(d, failLimit, err)
		return
	}
	// Any SIP response (200, 401, 403, 405, ...) proves the endpoint is alive.
	s.hbMu.Lock()
	delete(s.hbFailures, d.Ext)
	s.hbMu.Unlock()
	ttl := s.registrationCfg.MaxExpiresS
	if ttl <= 0 {
		ttl = 300
	}
	s.store.Touch(d.Ext, time.Duration(ttl)*time.Second)
}

func (s *Server) recordHeartbeatFailure(d store.View, failLimit int, err error) {
	s.hbMu.Lock()
	s.hbFailures[d.Ext]++
	n := s.hbFailures[d.Ext]
	if n >= failLimit {
		delete(s.hbFailures, d.Ext)
	}
	s.hbMu.Unlock()
	if n < failLimit {
		return
	}
	s.store.Unregister(d.Ext)
	s.log.Warn("device heartbeat lost", "ext", d.Ext, "contact", d.Contact, "source", d.Source, "failures", n, "error", err)
	s.emit("device.unregistered", map[string]any{"ext": d.Ext, "source": d.Source, "reason": "heartbeat"})
}

func (s *Server) resetHeartbeat(ext string) {
	s.hbMu.Lock()
	delete(s.hbFailures, ext)
	s.hbMu.Unlock()
}
