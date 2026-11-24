package sipserver

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mcs/internal/config"
	"mcs/internal/store"
)

var (
	ErrZoneBusy    = errors.New("zone busy with higher priority")
	ErrNoEndpoints = errors.New("no online broadcast endpoints")
)

type zone struct {
	id        string
	name      string
	dial      string
	codec     string
	mode      string
	members   []string
	multicast *net.UDPAddr
}

func buildZones(cfg []config.ZoneConfig, st *store.Store) []zone {
	var all []string
	for _, d := range st.List() {
		all = append(all, d.Ext)
	}
	if len(cfg) == 0 {
		if len(all) == 0 {
			return nil
		}
		return []zone{{id: "all", name: "全部终端", mode: "unicast", members: all}}
	}
	out := make([]zone, 0, len(cfg))
	for _, z := range cfg {
		members := z.Members
		if len(members) == 0 {
			members = all
		}
		var mc *net.UDPAddr
		if z.Mode == "multicast" && z.Multicast != "" {
			mc, _ = net.ResolveUDPAddr("udp4", z.Multicast)
		}
		if z.Codec == "" {
			z.Codec = "pcmu"
		}
		out = append(out, zone{id: z.ID, name: z.Name, dial: z.Dial, codec: z.Codec, mode: z.Mode, members: members, multicast: mc})
	}
	return out
}

func (s *Server) findZone(id string) (zone, bool) {
	s.zonesMu.RLock()
	defer s.zonesMu.RUnlock()
	for _, z := range s.zones {
		if z.id == id {
			return z, true
		}
	}
	return zone{}, false
}

func (s *Server) findZoneByDial(dial string) (zone, bool) {
	s.zonesMu.RLock()
	defer s.zonesMu.RUnlock()
	for _, z := range s.zones {
		if z.dial != "" && z.dial == dial {
			return z, true
		}
	}
	return zone{}, false
}

// ZoneView is the API projection of a broadcast zone.
type ZoneView struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Dial      string   `json:"dial,omitempty"`
	Codec     string   `json:"codec"`
	Mode      string   `json:"mode"`
	Multicast string   `json:"multicast,omitempty"`
	Members   []string `json:"members"`
	Online    int      `json:"online"`
}

func (s *Server) BroadcastZones() []ZoneView {
	s.zonesMu.RLock()
	zones := append([]zone(nil), s.zones...)
	s.zonesMu.RUnlock()
	out := make([]ZoneView, 0, len(zones))
	for _, z := range zones {
		v := ZoneView{ID: z.id, Name: z.name, Dial: z.dial, Codec: z.codec, Mode: z.mode, Members: z.members}
		if z.multicast != nil {
			v.Multicast = z.multicast.String()
		}
		for _, ext := range z.members {
			if d, ok := s.store.Get(ext); ok && d.Online {
				v.Online++
			}
		}
		out = append(out, v)
	}
	return out
}

