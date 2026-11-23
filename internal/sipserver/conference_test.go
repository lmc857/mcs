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
	ua   *sipgo.UserAgent
	dcs  *sipgo.DialogClientSession
	conn *net.UDPConn
	mcs  *net.UDPAddr
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
