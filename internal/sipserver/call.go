package sipserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mcs/internal/store"
)

type CallView struct {
	CallID    string `json:"call_id"`
	From      string `json:"from"`
	To        string `json:"to"`
	State     string `json:"state"`
	StartedAt string `json:"started_at"`
	Video     bool   `json:"video"`
	Alarm     bool   `json:"alarm"`
}

type alarmTarget struct {
	view      store.View
	level     int
	levelName string
	timeout   time.Duration
}

type call struct {
	id         string
	aCallID    string
	bCallID    string
	from       string
	to         string
	a          *sipgo.DialogServerSession
	b          *sipgo.DialogClientSession
	relayA     *relay
	relayB     *relay
	videoA     *relay
	videoB     *relay
	audioTap   *mediaTap
	videoTap   *mediaTap
	rec        *recorder
	codecA     string
	codecB     string
	audioPT    uint8
	startAt    time.Time
	answerAt   time.Time
	result     string
	lastCode   int
	lastReason string
	lastError  string
	targets    []alarmTarget
	attempt    int
	level      int
	alarmID    string
	ctx        context.Context
	cancel     context.CancelFunc
	once       sync.Once
	stateMu    sync.RWMutex
	server     *Server
}

type callRegistry struct {
	mu sync.RWMutex
	m  map[string]*call
}

func newCallRegistry() *callRegistry { return &callRegistry{m: map[string]*call{}} }

func (r *callRegistry) add(c *call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.aCallID != "" {
		r.m[c.aCallID] = c
	}
	if c.bCallID != "" {
		r.m[c.bCallID] = c
	}
}

func (r *callRegistry) addID(id string, c *call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[id] = c
}

func (r *callRegistry) removeID(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, id)
}

func (r *callRegistry) get(id string) *call {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.m[id]
}

func (r *callRegistry) remove(c *call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, c.aCallID)
	delete(r.m, c.bCallID)
}

func (r *callRegistry) getByID(id string) *call {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[*call]bool{}
	for _, c := range r.m {
		if !seen[c] {
			seen[c] = true
			if c.id == id {
				return c
			}
		}
	}
	return nil
}