// BroadcastView is the API projection of a broadcast session.
type BroadcastView struct {
	ID         string `json:"id"`
	ZoneID     string `json:"zone_id"`
	ZoneName   string `json:"zone_name"`
	State      string `json:"state"`
	Source     string `json:"source"`
	Codec      string `json:"codec"`
	Priority   int    `json:"priority"`
	Text       string `json:"text"`
	Voice      string `json:"voice"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Total      int    `json:"total"`
	Delivered  int    `json:"delivered"`
	Active     int    `json:"active"`
	Failed     int    `json:"failed"`
	Error      string `json:"error,omitempty"`
}

type broadcastEndpoint struct {
	sess    *broadcastSession
	target  store.View
	callID  string
	dcs     *sipgo.DialogClientSession
	addr    *net.UDPAddr
	active  bool
	errText string
}

type broadcastSession struct {
	id         string
	source     string
	codec      string
	rtpPT      uint8
	rtpInc     uint32
	zone       zone
	text       string
	voice      string
	priority   int
	live       bool
	liveCh     chan []byte
	frames     [][]byte
	endpoints  []*broadcastEndpoint
	conn       *net.UDPConn
	stopCh     chan struct{}
	doneCh     chan struct{}
	stopOnce   sync.Once
	finishOnce sync.Once
	ctx        context.Context
	cancel     context.CancelFunc
	server     *Server

	mu         sync.Mutex
	state      string
	errText    string
	startedAt  time.Time
	finishedAt time.Time
	delivered  int
}

func (b *broadcastSession) view() BroadcastView {
	b.mu.Lock()
	defer b.mu.Unlock()
	active := 0
	failed := 0
	for _, ep := range b.endpoints {
		if ep.active {
			active++
		} else if ep.errText != "" {
			failed++
		}
	}
	v := BroadcastView{
		ID: b.id, ZoneID: b.zone.id, ZoneName: b.zone.name, State: b.state,
		Source: b.source, Codec: b.codec, Priority: b.priority, Text: b.text, Voice: b.voice,
		StartedAt: b.startedAt.Format(time.RFC3339), Total: len(b.endpoints),
		Delivered: b.delivered, Active: active, Failed: failed, Error: b.errText,
	}
	if !b.finishedAt.IsZero() {
		v.FinishedAt = b.finishedAt.Format(time.RFC3339)
	}
	return v
}

func (b *broadcastSession) setState(state, errText string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == "finished" || b.state == "failed" || b.state == "stopped" || b.state == "preempted" {
		return
	}
	b.state = state
	if errText != "" {
		b.errText = errText
	}
}

func (b *broadcastSession) stopped() bool {
	select {
	case <-b.stopCh:
		return true
	default:
		return false
	}
}

func (b *broadcastSession) stop(reason string) {
	b.stopOnce.Do(func() {
		b.mu.Lock()
		if b.state != "finished" && b.state != "failed" && b.state != "stopped" && b.state != "preempted" {
			b.state = reason
		}
		b.mu.Unlock()
		b.cancel()
		close(b.stopCh)
	})
}

func (b *broadcastSession) run() {
	defer close(b.doneCh)
	b.setState("starting", "")
	max := b.server.broadcastCfg.MaxConcurrency
	if max <= 0 {
		max = 16
	}
	timeout := time.Duration(b.server.broadcastCfg.InviteTimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	inviteCtx, inviteCancel := context.WithTimeout(b.ctx, timeout)
	sem := make(chan struct{}, max)
	var wg sync.WaitGroup
	for _, ep := range b.endpoints {
		wg.Add(1)
		go func(ep *broadcastEndpoint) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			b.invite(inviteCtx, ep)
		}(ep)
	}
	wg.Wait()
	inviteCancel()

	if b.stopped() {
		b.finish(b.currentState())
		return
	}
	b.mu.Lock()
	active := 0
	for _, ep := range b.endpoints {
		if ep.active {
			active++
		}
	}
	b.mu.Unlock()
	if active == 0 {
		b.setState("failed", "all endpoint invites failed")
		b.finish("failed")
		return
	}

	b.setState("running", "")
	b.server.emit("broadcast.started", b.view())
	if b.live {
		b.sendLive()
	} else {
		b.sendFrames()
	}
	if b.stopped() {
		b.finish(b.currentState())
		return
	}
	b.finish("finished")
}

func (b *broadcastSession) currentState() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *broadcastSession) invite(parent context.Context, ep *broadcastEndpoint) {
	server := b.server
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	uri, err := contactURIForView(ep.target)
	if err != nil {
		b.setEndpointErr(ep, err.Error())
		return
	}
	sdp := b.buildSDP()
	req := sip.NewRequest(sip.INVITE, uri)
	req.AppendHeader(sip.NewHeader("From", fmt.Sprintf("<sip:broadcast@%s>;tag=%d", server.domain, time.Now().UnixNano())))
	req.AppendHeader(sip.NewHeader("To", fmt.Sprintf("<sip:%s@%s>", ep.target.Ext, server.domain)))
	req.AppendHeader(sip.NewHeader("Call-ID", ep.callID))
	req.AppendHeader(sip.NewHeader("CSeq", "1 INVITE"))
	req.AppendHeader(sip.NewHeader("Max-Forwards", "69"))
	req.AppendHeader(sip.NewHeader("Contact", fmt.Sprintf("<sip:mcs@%s>", server.advertise)))
	req.AppendHeader(sip.NewHeader("User-Agent", serverName))
	req.AppendHeader(sip.NewHeader("Alert-Info", server.broadcastCfg.AutoAnswer))
	req.AppendHeader(sip.NewHeader("X-MCS-Event", "broadcast"))
	req.AppendHeader(sip.NewHeader("X-MCS-Zone", b.zone.id))
	req.AppendHeader(sip.NewHeader("X-MCS-Broadcast-ID", b.id))
	req.AppendHeader(sip.NewHeader("X-MCS-Priority", strconv.Itoa(b.priority)))
	req.AppendHeader(sip.NewHeader("X-MCS-Codec", b.codec))
	req.AppendHeader(sip.NewHeader("X-MCS-Source", b.source))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody(sdp)
	dcs, err := server.dua.WriteInvite(ctx, req)
	if err != nil {
		b.setEndpointErr(ep, err.Error())
		return
	}
	if err := dcs.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
		_ = dcs.Close()
		b.setEndpointErr(ep, err.Error())
		return
	}
	if err := dcs.Ack(ctx); err != nil {
		b.setEndpointErr(ep, err.Error())
		return
	}

	info := parseSDP(dcs.InviteResponse.Body())
	if b.codec == "opus" {
		if !strings.EqualFold(info.Codec, "OPUS") {
			b.setEndpointErr(ep, "terminal did not answer with Opus")
			return
		}
	} else if info.PayloadType != 0 && !strings.EqualFold(info.Codec, "PCMU") {
		b.setEndpointErr(ep, "terminal did not answer with PCMU")
		return
	}
	addr := endpointAddr(ep.target, info)
	if addr == nil {
		b.setEndpointErr(ep, "no audio address in answer")
		return
	}
	b.mu.Lock()
	ep.dcs = dcs
	ep.addr = addr
	ep.active = true
	b.delivered++
	b.mu.Unlock()
	server.bc.registerCallID(ep.callID, ep)
}

func (b *broadcastSession) setEndpointErr(ep *broadcastEndpoint, msg string) {
	b.mu.Lock()
	ep.errText = msg
	b.mu.Unlock()
}

func endpointAddr(target store.View, info sdpInfo) *net.UDPAddr {
	if info.AudioPort <= 0 {
		return nil
	}
	host := info.ConnIP
	if host == "" || host == "0.0.0.0" {
		if h, _, err := net.SplitHostPort(target.Source); err == nil {
			host = h
		}
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() {
		return nil
	}
	return &net.UDPAddr{IP: ip, Port: info.AudioPort}
}

func (b *broadcastSession) buildSDP() []byte {
	connIP := b.server.advertiseHost
	port := b.conn.LocalAddr().(*net.UDPAddr).Port
	if b.zone.multicast != nil {
		connIP = b.zone.multicast.IP.String()
		port = b.zone.multicast.Port
	}
	if b.codec == "opus" {
		return []byte(fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 %s\r\ns=MCS Broadcast\r\nc=IN IP4 %s\r\nt=0 0\r\nm=audio %d RTP/AVP 111\r\na=rtpmap:111 opus/48000/2\r\na=fmtp:111 minptime=10;useinbandfec=1\r\na=sendonly\r\n", connIP, connIP, port))
	}
	return []byte(fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 %s\r\ns=MCS Broadcast\r\nc=IN IP4 %s\r\nt=0 0\r\nm=audio %d RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\na=sendonly\r\n", connIP, connIP, port))
}

func (b *broadcastSession) sendFrames() {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	seq := uint16(0)
	ts := uint32(0)
	ssrc := uint32(time.Now().UnixNano())
	for _, frame := range b.frames {
		pkt := make([]byte, 12+len(frame))
		pkt[0] = 0x80
		pkt[1] = b.rtpPT
		binary.BigEndian.PutUint16(pkt[2:4], seq)
		binary.BigEndian.PutUint32(pkt[4:8], ts)
		binary.BigEndian.PutUint32(pkt[8:12], ssrc)
		copy(pkt[12:], frame)
		b.sendPacket(pkt)
		seq++
		ts += b.rtpInc
		select {
		case <-b.stopCh:
			return
		case <-ticker.C:
		}
	}
}

func (b *broadcastSession) sendLive() {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	silence := bytes.Repeat([]byte{0xFF}, 160)
	seq := uint16(0)
	ts := uint32(0)
	ssrc := uint32(time.Now().UnixNano())
	for {
		select {
		case <-b.stopCh:
			return
		case <-ticker.C:
		}
		frame := silence
		if b.liveCh != nil {
			for {
				select {
				case f := <-b.liveCh:
					if len(f) == 160 {
						frame = f
					}
				default:
					goto ready
				}
			}
		}
	ready:
		pkt := make([]byte, 12+len(frame))
		pkt[0] = 0x80
		pkt[1] = b.rtpPT
		binary.BigEndian.PutUint16(pkt[2:4], seq)
		binary.BigEndian.PutUint32(pkt[4:8], ts)
		binary.BigEndian.PutUint32(pkt[8:12], ssrc)
		copy(pkt[12:], frame)
		seq++
		ts += b.rtpInc
		b.sendPacket(pkt)
	}
}

func (b *broadcastSession) sendPacket(pkt []byte) {
	if b.zone.multicast != nil {
		_, _ = b.conn.WriteToUDP(pkt, b.zone.multicast)
		return
	}
	b.mu.Lock()
	addrs := make([]*net.UDPAddr, 0, len(b.endpoints))
	for _, ep := range b.endpoints {
		if ep.active && ep.addr != nil {
			addrs = append(addrs, ep.addr)
		}
	}
	b.mu.Unlock()
	for _, addr := range addrs {
		_, _ = b.conn.WriteToUDP(pkt, addr)
	}
}

func (b *broadcastSession) finish(reason string) {
	b.finishOnce.Do(func() {
		b.mu.Lock()
		if b.state != "preempted" && b.state != "stopped" {
			b.state = reason
		}
		if b.state == "finished" && b.errText == "" {
			failed := 0
			for _, ep := range b.endpoints {
				if ep.errText != "" {
					failed++
				}
			}
			if failed > 0 {
				b.state = "partial"
			}
		}
		b.finishedAt = time.Now()
		eps := make([]*broadcastEndpoint, 0, len(b.endpoints))
		for _, ep := range b.endpoints {
			if ep.active {
				eps = append(eps, ep)
			}
		}
		b.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		var wg sync.WaitGroup
		for _, ep := range eps {
			wg.Add(1)
			go func(ep *broadcastEndpoint) {
				defer wg.Done()
				if ep.dcs != nil {
					_ = ep.dcs.Bye(ctx)
				}
				b.mu.Lock()
				ep.active = false
				b.mu.Unlock()
				b.server.bc.unregisterCallID(ep.callID)
			}(ep)
		}
		wg.Wait()
		cancel()
		_ = b.conn.Close()
		b.server.bc.remove(b)
		v := b.view()
		b.server.emit("broadcast.finished", v)
		b.server.log.Info("broadcast ended", "id", b.id, "zone", b.zone.id, "state", v.State, "total", v.Total, "delivered", v.Delivered, "failed", v.Failed)
	})
}

type broadcastManager struct {
	mu      sync.Mutex
	active  map[string]*broadcastSession
	all     map[string]*broadcastSession
	order   []string
	callIDs map[string]*broadcastEndpoint
	max     int
}

func newBroadcastManager() *broadcastManager {
	return &broadcastManager{
		active: map[string]*broadcastSession{}, all: map[string]*broadcastSession{},
		callIDs: map[string]*broadcastEndpoint{}, max: 200,
	}
}

func (m *broadcastManager) check(zoneID string, priority int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a := m.active[zoneID]; a != nil && priority > a.priority {
		return ErrZoneBusy
	}
	return nil
}

func (m *broadcastManager) register(s *broadcastSession) (*broadcastSession, error) {
	m.mu.Lock()
	preempt := m.active[s.zone.id]
	if preempt != nil && s.priority > preempt.priority {
		m.mu.Unlock()
		return nil, ErrZoneBusy
	}
	m.active[s.zone.id] = s
	m.all[s.id] = s
	m.order = append(m.order, s.id)
	if len(m.order) > m.max {
		old := m.order[0]
		m.order = m.order[1:]
		delete(m.all, old)
	}
	m.mu.Unlock()
	return preempt, nil
}

func (m *broadcastManager) remove(s *broadcastSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active[s.zone.id] == s {
		delete(m.active, s.zone.id)
	}
}

func (m *broadcastManager) registerCallID(id string, ep *broadcastEndpoint) {
	m.mu.Lock()
	m.callIDs[id] = ep
	m.mu.Unlock()
}

func (m *broadcastManager) unregisterCallID(id string) {
	m.mu.Lock()
	delete(m.callIDs, id)
	m.mu.Unlock()
}

func (m *broadcastManager) handleBye(req *sip.Request, tx sip.ServerTransaction) bool {
	id := req.CallID().Value()
	m.mu.Lock()
	ep, ok := m.callIDs[id]
	if ok {
		delete(m.callIDs, id)
	}
	m.mu.Unlock()
	if !ok {
		return false
	}
	_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	// The session will finish when its stop/BYE path runs; mark the endpoint gone.
	if ep.sess != nil {
		ep.sess.mu.Lock()
		ep.active = false
		ep.sess.mu.Unlock()
	}
	return true
}

func (m *broadcastManager) sessions(activeOnly bool) []BroadcastView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]BroadcastView, 0, len(m.order))
	if activeOnly {
		for _, s := range m.active {
			out = append(out, s.view())
		}
		return out
	}
	for i := len(m.order) - 1; i >= 0; i-- {
		if s := m.all[m.order[i]]; s != nil {
			out = append(out, s.view())
		}
	}
	return out
}

// BroadcastRequest selects a TTS or uploaded-audio broadcast.
type BroadcastRequest struct {
	ZoneID   string
	Text     string
	Voice    string
	MediaID  string
	Priority int
}

// StartBroadcast starts a TTS or media-file broadcast session.
func (s *Server) StartBroadcast(ctx context.Context, req BroadcastRequest) (BroadcastView, error) {
	zoneID, text, voice, priority := req.ZoneID, strings.TrimSpace(req.Text), req.Voice, req.Priority
	z, ok := s.findZone(zoneID)
	if !ok {
		return BroadcastView{}, fmt.Errorf("zone %q not found", zoneID)
	}
	if text == "" && req.MediaID == "" {
		return BroadcastView{}, fmt.Errorf("empty broadcast text")
	}
	if len([]rune(text)) > 500 {
		return BroadcastView{}, fmt.Errorf("text too long (max 500 chars)")
	}
	if priority < 0 || priority > 4 {
		priority = 3
	}
	if err := s.bc.check(zoneID, priority); err != nil {
		return BroadcastView{}, err
	}

	var endpoints []*broadcastEndpoint
	for _, ext := range z.members {
		if d, ok := s.store.Get(ext); ok && d.Online {
			endpoints = append(endpoints, &broadcastEndpoint{
				target: d,
				callID: fmt.Sprintf("bcs-%d-%s", time.Now().UnixNano(), ext),
			})
		}
	}
	if len(endpoints) == 0 {
		return BroadcastView{}, ErrNoEndpoints
	}

	source := "tts"
	var frames [][]byte
	if req.MediaID != "" {
		item, data, ok := s.media.read(req.MediaID)
		if !ok {
			return BroadcastView{}, fmt.Errorf("media %q not found", req.MediaID)
		}
		pcm, rate, _, err := parseAudio(data)
		if err != nil {
			return BroadcastView{}, fmt.Errorf("media audio: %w", err)
		}
		if z.codec == "opus" {
			frames, err = encodeOpusFrames(pcm, rate)
			if err != nil {
				return BroadcastView{}, fmt.Errorf("opus encode: %w", err)
			}
		} else {
			frames = ulawFrames(pcm, rate)
		}
		if len(frames) == 0 {
			return BroadcastView{}, fmt.Errorf("media produced no audio")
		}
		source = "media"
		text = item.Name
		voice = ""
	} else {
		ttsClient := s.TTSClient()
		wav, err := ttsClient.Synthesize(ctx, text, voice)
		if err != nil {
			return BroadcastView{}, fmt.Errorf("tts: %w", err)
		}
		pcm, rate, _, err := parseAudio(wav)
		if err != nil {
			return BroadcastView{}, fmt.Errorf("tts audio: %w", err)
		}
		if z.codec == "opus" {
			frames, err = encodeOpusFrames(pcm, rate)
			if err != nil {
				return BroadcastView{}, fmt.Errorf("opus encode: %w", err)
			}
		} else {
			frames = ulawFrames(pcm, rate)
		}
		if len(frames) == 0 {
			return BroadcastView{}, fmt.Errorf("tts produced no audio")
		}
		if voice == "" {
			voice = ttsClient.VoiceFor(text)
		}
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return BroadcastView{}, err
	}
	ctx2, cancel := context.WithCancel(context.Background())
	sess := &broadcastSession{
		id:     fmt.Sprintf("bcs-%d-%d", time.Now().UnixNano(), broadcastSeq.Add(1)),
		source: source,
		codec:  z.codec,
		rtpPT: func() uint8 {
			if z.codec == "opus" {
				return 111
			}
			return 0
		}(),
		rtpInc: func() uint32 {
			if z.codec == "opus" {
				return 960
			}
			return 160
		}(),
		zone:      z,
		text:      text,
		voice:     voice,
		priority:  priority,
		frames:    frames,
		endpoints: endpoints,
		conn:      conn,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
		ctx:       ctx2,
		cancel:    cancel,
		server:    s,
		state:     "starting",
		startedAt: time.Now(),
	}
	for _, ep := range sess.endpoints {
		ep.sess = sess
	}
	preempt, err := s.bc.register(sess)
	if err != nil {
		_ = conn.Close()
		cancel()
		return BroadcastView{}, err
	}
	go sess.run()
	if preempt != nil {
		preempt.stop("preempted")
	}
	return sess.view(), nil
}

// StartLiveBroadcast starts a broadcast fed by a live RTP source.
func (s *Server) StartLiveBroadcast(zoneID string, priority int) (*broadcastSession, error) {
	z, ok := s.findZone(zoneID)
	if !ok {
		return nil, fmt.Errorf("zone %q not found", zoneID)
	}
	if priority < 0 || priority > 4 {
		priority = 2
	}
	if err := s.bc.check(zoneID, priority); err != nil {
		return nil, err
	}
	var endpoints []*broadcastEndpoint
	for _, ext := range z.members {
		if d, ok := s.store.Get(ext); ok && d.Online {
			endpoints = append(endpoints, &broadcastEndpoint{
				target: d,
				callID: fmt.Sprintf("bcs-%d-%s", time.Now().UnixNano(), ext),
			})
		}
	}
	if len(endpoints) == 0 {
		return nil, ErrNoEndpoints
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	sess := &broadcastSession{
		id:        fmt.Sprintf("bcs-%d-%d", time.Now().UnixNano(), broadcastSeq.Add(1)),
		source:    "live",
		codec:     "pcmu",
		rtpPT:     0,
		rtpInc:    160,
		zone:      z,
		priority:  priority,
		live:      true,
		liveCh:    make(chan []byte, 200),
		endpoints: endpoints,
		conn:      conn,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
		ctx:       ctx,
		cancel:    cancel,
		server:    s,
		state:     "starting",
		startedAt: time.Now(),
	}
	for _, ep := range endpoints {
		ep.sess = sess
	}
	preempt, err := s.bc.register(sess)
	if err != nil {
		_ = conn.Close()
		cancel()
		return nil, err
	}
	go sess.run()
	if preempt != nil {
		preempt.stop("preempted")
	}
	return sess, nil
}

// StopBroadcast stops the active session in a zone.
func (s *Server) StopBroadcast(zoneID string) (BroadcastView, bool) {
	s.bc.mu.Lock()
	sess := s.bc.active[zoneID]
	s.bc.mu.Unlock()
	if sess == nil {
		return BroadcastView{}, false
	}
	sess.stop("stopped")
	return sess.view(), true
}

func (s *Server) BroadcastSessions(activeOnly bool) []BroadcastView {
	return s.bc.sessions(activeOnly)
}

var broadcastSeq atomic.Uint64

// ZoneConfigs returns a copy of the effective broadcast-zone configuration.
func (s *Server) ZoneConfigs() []config.ZoneConfig {
	s.zonesMu.RLock()
	defer s.zonesMu.RUnlock()
	out := make([]config.ZoneConfig, 0, len(s.zones))
	for _, z := range s.zones {
		members := append([]string(nil), z.members...)
		mc := ""
		if z.multicast != nil {
			mc = z.multicast.String()
		}
		out = append(out, config.ZoneConfig{
			ID: z.id, Name: z.name, Dial: z.dial, Codec: z.codec,
			Members: members, Mode: z.mode, Multicast: mc,
		})
	}
	return out
}

// SetZones validates and atomically replaces the runtime zones. It is used by
// the web console; the caller is responsible for persisting the config.
func (s *Server) SetZones(zones []config.ZoneConfig) error {
	if len(zones) == 0 {
		return fmt.Errorf("至少保留一个广播分区")
	}
	for i := range zones {
		zones[i] = config.NormalizeZone(zones[i])
	}
	if err := config.ValidateZones(zones); err != nil {
		return err
	}
	zones2 := buildZones(zones, s.store)
	if len(zones2) == 0 {
		return fmt.Errorf("分区没有可用成员")
	}
	s.zonesMu.Lock()
	s.zones = zones2
	s.zonesMu.Unlock()
	return nil
}
