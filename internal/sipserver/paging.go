package sipserver

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

type pagingSession struct {
	id     string
	zoneID string
	dss    *sipgo.DialogServerSession
	conn   *net.UDPConn
	bc     *broadcastSession
	srcIP  net.IP
	byeCh  chan struct{}
	once   sync.Once
	server *Server
}

func (s *Server) handlePagingInvite(req *sip.Request, tx sip.ServerTransaction, z zone) {
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
		_ = dss.Respond(code, reason, nil)
		return
	}
	var srcIP net.IP
	if host, _, err := net.SplitHostPort(req.Source()); err == nil {
		srcIP = net.ParseIP(host)
	}
	ps := &pagingSession{
		id: dss.InviteRequest.CallID().Value(), zoneID: z.id, dss: dss, conn: conn,
		bc: bc, srcIP: srcIP, byeCh: make(chan struct{}), server: s,
	}
	s.pagingAdd(ps)
	_ = dss.Respond(100, "Trying", nil)
	go ps.readRTP()

	sdp := pagingSDP(s.advertiseHost, conn.LocalAddr().(*net.UDPAddr).Port)
	if err := dss.RespondSDP(sdp); err != nil {
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
		_ = p.conn.Close()
		p.server.pagingRemove(p.id)
		if p.bc != nil {
			p.bc.stop("stopped")
		}
	})
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