func (r *callRegistry) list() []*call {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[*call]bool{}
	out := make([]*call, 0, len(r.m))
	for _, c := range r.m {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

var callSeq atomic.Uint64

func (s *Server) handleInvite(req *sip.Request, tx sip.ServerTransaction) {
	from := ""
	if f := req.From(); f != nil {
		from = f.Address.User
	}
	to := req.Recipient.User
	if room, ok := s.conference.find(to); ok {
		s.handleConferenceInvite(req, tx, room)
		return
	}
	if z, ok := s.findZoneByDial(to); ok {
		s.handlePagingInvite(req, tx, z)
		return
	}
	targets := s.resolveTargets(to, from)
	if len(targets) == 0 {
		s.respond(req, tx, 404, "Not Found")
		s.log.Info("invite rejected: target not found", "from", from, "to", to)
		return
	}
	body := req.Body()
	if len(body) == 0 {
		s.respond(req, tx, 488, "Not Acceptable Here")
		return
	}
	offer := parseSDP(body)
	if !offer.HasAudio || offer.AudioPort == 0 {
		s.respond(req, tx, 488, "Not Acceptable Here")
		return
	}
	if err := s.checkSRTPPolicy(offer); err != nil {
		s.respond(req, tx, 488, err.Error())
		return
	}
	dss, err := s.dua.ReadInvite(req, tx)
	if err != nil {
		s.log.Warn("read invite", "error", err, "from", from)
		s.respond(req, tx, 400, "Bad Request")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	c := &call{
		id:      fmt.Sprintf("mcs-%d-%d", now.UnixNano(), callSeq.Add(1)),
		aCallID: dss.InviteRequest.CallID().Value(),
		from:    from,
		to:      targets[0].view.Ext,
		a:       dss,
		startAt: now,
		targets: targets,
		ctx:     ctx,
		cancel:  cancel,
		server:  s,
	}
	s.calls.add(c)

	if s.isAlarm(to) {
		ev := s.alarms.Create(store.Alarm{
			CallID: c.id, From: from, To: targets[0].view.Ext, AlarmNumber: to,
			DeviceName: targets[0].view.Name, Zone: targets[0].view.Zone,
			State: "ringing", Level: targets[0].level, LevelName: targets[0].levelName,
			StartedAt: now.Format(time.RFC3339),
		})
		c.alarmID = ev.ID
		s.emit("alarm.created", map[string]any{
			"alarm_id": ev.ID, "call_id": c.id, "from": from, "to": targets[0].view.Ext,
			"alarm_number": to, "device_name": targets[0].view.Name, "zone": targets[0].view.Zone,
			"level": targets[0].level, "level_name": targets[0].levelName,
			"started_at": now.Format(time.RFC3339),
		})
	}
	s.log.Info("call incoming", "call_id", c.id, "from", from, "to", c.to, "targets", len(targets), "source", req.Source())
	s.emit("call.incoming", CallView{CallID: c.id, From: from, To: c.to, State: "ringing", StartedAt: now.Format(time.RFC3339)})
	_ = dss.Respond(100, "Trying", nil)
	// Keep the INVITE transaction alive until the call ends: sipgo terminates
	// the server transaction as soon as the handler returns.
	s.bridge(c, offer)
}

func (s *Server) isAlarm(to string) bool {
	return to == s.alarmCfg.Number || strings.HasPrefix(to, "8")
}

func (s *Server) resolveTargets(to, from string) []alarmTarget {
	if s.isAlarm(to) {
		var out []alarmTarget
		seen := map[string]bool{}
		for lvl, level := range s.alarmCfg.Levels {
			timeout := time.Duration(level.TimeoutS) * time.Second
			if timeout <= 0 {
				timeout = 20 * time.Second
			}
			for _, ext := range level.Members {
				if ext == from || seen[ext] {
					continue
				}
				if v, ok := s.store.Get(ext); ok && v.Online {
					out = append(out, alarmTarget{view: v, level: lvl, levelName: level.Name, timeout: timeout})
					seen[ext] = true
				}
			}
		}
		if len(out) == 0 {
			for _, v := range s.store.List() {
				if v.Online && v.Ext != from {
					out = append(out, alarmTarget{view: v, level: 0, levelName: "默认值班", timeout: 20 * time.Second})
					break
				}
			}
		}
		return out
	}
	if v, ok := s.store.Get(to); ok && v.Online && v.Ext != from {
		return []alarmTarget{{view: v, timeout: 60 * time.Second}}
	}
	return nil
}

func (s *Server) bridge(c *call, offer sdpInfo) {
	result := "no_answer"
	for i, t := range c.targets {
		if c.ctx.Err() != nil {
			c.finish("canceled")
			return
		}
		c.attempt = i
		if i > 0 {
			c.level = t.level
			c.stateMu.Lock()
			c.to = t.view.Ext
			c.stateMu.Unlock()
			if c.alarmID != "" {
				c.server.alarms.Update(c.alarmID, func(a *store.Alarm) {
					if a.State == "closed" {
						return
					}
					a.State = "ringing"
					a.Level = t.level
					a.LevelName = t.levelName
					a.To = t.view.Ext
				})
				c.server.emit("alarm.escalated", map[string]any{
					"alarm_id": c.alarmID, "call_id": c.id, "level": t.level,
					"level_name": t.levelName, "to": t.view.Ext,
					"device_name": t.view.Name, "zone": t.view.Zone,
				})
			}
			c.server.log.Info("call escalated", "call_id", c.id, "level", t.level, "to", t.view.Ext)
		}
		ok, res := c.tryTarget(t, offer)
		if ok {
			<-c.ctx.Done()
			c.finish("")
			return
		}
		result = res
		if c.ctx.Err() != nil {
			c.finish("canceled")
			return
		}
	}

	c.stateMu.RLock()
	lastCode, lastReason, lastError := c.lastCode, c.lastReason, c.lastError
	c.stateMu.RUnlock()
	code, reason := 480, "Temporarily Unavailable"
	if lastCode > 0 {
		code, reason = lastCode, lastReason
	}
	if code == 401 || code == 407 {
		// The B-leg challenge is between MCS and the callee, not the caller.
		code, reason = 480, "Temporarily Unavailable"
	}
	if result == "busy" {
		code, reason = 486, "Busy Here"
	}
	if reason == "" {
		reason = "Temporarily Unavailable"
	}
	if c.ctx.Err() == nil {
		_ = c.a.Respond(code, reason, nil, c.aContactHeader())
	}
	s.log.Info("call rejected", "call_id", c.id, "from", c.from, "to", c.to, "result", result,
		"code", code, "reason", reason, "targets", len(c.targets),
		"b_leg_code", lastCode, "b_leg_reason", lastReason, "b_leg_error", lastError)
	c.finish(result)
}

func (c *call) tryTarget(t alarmTarget, offer sdpInfo) (bool, string) {
	s := c.server
	ra, err := newRelay()
	if err != nil {
		return c.localFailure(t, "rtp-relay-a", err)
	}
	rb, err := newRelay()
	if err != nil {
		ra.close()
		return c.localFailure(t, "rtp-relay-b", err)
	}
	rec, err := newRecorder(s.dataDir, c.id)
	if err != nil {
		ra.close()
		rb.close()
		return c.localFailure(t, "recorder", err)
	}
	c.relayA, c.relayB, c.rec = ra, rb, rec
	ra.transcode = s.mediaTranscode
	rb.transcode = s.mediaTranscode
	aMediaIP := mediaIPForSource(offer.ConnIP, c.a.InviteRequest.Source())
	s.log.Info("a-leg media", "call_id", c.id, "sdp_ip", offer.ConnIP, "peer_ip", aMediaIP, "port", offer.AudioPort)
	ra.setPeerRTCP(aMediaIP, offer.AudioPort, offer.AudioRTCPPort, offer.AudioRTCPMux)
	c.codecA = codecFor(offer)
	c.audioPT = offer.PayloadType
	ra.codec = c.codecA

	hasVideo := offer.HasVideo && offer.VideoPort > 0
	var va, vb *relay
	if hasVideo {
		va, err = newRelay()
		if err != nil {
			c.cleanupAttempt()
			return c.localFailure(t, "rtp-relay-video-a", err)
		}
		vb, err = newRelay()
		if err != nil {
			va.close()
			c.cleanupAttempt()
			return c.localFailure(t, "rtp-relay-video-b", err)
		}
		c.videoA, c.videoB = va, vb
	}
	videoPortB := 0
	if hasVideo {
		videoPortB = vb.port()
	}

	secureAudio := offer.AudioSecure
	secureVideo := hasVideo && offer.VideoSecure
	var aAudioKey, bAudioKey, aVideoKey, bVideoKey *srtpKey
	if secureAudio {
		if !offer.AudioCrypto.OK {
			c.cleanupAttempt()
			return c.localFailure(t, "srtp-offer-audio", fmt.Errorf("SAVP audio without a=crypto"))
		}
		if aAudioKey, err = newSRTPKeyFor(offer.AudioCrypto.Suite); err != nil {
			c.cleanupAttempt()
			return c.localFailure(t, "srtp-key-audio-a", err)
		}
		if bAudioKey, err = newSRTPKeyFor(offer.AudioCrypto.Suite); err != nil {
			c.cleanupAttempt()
			return c.localFailure(t, "srtp-key-audio-b", err)
		}
	}
	if secureVideo {
		if !offer.VideoCrypto.OK {
			c.cleanupAttempt()
			return c.localFailure(t, "srtp-offer-video", fmt.Errorf("SAVP video without a=crypto"))
		}
		if aVideoKey, err = newSRTPKeyFor(offer.VideoCrypto.Suite); err != nil {
			c.cleanupAttempt()
			return c.localFailure(t, "srtp-key-video-a", err)
		}
		if bVideoKey, err = newSRTPKeyFor(offer.VideoCrypto.Suite); err != nil {
			c.cleanupAttempt()
			return c.localFailure(t, "srtp-key-video-b", err)
		}
	}
	audioCryptoB, videoCryptoB := "", ""
	if secureAudio {
		audioCryptoB = bAudioKey.SDP
	}
	if secureVideo {
		videoCryptoB = bVideoKey.SDP
	}
	sdpB, err := rewriteSDPWithCrypto(c.a.InviteRequest.Body(), rb.port(), videoPortB, s.advertiseHost, audioCryptoB, videoCryptoB)
	if err != nil {
		c.cleanupAttempt()
		return c.localFailure(t, "sdp-rewrite", err)
	}
	uri, err := contactURIForView(t.view)
	if err != nil {
		c.cleanupAttempt()
		return c.localFailure(t, "contact-uri", err)
	}
	caller := c.from
	if caller == "" {
		caller = "anonymous"
	}
	bID := fmt.Sprintf("%s-b%d", c.id, c.attempt)
	c.stateMu.Lock()
	c.bCallID = bID
	c.stateMu.Unlock()
	s.log.Info("b-leg invite", "call_id", c.id, "to", t.view.Ext, "uri", uri.String(), "contact", t.view.Contact, "source", t.view.Source)
	reqB := sip.NewRequest(sip.INVITE, uri)
	reqB.AppendHeader(sip.NewHeader("From", fmt.Sprintf("<sip:%s@%s>;tag=%d", caller, s.domain, time.Now().UnixNano())))
	reqB.AppendHeader(sip.NewHeader("To", fmt.Sprintf("<sip:%s@%s>", t.view.Ext, s.domain)))
	reqB.AppendHeader(sip.NewHeader("Call-ID", bID))
	reqB.AppendHeader(sip.NewHeader("CSeq", "1 INVITE"))
	reqB.AppendHeader(sip.NewHeader("Max-Forwards", "69"))
	reqB.AppendHeader(s.contactForTransport(transportForView(t.view, uri)))
	reqB.AppendHeader(sip.NewHeader("User-Agent", serverName))
	reqB.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	reqB.SetBody(sdpB)

	timeout := t.timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()

	dcs, err := s.dua.WriteInvite(ctx, reqB)
	if err != nil {
		res := c.failureResult(err, ctx)
		s.log.Info("b-leg invite failed", "call_id", c.id, "to", t.view.Ext, "result", res, "error", err)
		c.cleanupAttempt()
		return false, res
	}
	c.stateMu.Lock()
	c.b = dcs
	c.stateMu.Unlock()
	s.calls.addID(bID, c)

	err = dcs.WaitAnswer(ctx, sipgo.AnswerOptions{
		OnResponse: func(res *sip.Response) error {
			if res.StatusCode == 100 {
				return nil
			}
			s.log.Info("b-leg response", "call_id", c.id, "to", t.view.Ext, "code", res.StatusCode, "reason", res.Reason)
			if !res.IsProvisional() {
				c.stateMu.Lock()
				c.lastCode, c.lastReason = res.StatusCode, res.Reason
				c.stateMu.Unlock()
			}
			if res.IsProvisional() {
				body := res.Body()
				if len(body) > 0 {
					videoPortA := 0
					if hasVideo {
						videoPortA = va.port()
					}
					audioCryptoA, videoCryptoA := "", ""
					if aAudioKey != nil {
						audioCryptoA = aAudioKey.SDP
					}
					if aVideoKey != nil {
						videoCryptoA = aVideoKey.SDP
					}
					if rewritten, err := rewriteSDPWithCrypto(body, ra.port(), videoPortA, s.advertiseHost, audioCryptoA, videoCryptoA); err == nil {
						body = rewritten
					}
				}
				if err := c.a.Respond(res.StatusCode, res.Reason, body, c.aContactHeader()); err != nil {
					if c.ctx.Err() == nil {
						s.log.Warn("forward provisional failed", "call_id", c.id, "error", err)
					}
					return err
				}
				return nil
			}
			return nil
		},
	})
	if err != nil {
		res := c.failureResult(err, ctx)
		s.log.Info("b-leg attempt failed", "call_id", c.id, "to", t.view.Ext, "result", res, "error", err)
		c.cleanupAttempt()
		return false, res
	}

	answer := dcs.InviteResponse.Body()
	infoB := parseSDP(answer)
	bMediaIP := mediaIPForSource(infoB.ConnIP, t.view.Source)
	s.log.Info("b-leg media", "call_id", c.id, "sdp_ip", infoB.ConnIP, "peer_ip", bMediaIP, "port", infoB.AudioPort)
	rb.setPeerRTCP(bMediaIP, infoB.AudioPort, infoB.AudioRTCPPort, infoB.AudioRTCPMux)
	c.codecB = codecFor(infoB)
	rb.codec = c.codecB
	s.log.Info("call codecs", "call_id", c.id, "a", c.codecA, "b", c.codecB, "transcode", s.mediaTranscode)
	videoOK := hasVideo && infoB.HasVideo && infoB.VideoPort > 0
	videoPortA := 0
	if videoOK {
		videoPortA = va.port()
		s.log.Info("call video", "call_id", c.id, "a_pt", offer.VideoPT, "b_pt", infoB.VideoPT,
			"a_rtcp_mux", offer.VideoRTCPMux, "b_rtcp_mux", infoB.VideoRTCPMux)
		// The two SDP answers may use different dynamic H264 payload types.
		// Rewrite the RTP PT per leg; otherwise the receiver sees a PT it did
		// not negotiate and may drop or mis-decode the video.
		if infoB.VideoPT != 0 {
			va.setOutPT(infoB.VideoPT)
		}
		if offer.VideoPT != 0 {
			vb.setOutPT(offer.VideoPT)
		}
	} else if va != nil {
		va.close()
		vb.close()
		c.videoA, c.videoB = nil, nil
	}
	if !secureAudio && infoB.AudioSecure {
		c.cleanupAttempt()
		return false, "failed"
	}
	if secureAudio {
		if !infoB.AudioSecure || !infoB.AudioCrypto.OK {
			c.cleanupAttempt()
			return false, "failed"
		}
		rbIn, err := parseSRTPKey(infoB.AudioCrypto)
		if err != nil {
			c.cleanupAttempt()
			return false, "failed"
		}
		raIn, err := parseSRTPKey(offer.AudioCrypto)
		if err != nil {
			c.cleanupAttempt()
			return false, "failed"
		}
		if rb.rx, err = rbIn.context(); err != nil {
			c.cleanupAttempt()
			return false, "failed"
		}
		if rb.tx, err = bAudioKey.context(); err != nil {
			c.cleanupAttempt()
			return false, "failed"
		}
		if ra.rx, err = raIn.context(); err != nil {
			c.cleanupAttempt()
			return false, "failed"
		}
		if ra.tx, err = aAudioKey.context(); err != nil {
			c.cleanupAttempt()
			return false, "failed"
		}
	}
	if !secureVideo && videoOK && infoB.VideoSecure {
		c.cleanupAttempt()
		return false, "failed"
	}
	if secureVideo && videoOK {
		if !infoB.VideoSecure || !infoB.VideoCrypto.OK {
			c.cleanupAttempt()
			return false, "failed"
		}
		vbIn, err := parseSRTPKey(infoB.VideoCrypto)
		if err != nil {
			c.cleanupAttempt()
			return false, "failed"
		}
		vaIn, err := parseSRTPKey(offer.VideoCrypto)
		if err != nil {
			c.cleanupAttempt()
			return false, "failed"
		}
		if vb.rx, err = vbIn.context(); err != nil {
			c.cleanupAttempt()
			return false, "failed"
		}
		if vb.tx, err = bVideoKey.context(); err != nil {
			c.cleanupAttempt()
			return false, "failed"
		}
		if va.rx, err = vaIn.context(); err != nil {
			c.cleanupAttempt()
			return false, "failed"
		}
		if va.tx, err = aVideoKey.context(); err != nil {
			c.cleanupAttempt()
			return false, "failed"
		}
	}
	audioCryptoA, videoCryptoA := "", ""
	if secureAudio {
		audioCryptoA = aAudioKey.SDP
	}
	if secureVideo && videoOK {
		videoCryptoA = aVideoKey.SDP
	}
	sdpA, err := rewriteSDPWithCrypto(answer, ra.port(), videoPortA, s.advertiseHost, audioCryptoA, videoCryptoA)
	if err != nil {
		c.cleanupAttempt()
		return false, "failed"
	}

	var tap *mediaTap
	if videoOK {
		tap = newMediaTap()
	}
	answeredAt := time.Now()
	c.stateMu.Lock()
	c.to = t.view.Ext
	c.answerAt = answeredAt
	c.result = "answered"
	c.videoTap = tap
	c.stateMu.Unlock()
	audioTap := newMediaTap()
	c.stateMu.Lock()
	c.audioTap = audioTap
	c.stateMu.Unlock()
	ra.startWithTap(rb, func(payload []byte, pt uint8) { c.rec.writeA(payload, c.codecA) }, audioTap.push)
	rb.start(ra, func(payload []byte, pt uint8) { c.rec.writeB(payload, c.codecB) })
	if videoOK {
		va.setPeerRTCP(mediaIPForSource(offer.ConnIP, c.a.InviteRequest.Source()), offer.VideoPort, offer.VideoRTCPPort, offer.VideoRTCPMux)
		vb.setPeerRTCP(mediaIPForSource(infoB.ConnIP, t.view.Source), infoB.VideoPort, infoB.VideoRTCPPort, infoB.VideoRTCPMux)
		va.startWithTap(vb, nil, tap.push)
		vb.start(va, nil)
	}

	// Respond with a transport-correct Contact (sips for a TLS caller).
	// RespondSDP waits for the caller's ACK; keep it off the media path.
	go func() {
		res := sip.NewSDPResponseFromRequest(c.a.InviteRequest, sdpA)
		res.AppendHeader(c.aContactHeader())
		if err := c.a.WriteResponse(res); err != nil && c.ctx.Err() == nil {
			s.log.Warn("answer/ack failed", "call_id", c.id, "error", err)
		}
	}()
	if err := dcs.Ack(c.ctx); err != nil && c.ctx.Err() == nil {
		s.log.Warn("ack callee failed", "call_id", c.id, "error", err)
	}
	if c.alarmID != "" {
		s.alarms.Update(c.alarmID, func(a *store.Alarm) {
			if a.State == "closed" {
				return
			}
			a.State = "answered"
			a.AnsweredAt = answeredAt.Format(time.RFC3339)
			a.AnsweredBy = t.view.Ext
			a.To = t.view.Ext
		})
		s.emit("alarm.answered", map[string]any{
			"alarm_id": c.alarmID, "call_id": c.id, "answered_by": t.view.Ext,
			"ts": time.Now().Format(time.RFC3339),
		})
	}
	s.log.Info("call answered", "call_id", c.id, "from", c.from, "to", t.view.Ext)
	s.emit("call.answered", map[string]any{"call_id": c.id, "from": c.from, "to": t.view.Ext, "ts": time.Now().Format(time.RFC3339)})
	return true, "answered"
}

func (c *call) failureResult(err error, ctx context.Context) string {
	if c.ctx.Err() != nil {
		return "canceled"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "no_answer"
	}
	if err != nil {
		c.stateMu.Lock()
		c.lastError = err.Error()
		c.stateMu.Unlock()
	}
	var dialogErr *sipgo.ErrDialogResponse
	if errors.As(err, &dialogErr) && dialogErr.Res != nil {
		code, reason := dialogErr.Res.StatusCode, dialogErr.Res.Reason
		c.stateMu.Lock()
		c.lastCode, c.lastReason = code, reason
		c.stateMu.Unlock()
		switch code {
		case 486, 600:
			return "busy"
		case 408, 480:
			return "no_answer"
		case 603:
			return "rejected"
		}
	}
	return "failed"
}

func (c *call) cleanupAttempt() {
	c.stateMu.Lock()
	b := c.b
	c.b = nil
	bID := c.bCallID
	c.bCallID = ""
	tap := c.videoTap
	c.videoTap = nil
	c.stateMu.Unlock()
	if b != nil {
		_ = b.Close()
	}
	if bID != "" {
		c.server.calls.removeID(bID)
	}
	if c.relayA != nil {
		c.relayA.close()
		c.relayA = nil
	}
	if c.relayB != nil {
		c.relayB.close()
		c.relayB = nil
	}
	if c.videoA != nil {
		c.videoA.close()
		c.videoA = nil
	}
	if c.videoB != nil {
		c.videoB.close()
		c.videoB = nil
	}
	if tap != nil {
		tap.close()
	}
	c.stateMu.Lock()
	audioTap := c.audioTap
	c.audioTap = nil
	c.stateMu.Unlock()
	if audioTap != nil {
		audioTap.close()
	}
	if c.rec != nil {
		_ = c.rec.close()
		c.rec = nil
	}
}

func (c *call) hangup(fromCaller bool) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		c.stateMu.RLock()
		b := c.b
		a := c.a
		c.stateMu.RUnlock()
		if fromCaller {
			if b != nil {
				_ = b.Bye(ctx)
			}
		} else if a != nil {
			_ = a.Bye(ctx)
		}
		c.finish("")
	}()
}

func (c *call) finish(result string) {
	c.once.Do(func() {
		c.stateMu.Lock()
		if result != "" && c.result != "answered" {
			c.result = result
		}
		if c.result == "" {
			c.result = "failed"
		}
		resultValue := c.result
		answeredAt := c.answerAt
		to := c.to
		tap := c.videoTap
		c.stateMu.Unlock()
		var aRX, aTX, bRX, bTX uint64
		if c.relayA != nil {
			aRX, aTX = c.relayA.rxRTP.Load(), c.relayA.txRTP.Load()
		}
		if c.relayB != nil {
			bRX, bTX = c.relayB.rxRTP.Load(), c.relayB.txRTP.Load()
		}
		c.server.log.Info("rtp stats", "call_id", c.id, "a_rx", aRX, "a_tx", aTX, "b_rx", bRX, "b_tx", bTX)
		if c.relayA != nil {
			c.relayA.close()
		}
		if c.relayB != nil {
			c.relayB.close()
		}
		if c.videoA != nil {
			c.videoA.close()
		}
		if c.videoB != nil {
			c.videoB.close()
		}
		if tap != nil {
			tap.close()
		}
		c.stateMu.Lock()
		audioTap := c.audioTap
		c.audioTap = nil
		c.stateMu.Unlock()
		if audioTap != nil {
			audioTap.close()
		}
		recording := ""
		if c.rec != nil {
			recording = c.rec.close()
		}
		end := time.Now()
		duration := 0
		if !answeredAt.IsZero() {
			duration = int(end.Sub(answeredAt).Seconds())
			if duration < 0 {
				duration = 0
			}
		}
		c.stateMu.RLock()
		lastCode, lastReason := c.lastCode, c.lastReason
		c.stateMu.RUnlock()
		cdr := store.CDR{
			ID:         c.id,
			CallID:     c.id,
			From:       c.from,
			To:         to,
			StartAt:    c.startAt.Format(time.RFC3339),
			EndAt:      end.Format(time.RFC3339),
			Duration:   duration,
			Result:     resultValue,
			BLegCode:   lastCode,
			BLegReason: lastReason,
			Recording:  recording,
		}
		if !answeredAt.IsZero() {
			cdr.AnswerAt = answeredAt.Format(time.RFC3339)
		}
		c.server.cdr.Add(cdr)
		c.server.emit("call.ended", cdr)
		if c.alarmID != "" {
			state := "unanswered"
			if resultValue == "answered" {
				state = "pending"
			}
			c.server.alarms.Update(c.alarmID, func(a *store.Alarm) {
				if a.State != "closed" {
					a.State = state
				}
				a.EndedAt = end.Format(time.RFC3339)
				a.Recording = recording
			})
			c.server.emit("alarm.updated", map[string]any{
				"alarm_id": c.alarmID, "call_id": c.id, "state": state,
				"result": resultValue, "recording": recording,
			})
		}
		c.server.calls.remove(c)
		c.cancel()
		c.server.log.Info("call ended", "call_id", c.id, "result", resultValue, "duration_s", duration, "recording", recording)
	})
}

func (s *Server) handleBye(req *sip.Request, tx sip.ServerTransaction) {
	id := req.CallID().Value()
	if p := s.conference.byCallID(id); p != nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
		p.stop(false)
		return
	}
	if ps := s.pagingGet(id); ps != nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
		ps.stop()
		return
	}
	if s.bc != nil && s.bc.handleBye(req, tx) {
		return
	}
	c := s.calls.get(id)
	if c == nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil))
		return
	}
	c.stateMu.RLock()
	bID := c.bCallID
	b := c.b
	c.stateMu.RUnlock()
	switch id {
	case c.aCallID:
		if err := c.a.ReadBye(req, tx); err != nil {
			s.log.Warn("bye from caller", "call_id", c.id, "error", err)
		}
		c.hangup(true)
	case bID:
		if err := b.ReadBye(req, tx); err != nil {
			s.log.Warn("bye from callee", "call_id", c.id, "error", err)
		}
		c.hangup(false)
	default:
		_ = tx.Respond(sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil))
	}
}

