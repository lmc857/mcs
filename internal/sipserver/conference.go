package sipserver

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"

	"mcs/internal/config"
)

const conferenceFrameSamples = 160 // 20 ms @ 8 kHz

type conferenceManager struct {
	server *Server
	mu     sync.RWMutex
	rooms  map[string]*conferenceRoom
	byCall map[string]*conferenceParticipant
}

type conferenceRoom struct {
	number string
	name   string
	max    int
	record bool

	mu        sync.Mutex
	parts     map[string]*conferenceParticipant
	started   bool
	closed    bool
	done      chan struct{}
	rec       *conferenceRecorder
	recording string

	server *Server
}

type conferenceParticipant struct {
	id     string
	ext    string
	name   string
	codec  string
	pt     uint8
	conn   *net.UDPConn
	dss    *sipgo.DialogServerSession
	room   *conferenceRoom
	server *Server

	remoteMu sync.Mutex
	remote   *net.UDPAddr

	mu    sync.Mutex
	in    chan []int16
	last  []int16
	muted bool

	seq  uint16
	ts   uint32
	ssrc uint32

	stopOnce sync.Once
	stopCh   chan struct{}
	joinedAt time.Time
}

type ConferenceParticipantView struct {
	ID       string `json:"id"`
	Ext      string `json:"ext"`
	Name     string `json:"name"`
	Muted    bool   `json:"muted"`
	Codec    string `json:"codec"`
	JoinedAt string `json:"joined_at"`
}

type ConferenceRoomView struct {
	Number       string                      `json:"number"`
	Name         string                      `json:"name"`
	MaxMembers   int                         `json:"max_members"`
	Active       int                         `json:"active"`
	Recording    string                      `json:"recording,omitempty"`
	Participants []ConferenceParticipantView `json:"participants"`
}

func newConferenceManager(s *Server, cfg config.ConferenceConfig) *conferenceManager {
	m := &conferenceManager{server: s, rooms: map[string]*conferenceRoom{}, byCall: map[string]*conferenceParticipant{}}
	if !cfg.Enabled {
		return m
	}
	for _, rc := range cfg.Rooms {
		if rc.Number == "" {
			continue
		}
		if rc.MaxMembers <= 0 {
			rc.MaxMembers = 8
		}
		m.rooms[rc.Number] = &conferenceRoom{
			number: rc.Number, name: rc.Name, max: rc.MaxMembers, record: rc.Record,
			parts: map[string]*conferenceParticipant{}, done: make(chan struct{}), server: s,
		}
	}
	return m
}

func (m *conferenceManager) find(number string) (*conferenceRoom, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.RLock()
	r, ok := m.rooms[number]
	m.mu.RUnlock()
	return r, ok
}

func (m *conferenceManager) byCallID(id string) *conferenceParticipant {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	p := m.byCall[id]
	m.mu.RUnlock()
	return p
}

func (m *conferenceManager) addCall(p *conferenceParticipant) {
	m.mu.Lock()
	m.byCall[p.id] = p
	m.mu.Unlock()
}

func (m *conferenceManager) removeCall(id string) {
	m.mu.Lock()
	delete(m.byCall, id)
	m.mu.Unlock()
}

