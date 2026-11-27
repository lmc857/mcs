package sipserver

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/webrtc/v4"

	"mcs/internal/store"
)

type pagingSession struct {
	id         string
	zoneID     string
	dss        *sipgo.DialogServerSession
	conn       *net.UDPConn
	bc         *broadcastSession
	srcIP      net.IP
	source     string
	sourceName string
	kind       string
	webPC      *webrtc.PeerConnection
	startedAt  time.Time
	byeCh      chan struct{}
	once       sync.Once
	server     *Server
}

func (s *Server) handlePagingInvite(req *sip.Request, tx sip.ServerTransaction, z zone) {
	if !s.pagingCfg.Enabled {
		s.respond(req, tx, 480, "Temporarily Unavailable")
		return
	}
	caller := pagingCaller(req)
	if !s.pagingAllowed(caller) {
		s.log.Warn("paging forbidden", "zone", z.id, "ext", caller)
		s.respond(req, tx, 403, "Forbidden")
		return
	}
	body := req.Body()
	if len(body) == 0 {
		s.respond(req, tx, 488, "Not Acceptable Here")
		return
	}
	offer := parseSDP(body)
	if !offer.HasAudio {
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
	bc, err := s.StartLiveBroadcast(z.id, 2)
	if err != nil {
		_ = conn.Close()
		code, reason := 480, "Temporarily Unavailable"
		if err == ErrZoneBusy {
			code, reason = 486, "Busy Here"
		}
		s.log.Warn("paging rejected", "zone", z.id, "code", code, "error", err)
		_ = dss.Respond(code, reason, nil)
		return
	}
	var srcIP net.IP
	if host, _, err := net.SplitHostPort(req.Source()); err == nil {
		srcIP = net.ParseIP(host)
	}
	ps := &pagingSession{
		id: dss.InviteRequest.CallID().Value(), zoneID: z.id, dss: dss, conn: conn,
		bc: bc, srcIP: srcIP, source: caller, sourceName: pagingSourceName(s.store, caller),
		kind: "sip", startedAt: time.Now(), byeCh: make(chan struct{}), server: s,
	}
	s.pagingAdd(ps)
	s.pagingRecordStart(ps)
	_ = dss.Respond(100, "Trying", nil)
	go ps.readRTP()

	sdp := pagingSDP(s.advertiseHost, conn.LocalAddr().(*net.UDPAddr).Port)
	if err := s.respondSDPWithContact(dss, sdp); err != nil {
		ps.stop()
		return
	}
	s.log.Info("paging started", "call_id", ps.id, "zone", z.id, "ext", aor(req))

	select {
	case <-ps.byeCh:
	case <-bc.doneCh:
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = dss.Bye(ctx)
		cancel()
		ps.stop()
	}
	s.log.Info("paging ended", "call_id", ps.id, "zone", z.id)
}

func pagingSDP(host string, port int) []byte {
	return []byte(fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 %s\r\ns=MCS Paging\r\nc=IN IP4 %s\r\nt=0 0\r\nm=audio %d RTP/AVP 0 8\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:8 PCMA/8000\r\na=recvonly\r\n", host, host, port))
}

func (p *pagingSession) readRTP() {
	buf := make([]byte, 2048)
	for {
		n, addr, err := p.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if p.srcIP != nil && !addr.IP.Equal(p.srcIP) {
			continue
		}
		payload, pt, ok := rtpPayload(buf[:n])
		if !ok || len(payload) == 0 {
			continue
		}
		switch pt {
		case 8: // PCMA -> PCM -> PCMU
			payload = encodeULaw(decodeALawBytes(payload))
		case 0: // PCMU
		default:
			continue
		}
		for len(payload) > 0 {
			n := 160
			if len(payload) < n {
				n = len(payload)
			}
			frame := make([]byte, 160)
			copy(frame, payload[:n])
			if n < 160 {
				for i := n; i < 160; i++ {
					frame[i] = 0xFF
				}
			}
			payload = payload[n:]
			if p.bc == nil || p.bc.liveCh == nil {
				return
			}
			select {
			case p.bc.liveCh <- frame:
			default: // drop oldest-ish; paging favors low latency
			}
		}
	}
}

func decodeALawBytes(payload []byte) []int16 {
	out := make([]int16, len(payload))
	for i, b := range payload {
		out[i] = decodeALaw(b)
	}
	return out
}

func (p *pagingSession) stop() {
	p.once.Do(func() {
		close(p.byeCh)
		if p.conn != nil {
			_ = p.conn.Close()
		}
		if p.webPC != nil {
			_ = p.webPC.Close()
		}
		p.server.pagingRemove(p.id)
		if p.bc != nil {
			p.bc.stop("stopped")
		}
		p.server.pagingRecordFinish(p)
	})
}

func pagingCaller(req *sip.Request) string {
	if f := req.From(); f != nil && f.Address.User != "" {
		return f.Address.User
	}
	return aor(req)
}

func pagingSourceName(st *store.Store, ext string) string {
	if st != nil {
		if v, ok := st.Get(ext); ok && v.Name != "" {
			return v.Name
		}
	}
	return ext
}

func (s *Server) pagingAllowed(ext string) bool {
	if len(s.pagingCfg.AllowedExts) == 0 {
		return true
	}
	for _, allowed := range s.pagingCfg.AllowedExts {
		if strings.TrimSpace(allowed) == ext {
			return true
		}
	}
	return false
}

func (s *Server) pagingRecordStart(p *pagingSession) {
	if s.store == nil {
		return
	}
	z := s.zoneByID(p.zoneID)
	_ = s.store.SavePagingEvent(store.PagingEvent{
		ID: p.id, Source: p.source, SourceName: p.sourceName, ZoneID: p.zoneID, ZoneName: z.name,
		Kind: p.kind, StartedAt: p.startedAt.Format(time.RFC3339), Status: "active",
	})
}

func (s *Server) pagingRecordFinish(p *pagingSession) {
	if s.store == nil {
		return
	}
	delivered, total := 0, 0
	if p.bc != nil {
		v := p.bc.view()
		delivered, total = v.Delivered, v.Total
	}
	z := s.zoneByID(p.zoneID)
	_ = s.store.SavePagingEvent(store.PagingEvent{
		ID: p.id, Source: p.source, SourceName: p.sourceName, ZoneID: p.zoneID, ZoneName: z.name,
		Kind: p.kind, StartedAt: p.startedAt.Format(time.RFC3339), EndedAt: time.Now().Format(time.RFC3339),
		Status: "stopped", Delivered: delivered, Total: total,
	})
}

func (s *Server) zoneByID(id string) zone {
	s.zonesMu.RLock()
	defer s.zonesMu.RUnlock()
	for _, z := range s.zones {
		if z.id == id {
			return z
		}
	}
	return zone{id: id}
}

func (s *Server) pagingAdd(p *pagingSession) {
	s.pagingMu.Lock()
	s.paging[p.id] = p
	s.pagingMu.Unlock()
}

func (s *Server) pagingGet(id string) *pagingSession {
	s.pagingMu.Lock()
	defer s.pagingMu.Unlock()
	return s.paging[id]
}

func (s *Server) pagingRemove(id string) {
	s.pagingMu.Lock()
	delete(s.paging, id)
	s.pagingMu.Unlock()
}