func (s *Server) handleAck(req *sip.Request, tx sip.ServerTransaction) {
	id := req.CallID().Value()
	if p := s.conference.byCallID(id); p != nil {
		if err := p.dss.ReadAck(req, tx); err != nil {
			s.log.Warn("conference ack", "call_id", id, "error", err)
		}
		return
	}
	if ps := s.pagingGet(id); ps != nil {
		if err := ps.dss.ReadAck(req, tx); err != nil {
			s.log.Warn("paging ack", "call_id", id, "error", err)
		}
		return
	}
	c := s.calls.get(id)
	if c != nil && id == c.aCallID {
		if err := c.a.ReadAck(req, tx); err != nil {
			s.log.Warn("ack from caller", "call_id", c.id, "error", err)
		}
	}
}

func (s *Server) handleCancel(req *sip.Request, tx sip.ServerTransaction) {
	id := req.CallID().Value()
	if p := s.conference.byCallID(id); p != nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
		p.stop(false)
		return
	}
	if ps := s.pagingGet(id); ps != nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
		go func() {
			_ = ps.dss.Respond(487, "Request Terminated", nil)
			ps.stop()
		}()
		return
	}
	c := s.calls.get(id)
	_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	if c == nil {
		return
	}
	c.cancel()
	go func() {
		_ = c.a.Respond(487, "Request Terminated", nil, c.aContactHeader())
		c.finish("canceled")
	}()
}