func (m *conferenceManager) view() []ConferenceRoomView {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	rooms := make([]*conferenceRoom, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	m.mu.RUnlock()
	out := make([]ConferenceRoomView, 0, len(rooms))
	for _, r := range rooms {
		out = append(out, r.view())
	}
	return out
}

func (m *conferenceManager) kick(roomNumber, id string) error {
	r, ok := m.find(roomNumber)
	if !ok {
		return fmt.Errorf("room not found")
	}
	p := r.get(id)
	if p == nil {
		return fmt.Errorf("participant not found")
	}
	p.stop(true)
	return nil
}

func (m *conferenceManager) mute(roomNumber, id string, muted bool) error {
	r, ok := m.find(roomNumber)
	if !ok {
		return fmt.Errorf("room not found")
	}
	p := r.get(id)
	if p == nil {
		return fmt.Errorf("participant not found")
	}
	p.mu.Lock()
	p.muted = muted
	p.mu.Unlock()
	return nil
}

func (r *conferenceRoom) view() ConferenceRoomView {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := ConferenceRoomView{Number: r.number, Name: r.name, MaxMembers: r.max, Recording: r.recording}
	for _, p := range r.parts {
		p.mu.Lock()
		item := ConferenceParticipantView{ID: p.id, Ext: p.ext, Name: p.name, Muted: p.muted, Codec: p.codec, JoinedAt: p.joinedAt.Format(time.RFC3339)}
		p.mu.Unlock()
		v.Participants = append(v.Participants, item)
	}
	v.Active = len(v.Participants)
	return v
}

func (r *conferenceRoom) get(id string) *conferenceParticipant {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.parts[id]
}

func (r *conferenceRoom) add(p *conferenceParticipant) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.parts) >= r.max {
		return false
	}
	if r.closed {
		r.closed = false
		r.done = make(chan struct{})
	}
	r.parts[p.id] = p
	p.room = r
	if !r.started {
		r.started = true
		if r.record {
			r.rec = newConferenceRecorder(r.server.dataDir, r.number)
		}
		go r.mixLoop()
	}
	return true
}

func (r *conferenceRoom) remove(p *conferenceParticipant) {
	r.mu.Lock()
	delete(r.parts, p.id)
	empty := len(r.parts) == 0
	if empty && !r.closed {
		r.closed = true
		r.started = false
		close(r.done)
		rec := r.rec
		r.rec = nil
		r.mu.Unlock()
		if rec != nil {
			url := rec.close()
			r.mu.Lock()
			r.recording = url
			r.mu.Unlock()
		}
		return
	}
	r.mu.Unlock()
}

func (r *conferenceRoom) snapshot() []*conferenceParticipant {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*conferenceParticipant, 0, len(r.parts))
	for _, p := range r.parts {
		out = append(out, p)
	}
	return out
}

func (r *conferenceRoom) mixLoop() {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-ticker.C:
			parts := r.snapshot()
			if len(parts) == 0 {
				return
			}
			type frame struct {
				p     *conferenceParticipant
				pcm   []int16
				muted bool
			}
			frames := make([]frame, 0, len(parts))
			for _, p := range parts {
				p.mu.Lock()
				for {
					select {
					case f := <-p.in:
						p.last = f
					default:
						goto drained
					}
				}
			drained:
				last := p.last
				p.last = nil
				muted := p.muted
				p.mu.Unlock()
				frames = append(frames, frame{p: p, pcm: last, muted: muted})
			}
			var all [conferenceFrameSamples]int32
			for _, f := range frames {
				if f.muted || len(f.pcm) == 0 {
					continue
				}
				for i := 0; i < conferenceFrameSamples && i < len(f.pcm); i++ {
					all[i] += int32(f.pcm[i])
				}
			}
			for _, dst := range frames {
				var mixed [conferenceFrameSamples]int16
				for _, src := range frames {
					if src.p.id == dst.p.id || src.muted {
						continue
					}
					for i := 0; i < conferenceFrameSamples; i++ {
						if i < len(src.pcm) {
							v := int32(mixed[i]) + int32(src.pcm[i])
							if v > 32767 {
								v = 32767
							} else if v < -32768 {
								v = -32768
							}
							mixed[i] = int16(v)
						}
					}
				}
				dst.p.sendPCM(mixed[:])
			}
			r.mu.Lock()
			rec := r.rec
			r.mu.Unlock()
			if rec != nil {
				rec.write(all[:])
			}
			r.server.log.Debug("conference mix tick", "room", r.number, "parts", len(frames))
		}
	}
}

