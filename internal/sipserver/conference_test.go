package sipserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"

	"mcs/internal/config"
	"mcs/internal/store"
)

type confClient struct {
	ua        *sipgo.UserAgent
	dcs       *sipgo.DialogClientSession
	conn      *net.UDPConn
	mcs       *net.UDPAddr
	videoConn *net.UDPConn
	videoMCS  *net.UDPAddr
}

func joinConference(t *testing.T, ctx context.Context, port int, ext string) *confClient {
	t.Helper()
	ua, err := sipgo.NewUA()
	if err != nil {
		t.Fatal(err)
	}
	client, err := sipgo.NewClient(ua)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	dua := &sipgo.DialogUA{Client: client, ContactHDR: sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: ext, Host: "127.0.0.1", Port: 15076}}}
	sdp := []byte("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio " + itoa(conn.LocalAddr().(*net.UDPAddr).Port) + " RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\na=sendrecv\r\n")
	dcs, err := dua.Invite(ctx, sip.Uri{Scheme: "sip", User: "7000", Host: "127.0.0.1", Port: port}, sdp)
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := dcs.Ack(ctx); err != nil {
		t.Fatal(err)
	}
	info := parseSDP(dcs.InviteResponse.Body())
	return &confClient{ua: ua, dcs: dcs, conn: conn, mcs: &net.UDPAddr{IP: net.ParseIP(info.ConnIP), Port: info.AudioPort}}
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

func TestConferenceMix(t *testing.T) {
	const sipPort = 15076
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := store.New(nil)
	srv, err := New(st, NewAuthenticator("mcs", "test"), store.NewCDRStore(10), store.NewAlarmStore(10), Options{
		Domain: "mcs.local", Advertise: "127.0.0.1:15076", DataDir: t.TempDir(),
		Conference: config.ConferenceConfig{Enabled: true, Rooms: []config.ConferenceRoomConfig{{Number: "7000", Name: "test", MaxMembers: 8}}},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ctx, "127.0.0.1:15076") }()
	time.Sleep(150 * time.Millisecond)
	defer srv.Close()

	a := joinConference(t, ctx, sipPort, "1001")
	defer a.ua.Close()
	defer a.conn.Close()
	b := joinConference(t, ctx, sipPort, "1002")
	defer b.ua.Close()
	defer b.conn.Close()

	time.Sleep(100 * time.Millisecond)
	if got := len(srv.ConferenceView()); got != 1 || srv.ConferenceView()[0].Active != 2 {
		t.Fatalf("conference view = %+v", srv.ConferenceView())
	}

	// Each endpoint sends a constant µ-law frame. The mixer must deliver the
	// other party's audio, not silence.
	frame := make([]byte, 160)
	for i := range frame {
		frame[i] = 0x00
	}
	for i := 0; i < 20; i++ {
		for _, c := range []*confClient{a, b} {
			pkt := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: uint16(i), Timestamp: uint32(i * 160), SSRC: 1}, Payload: frame}
			raw, _ := pkt.Marshal()
			if _, err := c.conn.WriteToUDP(raw, c.mcs); err != nil {
				t.Logf("send rtp %s: %v", c.dcs.InviteRequest.From().Address.User, err)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	deadline := time.Now().Add(2 * time.Second)
	_ = a.conn.SetReadDeadline(deadline)
	buf := make([]byte, 1500)
	received := 0
	nonSilent := false
	for time.Now().Before(deadline) && !(received >= 5 && nonSilent) {
		n, _, err := a.conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		var pkt rtp.Packet
		if pkt.Unmarshal(buf[:n]) == nil {
			received++
			for _, b := range pkt.Payload {
				if b != 0xFF {
					nonSilent = true
					break
				}
			}
		}
	}
	if received < 3 {
		t.Fatalf("conference output packets = %d", received)
	}
	if !nonSilent {
		t.Fatal("conference output is silence")
	}
}

func joinConferenceWithVideo(t *testing.T, ctx context.Context, port int, ext string) *confClient {
	t.Helper()
	ua, err := sipgo.NewUA()
	if err != nil {
		t.Fatal(err)
	}
	client, err := sipgo.NewClient(ua)
	if err != nil {
		t.Fatal(err)
	}
	aconn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	vconn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		_ = aconn.Close()
		t.Fatal(err)
	}
	dua := &sipgo.DialogUA{Client: client, ContactHDR: sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: ext, Host: "127.0.0.1", Port: 15076}}}
	sdp := []byte("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio " + itoa(aconn.LocalAddr().(*net.UDPAddr).Port) + " RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\na=sendrecv\r\nm=video " + itoa(vconn.LocalAddr().(*net.UDPAddr).Port) + " RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=fmtp:96 packetization-mode=1\r\na=sendrecv\r\n")
	dcs, err := dua.Invite(ctx, sip.Uri{Scheme: "sip", User: "7000", Host: "127.0.0.1", Port: port}, sdp)
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := dcs.Ack(ctx); err != nil {
		t.Fatal(err)
	}
	info := parseSDP(dcs.InviteResponse.Body())
	return &confClient{ua: ua, dcs: dcs, conn: aconn, mcs: &net.UDPAddr{IP: net.ParseIP(info.ConnIP), Port: info.AudioPort}, videoConn: vconn, videoMCS: &net.UDPAddr{IP: net.ParseIP(info.ConnIP), Port: info.VideoPort}}
}