func (s *Server) handleInfo(req *sip.Request, tx sip.ServerTransaction) {
	_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
}

// CloseAlarm closes an alarm event and notifies subscribers.
func (s *Server) CloseAlarm(id, disposition, note string) (store.Alarm, bool) {
	if disposition == "" {
		disposition = "other"
	}
	a, ok := s.alarms.Update(id, func(a *store.Alarm) {
		a.State = "closed"
		a.ClosedAt = time.Now().Format(time.RFC3339)
		a.Disposition = disposition
		a.Note = note
	})
	if ok {
		s.emit("alarm.closed", a)
	}
	return a, ok
}

// UnlockCall sends an in-dialog DTMF INFO to the callee (door station).
func (s *Server) UnlockCall(ctx context.Context, callID, code string) error {
	c := s.calls.getByID(callID)
	if c == nil {
		return fmt.Errorf("call not found")
	}
	c.stateMu.RLock()
	answeredAt := c.answerAt
	to := c.to
	a := c.a
	b := c.b
	c.stateMu.RUnlock()
	if answeredAt.IsZero() {
		return fmt.Errorf("call is not answered")
	}
	if code == "" {
		code = "#"
	}
	var (
		uri  sip.Uri
		send func(context.Context, *sip.Request) (*sip.Response, error)
	)
	// Door station may be either leg: 6xxx caller (guard answers) or 6xxx callee.
	if strings.HasPrefix(c.from, "6") && a != nil {
		if cont := a.InviteRequest.Contact(); cont != nil {
			uri = cont.Address
		}
		send = a.Do
	} else if b != nil {
		uri = b.InviteRequest.Recipient
		if b.InviteResponse != nil {
			if cont := b.InviteResponse.Contact(); cont != nil {
				uri = cont.Address
			}
		}
		send = b.Do
	} else {
		return fmt.Errorf("no dialog for unlock")
	}
	req := sip.NewRequest(sip.INFO, uri)
	req.AppendHeader(sip.NewHeader("Content-Type", "application/dtmf-relay"))
	req.SetBody([]byte("Signal=" + code + "\r\nDuration=200\r\n"))
	res, err := send(ctx, req)
	if err != nil {
		return fmt.Errorf("dtmf-relay: %w", err)
	}
	if res.StatusCode != 200 {
		// Some door stations only accept the plain application/dtmf body.
		req2 := sip.NewRequest(sip.INFO, uri)
		req2.AppendHeader(sip.NewHeader("Content-Type", "application/dtmf"))
		req2.SetBody([]byte(code))
		res2, err2 := send(ctx, req2)
		if err2 != nil {
			return fmt.Errorf("dtmf info: %w", err2)
		}
		if res2.StatusCode != 200 {
			return fmt.Errorf("unlock rejected: %d %s", res2.StatusCode, res2.Reason)
		}
	}
	s.log.Info("unlock sent", "call_id", callID, "to", to, "code", code)
	s.emit("intercom.unlock", map[string]any{"call_id": callID, "to": to, "code": code, "result": "ok"})
	return nil
}