func (p *conferenceParticipant) readRTP() {
	buf := make([]byte, 2048)
	for {
		n, addr, err := p.conn.ReadFromUDP(buf)
		if err != nil {
			p.server.log.Debug("conference RTP read stopped", "ext", p.ext, "error", err)
			return
		}
		p.server.log.Debug("conference RTP packet", "ext", p.ext, "bytes", n, "from", addr.String())
		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			p.server.log.Debug("conference RTP unmarshal failed", "ext", p.ext, "error", err)
			continue
		}
		if pkt.PayloadType != p.pt {
			p.server.log.Debug("conference RTP PT mismatch", "ext", p.ext, "got", pkt.PayloadType, "want", p.pt)
			continue
		}
		raw := decodeG711(pkt.Payload, p.codec)
		frame := make([]int16, conferenceFrameSamples)
		for i := 0; i < conferenceFrameSamples && i*2+1 < len(raw); i++ {
			frame[i] = int16(binary.LittleEndian.Uint16(raw[i*2 : i*2+2]))
		}
		p.remoteMu.Lock()
		p.remote = addr
		p.remoteMu.Unlock()
		p.mu.Lock()
		select {
		case p.in <- frame:
			p.server.log.Debug("conference RTP in", "ext", p.ext, "bytes", n, "pt", pkt.PayloadType)
		default:
		}
		p.mu.Unlock()
	}
}

func (p *conferenceParticipant) sendPCM(samples []int16) {
	var payload []byte
	if p.codec == "PCMA" {
		payload = encodeALaw(samples)
	} else {
		payload = encodeULaw(samples)
	}
	pkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: p.pt, SequenceNumber: p.seq, Timestamp: p.ts, SSRC: p.ssrc},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		return
	}
	p.seq++
	p.ts += conferenceFrameSamples
	p.remoteMu.Lock()
	addr := p.remote
	p.remoteMu.Unlock()
	if addr != nil {
		_, _ = p.conn.WriteToUDP(raw, addr)
	}
}

func (p *conferenceParticipant) stop(sendBye bool) {
	p.stopOnce.Do(func() {
		close(p.stopCh)
		_ = p.conn.Close()
		p.room.remove(p)
		p.server.conference.removeCall(p.id)
		p.server.log.Info("conference participant left", "room", p.room.number, "ext", p.ext, "call_id", p.id)
		p.server.emit("conference.left", map[string]any{"room": p.room.number, "id": p.id, "ext": p.ext})
		if sendBye && p.dss != nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = p.dss.Bye(ctx)
			}()
		}
	})
}

func (s *Server) ConferenceView() []ConferenceRoomView {
	if s.conference == nil {
		return nil
	}
	return s.conference.view()
}

func (s *Server) ConferenceKick(room, id string) error {
	if s.conference == nil {
		return fmt.Errorf("conference disabled")
	}
	return s.conference.kick(room, id)
}

func (s *Server) ConferenceMute(room, id string, muted bool) error {
	if s.conference == nil {
		return fmt.Errorf("conference disabled")
	}
	return s.conference.mute(room, id, muted)
}