func TestConferenceVideoForward(t *testing.T) {
	const sipPort = 15079
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := store.New(nil)
	srv, err := New(st, NewAuthenticator("mcs", "test"), store.NewCDRStore(10), store.NewAlarmStore(10), Options{
		Domain: "mcs.local", Advertise: "127.0.0.1:15079", DataDir: t.TempDir(),
		Conference: config.ConferenceConfig{Enabled: true, Rooms: []config.ConferenceRoomConfig{{Number: "7000", Name: "test", MaxMembers: 8}}},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ctx, "127.0.0.1:15079") }()
	time.Sleep(150 * time.Millisecond)
	defer srv.Close()
	a := joinConferenceWithVideo(t, ctx, sipPort, "1001")
	defer a.ua.Close()
	defer a.conn.Close()
	defer a.videoConn.Close()
	b := joinConferenceWithVideo(t, ctx, sipPort, "1002")
	defer b.ua.Close()
	defer b.conn.Close()
	defer b.videoConn.Close()
	time.Sleep(100 * time.Millisecond)
	if b.videoMCS == nil || b.videoMCS.Port == 0 {
		t.Fatalf("no video answer: %+v", b.videoMCS)
	}
	pkt := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: 1, Timestamp: 1, SSRC: 42}, Payload: []byte{0x00, 0x00, 0x00, 0x01, 0x09}}
	raw, _ := pkt.Marshal()
	for i := 0; i < 3; i++ {
		_, _ = a.videoConn.WriteToUDP(raw, a.videoMCS)
		time.Sleep(20 * time.Millisecond)
	}
	_ = b.videoConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := b.videoConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("conference video not forwarded: %v", err)
	}
	var got rtp.Packet
	if err := got.Unmarshal(buf[:n]); err != nil || got.PayloadType != 96 {
		t.Fatalf("bad forwarded video packet: %v pt=%d", err, got.PayloadType)
	}
}

func TestConferenceRecorderWritesWAV(t *testing.T) {
	dir := t.TempDir()
	r := newConferenceRecorder(dir, "7000")
	if r == nil {
		t.Fatal("recorder nil")
	}
	samples := make([]int32, conferenceFrameSamples)
	for i := range samples {
		samples[i] = 1000
	}
	r.write(samples)
	url := r.close()
	if url == "" {
		t.Fatal("empty recording URL")
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(url, "/")))); err != nil {
		t.Fatalf("recording file: %v", err)
	}
}

func TestConferenceRoomCRUD(t *testing.T) {
	st := store.New([]config.DeviceConfig{{Ext: "1001", Password: "pass"}})
	srv, err := New(st, NewAuthenticator("mcs", "test"), store.NewCDRStore(10), store.NewAlarmStore(10), Options{
		Domain: "mcs.local", Advertise: "127.0.0.1:15077", DataDir: t.TempDir(),
		Alarm: config.AlarmConfig{Number: "8000"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.CreateConferenceRoom("7000", "test", 8, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.conference.find("7000"); !ok {
		t.Fatal("room not found")
	}
	if err := srv.CreateConferenceRoom("1001", "conflict", 8, false); err == nil {
		t.Fatal("expected extension conflict")
	}
	if err := srv.UpdateConferenceRoom("7000", "test2", 4, true, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.conference.find("7000"); ok {
		t.Fatal("disabled room should not be found")
	}
	if err := srv.UpdateConferenceRoom("7000", "test2", 4, true, true); err != nil {
		t.Fatal(err)
	}
	if err := srv.DeleteConferenceRoom("7000"); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.conference.find("7000"); ok {
		t.Fatal("deleted room found")
	}
}

func TestConferencePCMBufferPtime(t *testing.T) {
	p := &conferenceParticipant{}
	// Two 30 ms G.711 packets contain 480 samples = exactly three 20 ms
	// frames. They must not be truncated to two frames.
	for i := 0; i < 2; i++ {
		samples := make([]int16, 240)
		for j := range samples {
			samples[j] = int16(i*100 + j)
		}
		p.enqueuePCM(samples)
	}
	if f := p.takeFrame(); len(f) != conferenceFrameSamples {
		t.Fatalf("frame 1 = %d", len(f))
	}
	if f := p.takeFrame(); len(f) != conferenceFrameSamples {
		t.Fatalf("frame 2 = %d", len(f))
	}
	if f := p.takeFrame(); len(f) != conferenceFrameSamples {
		t.Fatalf("frame 3 = %d", len(f))
	}
	if f := p.takeFrame(); f != nil {
		t.Fatalf("unexpected extra frame: %d", len(f))
	}
}