// EmitTestAlarm publishes a simulated alarm for console/demo testing.
func (s *Server) EmitTestAlarm(from, to string) {
	if from == "" {
		from = "1002"
	}
	if to == "" {
		to = "1001"
	}
	ev := s.alarms.Create(store.Alarm{
		From: from, To: to, AlarmNumber: "8000", DeviceName: "测试报警柱",
		Zone: "测试区", State: "ringing", Level: 0, LevelName: "测试", Test: true,
	})
	s.emit("alarm.created", map[string]any{
		"alarm_id": ev.ID, "call_id": "test-" + ev.ID, "from": from, "to": to,
		"alarm_number": "8000", "device_name": "测试报警柱", "zone": "测试区",
		"started_at": ev.StartedAt, "test": true,
	})
}

func (s *Server) ActiveCalls() []CallView {
	calls := s.calls.list()
	out := make([]CallView, 0, len(calls))
	for _, c := range calls {
		c.stateMu.RLock()
		state := "ringing"
		if !c.answerAt.IsZero() {
			state = "answered"
		}
		to := c.to
		video := c.videoTap != nil
		alarm := c.alarmID != ""
		c.stateMu.RUnlock()
		out = append(out, CallView{
			CallID:    c.id,
			From:      c.from,
			To:        to,
			State:     state,
			StartedAt: c.startAt.Format(time.RFC3339),
			Video:     video,
			Alarm:     alarm,
		})
	}
	return out
}