func (s *Server) handleConferenceInvite(req *sip.Request, tx sip.ServerTransaction, room *conferenceRoom) {
	from := ""
	if f := req.From(); f != nil {
		from = f.Address.User
	}
	if from == "" {
		from = "anonymous"
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
	codec, pt, ok := conferenceCodec(offer)
	if !ok {
		s.respond(req, tx, 488, "Not Acceptable Here")
		return
	}
	dss, err := s.dua.ReadInvite(req, tx)
	if err != nil {
		s.respond(req, tx, 400, "Bad Request")
		return
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		_ = dss.Respond(480, "Temporarily Unavailable", nil)
		return
	}
	view, _ := s.store.Get(from)
	name := view.Name
	if name == "" {
		name = from
	}
	callID := dss.InviteRequest.CallID().Value()
	p := &conferenceParticipant{
		id: callID, ext: from, name: name, codec: codec, pt: pt, conn: conn, dss: dss, server: s,
		remote: conferenceRemote(offer, req.Source()),
		in:     make(chan []int16, 8), stopCh: make(chan struct{}), joinedAt: time.Now(),
		seq: uint16(time.Now().UnixNano()), ts: uint32(time.Now().UnixNano()), ssrc: uint32(time.Now().UnixNano()),
	}
	if !room.add(p) {
		_ = conn.Close()
		_ = dss.Respond(486, "Busy Here", nil)
		return
	}
	s.conference.addCall(p)
	_ = dss.Respond(100, "Trying", nil)
	sdp := conferenceSDP(s.advertiseHost, conn.LocalAddr().(*net.UDPAddr).Port, codec)
	if err := dss.RespondSDP(sdp); err != nil {
		p.stop(false)
		return
	}
	go p.readRTP()
	s.log.Info("conference joined", "room", room.number, "ext", from, "call_id", callID, "codec", codec)
	s.emit("conference.joined", map[string]any{"room": room.number, "id": callID, "ext": from, "name": name})

	select {
	case <-p.stopCh:
	case <-room.done:
		p.stop(false)
	}
}

func conferenceCodec(info sdpInfo) (string, uint8, bool) {
	if info.Codec == "PCMA" || info.Codec == "PCMU" {
		pt := uint8(0)
		if info.Codec == "PCMA" {
			pt = 8
		}
		return info.Codec, pt, true
	}
	for _, pt := range info.AudioPayloads {
		switch pt {
		case 0:
			return "PCMU", 0, true
		case 8:
			return "PCMA", 8, true
		}
	}
	for pt, codec := range info.AudioCodecs {
		if codec == "PCMU" {
			return "PCMU", pt, true
		}
		if codec == "PCMA" {
			return "PCMA", pt, true
		}
	}
	return "", 0, false
}

func conferenceRemote(info sdpInfo, source string) *net.UDPAddr {
	host := info.ConnIP
	port := info.AudioPort
	if host == "" || host == "0.0.0.0" {
		if h, p, err := net.SplitHostPort(source); err == nil {
			host = h
			if port <= 0 {
				fmt.Sscanf(p, "%d", &port)
			}
		}
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() || port <= 0 {
		return nil
	}
	return &net.UDPAddr{IP: ip, Port: port}
}

func conferenceSDP(host string, port int, codec string) []byte {
	pt, name := 0, "PCMU"
	if codec == "PCMA" {
		pt, name = 8, "PCMA"
	}
	return []byte(fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 %s\r\ns=MCS Conference\r\nc=IN IP4 %s\r\nt=0 0\r\nm=audio %d RTP/AVP %d\r\na=rtpmap:%d %s/8000\r\na=sendrecv\r\n", host, host, port, pt, pt, name))
}

type conferenceRecorder struct {
	mu   sync.Mutex
	f    *os.File
	path string
	root string
}

func newConferenceRecorder(dataDir, room string) *conferenceRecorder {
	root := filepath.Join(dataDir, "recordings")
	tmp := filepath.Join(root, ".tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil
	}
	path := filepath.Join(tmp, fmt.Sprintf("conf-%s-%d.raw", room, time.Now().UnixNano()))
	f, err := os.Create(path)
	if err != nil {
		return nil
	}
	return &conferenceRecorder{f: f, path: path, root: root}
}

func (r *conferenceRecorder) write(samples []int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return
	}
	buf := make([]byte, len(samples)*4)
	for i, v := range samples {
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		binary.LittleEndian.PutUint16(buf[i*4:], uint16(int16(v)))
		binary.LittleEndian.PutUint16(buf[i*4+2:], uint16(int16(v)))
	}
	_, _ = r.f.Write(buf)
}

func (r *conferenceRecorder) close() string {
	r.mu.Lock()
	if r.f == nil {
		r.mu.Unlock()
		return ""
	}
	_ = r.f.Close()
	path, root := r.path, r.root
	r.f = nil
	r.mu.Unlock()
	defer os.Remove(path)

	stat, err := os.Stat(path)
	if err != nil || stat.Size() == 0 {
		return ""
	}
	now := time.Now()
	relDir := filepath.Join(now.Format("2006"), now.Format("01"), now.Format("02"))
	finalDir := filepath.Join(root, relDir)
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		return ""
	}
	final := filepath.Join(finalDir, filepath.Base(path)+".wav")
	out, err := os.Create(final)
	if err != nil {
		return ""
	}
	if _, err := out.Write(wavHeader(uint32(stat.Size()))); err != nil {
		_ = out.Close()
		return ""
	}
	in, err := os.Open(path)
	if err != nil {
		_ = out.Close()
		return ""
	}
	_, copyErr := out.ReadFrom(in)
	_ = in.Close()
	_ = out.Close()
	if copyErr != nil {
		return ""
	}
	rel, err := filepath.Rel(root, final)
	if err != nil {
		return ""
	}
	return "/recordings/" + filepath.ToSlash(rel)
}