func contactURI(raw string) (sip.Uri, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return sip.Uri{}, fmt.Errorf("empty contact")
	}
	if i := strings.Index(s, "<"); i >= 0 {
		if j := strings.Index(s[i:], ">"); j > 0 {
			s = s[i+1 : i+j]
		}
	}
	var u sip.Uri
	if err := sip.ParseUri(s, &u); err != nil {
		return sip.Uri{}, err
	}
	return u, nil
}

// contactURIForView routes to the REGISTER source when the Contact points at
// a loopback/benchmark address (common with Windows VPN/TUN adapters). This is
// the SIP equivalent of latching; the real source IP is where the client is.
func contactURIForView(v store.View) (sip.Uri, error) {
	uri, err := contactURI(v.Contact)
	if err != nil {
		return sip.Uri{}, err
	}
	if v.Transport != "" {
		if _, ok := uri.UriParams.Get("transport"); !ok {
			uri.UriParams.Add("transport", v.Transport)
		}
	}
	ip := net.ParseIP(uri.Host)
	if ip == nil {
		return uri, nil
	}
	bad := ip.IsUnspecified()
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19) {
		bad = true // 198.18.0.0/15, commonly used by VPN fake-IP
	}
	host, port, err := splitHostPort(v.Source)
	if err != nil || host == "" {
		return uri, nil
	}
	if ip.IsLoopback() {
		// Local tests register a loopback Contact from an ephemeral source port;
		// keep the Contact unless the source is a real non-loopback interface.
		if srcIP := net.ParseIP(host); srcIP != nil && srcIP.IsLoopback() {
			return uri, nil
		}
		bad = true
	}
	if !bad {
		return uri, nil
	}
	uri.Host = host
	if port > 0 {
		uri.Port = port
	}
	return uri, nil
}

// mediaIPForSource replaces a loopback/benchmark SDP connection address with
// the real signaling source (same problem as fake-IP Contact headers).
func mediaIPForSource(sdpIP, source string) string {
	ip := net.ParseIP(sdpIP)
	if ip == nil {
		return sdpIP
	}
	bad := ip.IsUnspecified()
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19) {
		bad = true
	}
	host, _, err := splitHostPort(source)
	if err != nil || host == "" {
		return sdpIP
	}
	if ip.IsLoopback() {
		if srcIP := net.ParseIP(host); srcIP != nil && srcIP.IsLoopback() {
			return sdpIP
		}
		bad = true
	}
	if !bad {
		return sdpIP
	}
	return host
}

func codecFor(info sdpInfo) string {
	if info.Codec != "" {
		return info.Codec
	}
	if info.PayloadType == 8 {
		return "PCMA"
	}
	return "PCMU"
}

func (s *Server) checkSRTPPolicy(offer sdpInfo) error {
	switch s.srtpMode {
	case "required":
		if !offer.AudioSecure || !offer.AudioCrypto.OK {
			return fmt.Errorf("SRTP Required")
		}
		if offer.HasVideo && (!offer.VideoSecure || !offer.VideoCrypto.OK) {
			return fmt.Errorf("SRTP Required")
		}
	case "off":
		if offer.AudioSecure || (offer.HasVideo && offer.VideoSecure) {
			return fmt.Errorf("SRTP Disabled")
		}
	}
	return nil
}

func (c *call) aContactHeader() *sip.ContactHeader {
	tp := ""
	if c.a != nil && c.a.InviteRequest != nil {
		tp = c.a.InviteRequest.Transport()
	}
	return c.server.contactForTransport(tp)
}

func (c *call) localFailure(t alarmTarget, stage string, err error) (bool, string) {
	msg := stage
	if err != nil {
		msg += ": " + err.Error()
	}
	c.stateMu.Lock()
	c.lastError = msg
	c.stateMu.Unlock()
	c.server.log.Warn("b-leg prepare failed", "call_id", c.id, "to", t.view.Ext, "stage", stage, "error", err)
	return false, "failed"
}
