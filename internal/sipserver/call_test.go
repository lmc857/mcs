package sipserver

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mcs/internal/config"
	"mcs/internal/store"
)

func testSDP(port int) []byte {
	return []byte(fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio %d RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n", port))
}

func registerForTest(t *testing.T, client *sipgo.Client, port int, ext, pass, contact string) {
	t.Helper()
	req := sip.NewRequest(sip.REGISTER, sip.Uri{Scheme: "sip", Host: "127.0.0.1", Port: port})
	req.AppendHeader(sip.NewHeader("From", fmt.Sprintf("<sip:%s@mcs.local>;tag=%s", ext, ext)))
	req.AppendHeader(sip.NewHeader("To", fmt.Sprintf("<sip:%s@mcs.local>", ext)))
	req.AppendHeader(sip.NewHeader("Call-ID", "reg-"+ext))
	req.AppendHeader(sip.NewHeader("CSeq", "1 REGISTER"))
	req.AppendHeader(sip.NewHeader("Contact", fmt.Sprintf("<%s>", contact)))
	req.AppendHeader(sip.NewHeader("Expires", "300"))
	res, err := client.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("register %s: %v", ext, err)
	}
	if res.StatusCode == 401 {
		res, err = client.DoDigestAuth(context.Background(), req, res, sipgo.DigestAuth{Username: ext, Password: pass})
		if err != nil {
			t.Fatalf("digest register %s: %v", ext, err)
		}
	}
	if res.StatusCode != 200 {
		t.Fatalf("register %s status %d", ext, res.StatusCode)
	}
}

func TestCallRelayAndRecording(t *testing.T) {
	const (
		mcsPort    = 25073
		calleePort = 26002
		callerRTP  = 27000
		calleeRTP  = 27002
	)
	dataDir := t.TempDir()
	st := store.New([]config.DeviceConfig{
		{Ext: "1001", Name: "caller", Password: "pass-1001"},
		{Ext: "1002", Name: "callee", Password: "pass-1002"},
	})
	cdr := store.NewCDRStore(10)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	alarms := store.NewAlarmStore(10)
	srv, err := New(st, NewAuthenticator("mcs", "test-secret"), cdr, alarms, Options{
		Domain: "mcs.local", Advertise: fmt.Sprintf("127.0.0.1:%d", mcsPort), DataDir: dataDir,
		Alarm: config.AlarmConfig{Number: "8000"},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, fmt.Sprintf("127.0.0.1:%d", mcsPort)) }()
	defer srv.Close()
	time.Sleep(150 * time.Millisecond)

	// Callee UAS.
	uaB, err := sipgo.NewUA()
	if err != nil {
		t.Fatal(err)
	}
	defer uaB.Close()
	srvB, err := sipgo.NewServer(uaB, sipgo.WithServerLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	duaB := &sipgo.DialogUA{Client: mustClient(t, uaB), ContactHDR: sip.ContactHeader{Address: sip.Uri{
		Scheme: "sip", User: "1002", Host: "127.0.0.1", Port: calleePort,
	}}}
	var (
		mu        sync.Mutex
		calleeDSS *sipgo.DialogServerSession
		calleeSDP sdpInfo
	)
	srvB.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		dss, err := duaB.ReadInvite(req, tx)
		if err != nil {
			t.Logf("callee ReadInvite error: %v", err)
		}
		if err != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
			return
		}
		info := parseSDP(req.Body())
		mu.Lock()
		calleeDSS = dss
		calleeSDP = info
		mu.Unlock()
		res180 := sip.NewResponseFromRequest(dss.InviteRequest, 180, "Ringing", nil)
		res180.AppendHeader(&duaB.ContactHDR)
		if err := dss.WriteResponse(res180); err != nil {
			t.Logf("callee 180 error: %v", err)
		}
		if err := dss.RespondSDP(testSDP(calleeRTP)); err != nil {
			t.Logf("callee 200 error: %v", err)
		}
	})
	srvB.OnAck(func(*sip.Request, sip.ServerTransaction) {})
	unlockCh := make(chan string, 1)
	srvB.OnInfo(func(req *sip.Request, tx sip.ServerTransaction) {
		select {
		case unlockCh <- string(req.Body()):
		default:
		}
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	srvB.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	go func() {
		if err := srvB.ListenAndServe(ctx, "udp", fmt.Sprintf("127.0.0.1:%d", calleePort)); err != nil {
			t.Logf("callee server stopped: %v", err)
		}
	}()
	time.Sleep(150 * time.Millisecond)

	// Register both extensions.
	uaReg, err := sipgo.NewUA()
	if err != nil {
		t.Fatal(err)
	}
	defer uaReg.Close()
	regClient := mustClient(t, uaReg)
	registerForTest(t, regClient, mcsPort, "1001", "pass-1001", "sip:1001@127.0.0.1:16001")
	registerForTest(t, regClient, mcsPort, "1002", "pass-1002", fmt.Sprintf("sip:1002@127.0.0.1:%d", calleePort))

	// Caller INVITE to alarm short number 8000.
	uaA, err := sipgo.NewUA()
	if err != nil {
		t.Fatal(err)
	}
	defer uaA.Close()
	duaA := &sipgo.DialogUA{Client: mustClient(t, uaA), ContactHDR: sip.ContactHeader{Address: sip.Uri{
		Scheme: "sip", User: "1001", Host: "127.0.0.1", Port: 16001,
	}}}
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: "8000", Host: "127.0.0.1", Port: mcsPort})
	req.AppendHeader(sip.NewHeader("From", "<sip:1001@mcs.local>;tag=caller"))
	req.AppendHeader(sip.NewHeader("To", "<sip:8000@mcs.local>"))
	req.AppendHeader(sip.NewHeader("Call-ID", "call-test-a"))
	req.AppendHeader(sip.NewHeader("CSeq", "1 INVITE"))
	req.AppendHeader(sip.NewHeader("Contact", "<sip:1001@127.0.0.1:16001>"))
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody(testSDP(callerRTP))
	dcsA, err := duaA.WriteInvite(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := dcsA.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("wait answer: %v", err)
	}
	answer := parseSDP(dcsA.InviteResponse.Body())
	if answer.AudioPort == 0 {
		t.Fatalf("no relay port in answer: %s", dcsA.InviteResponse.Body())
	}
	if err := dcsA.Ack(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	active := srv.ActiveCalls()
	if len(active) != 1 {
		t.Fatalf("active calls = %d", len(active))
	}
	unlockCtx, unlockCancel := context.WithTimeout(ctx, 3*time.Second)
	defer unlockCancel()
	if err := srv.UnlockCall(unlockCtx, active[0].CallID, "#"); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	select {
	case body := <-unlockCh:
		if !strings.Contains(body, "Signal=#") {
			t.Fatalf("unlock body = %q", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("door station did not receive unlock INFO")
	}

	mu.Lock()
	gotOffer := calleeSDP
	dss := calleeDSS
	mu.Unlock()
	if gotOffer.AudioPort == 0 || dss == nil {
		t.Fatalf("callee did not receive offer: %+v", gotOffer)
	}

	// Send RTP both ways.
	connA, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: callerRTP})
	if err != nil {
		t.Fatal(err)
	}
	defer connA.Close()
	connB, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: calleeRTP})
	if err != nil {
		t.Fatal(err)
	}
	defer connB.Close()

	dstA := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: answer.AudioPort}
	dstB := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: gotOffer.AudioPort}
	for i := 0; i < 50; i++ {
		_, _ = connA.WriteToUDP(rtpPacket(uint16(i), 0x00), dstA)
		_, _ = connB.WriteToUDP(rtpPacket(uint16(i), 0xff), dstB)
	}
	time.Sleep(200 * time.Millisecond)

	if err := dcsA.Bye(ctx); err != nil {
		t.Fatalf("bye: %v", err)
	}

	var rec store.CDR
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		items := cdr.List(1)
		if len(items) == 1 {
			rec = items[0]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if rec.ID == "" {
		t.Fatal("no CDR written")
	}
	if rec.Result != "answered" {
		t.Fatalf("cdr result = %q", rec.Result)
	}
	if rec.Recording == "" {
		t.Fatal("no recording path in CDR")
	}
	path := filepath.Join(dataDir, strings.TrimPrefix(rec.Recording, "/"))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("recording: %v", err)
	}
	if info.Size() < 10000 {
		t.Fatalf("recording too small: %d", info.Size())
	}
}

func mustClient(t *testing.T, ua *sipgo.UserAgent) *sipgo.Client {
	t.Helper()
	c, err := sipgo.NewClient(ua)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func rtpPacket(seq uint16, payloadByte byte) []byte {
	payload := make([]byte, 160)
	for i := range payload {
		payload[i] = payloadByte
	}
	pkt := make([]byte, 12+len(payload))
	pkt[0] = 0x80
	pkt[1] = 0
	binary.BigEndian.PutUint16(pkt[2:4], seq)
	binary.BigEndian.PutUint32(pkt[4:8], uint32(seq)*160)
	binary.BigEndian.PutUint32(pkt[8:12], 0x11223344)
	copy(pkt[12:], payload)
	return pkt
}

func TestAlarmEscalationTimeout(t *testing.T) {
	const (
		mcsPort  = 25074
		slowPort = 26012
		fastPort = 26013
	)
	dataDir := t.TempDir()
	st := store.New([]config.DeviceConfig{
		{Ext: "1001", Name: "caller", Password: "pass-1001"},
		{Ext: "1002", Name: "slow guard", Password: "pass-1002"},
		{Ext: "1003", Name: "fast guard", Password: "pass-1003"},
	})
	cdr := store.NewCDRStore(10)
	alarms := store.NewAlarmStore(10)
	alarmCfg := config.AlarmConfig{Number: "8000", Levels: []config.AlarmLevel{
		{Name: "L1", Members: []string{"1002"}, TimeoutS: 1},
		{Name: "L2", Members: []string{"1003"}, TimeoutS: 2},
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := New(st, NewAuthenticator("mcs", "test-secret"), cdr, alarms, Options{
		Domain: "mcs.local", Advertise: fmt.Sprintf("127.0.0.1:%d", mcsPort), DataDir: dataDir,
		Alarm: alarmCfg,
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, fmt.Sprintf("127.0.0.1:%d", mcsPort)) }()
	defer srv.Close()
	time.Sleep(150 * time.Millisecond)

	// Fast guard answers at 1003.
	uaFast, _ := sipgo.NewUA()
	defer uaFast.Close()
	srvFast, _ := sipgo.NewServer(uaFast)
	duaFast := &sipgo.DialogUA{Client: mustClient(t, uaFast), ContactHDR: sip.ContactHeader{Address: sip.Uri{
		Scheme: "sip", User: "1003", Host: "127.0.0.1", Port: fastPort,
	}}}
	srvFast.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		dss, err := duaFast.ReadInvite(req, tx)
		if err != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
			return
		}
		_ = dss.Respond(180, "Ringing", nil)
		_ = dss.RespondSDP(testSDP(27013))
	})
	srvFast.OnAck(func(*sip.Request, sip.ServerTransaction) {})
	srvFast.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	go func() { _ = srvFast.ListenAndServe(ctx, "udp", fmt.Sprintf("127.0.0.1:%d", fastPort)) }()

	// Slow guard rings but never answers; it must keep its INVITE transaction alive.
	uaSlow, _ := sipgo.NewUA()
	defer uaSlow.Close()
	srvSlow, _ := sipgo.NewServer(uaSlow)
	duaSlow := &sipgo.DialogUA{Client: mustClient(t, uaSlow), ContactHDR: sip.ContactHeader{Address: sip.Uri{
		Scheme: "sip", User: "1002", Host: "127.0.0.1", Port: slowPort,
	}}}
	srvSlow.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		dss, err := duaSlow.ReadInvite(req, tx)
		if err != nil {
			return
		}
		_ = dss.Respond(180, "Ringing", nil)
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	})
	go func() { _ = srvSlow.ListenAndServe(ctx, "udp", fmt.Sprintf("127.0.0.1:%d", slowPort)) }()
	time.Sleep(200 * time.Millisecond)

	// Register all three endpoints.
	uaReg, _ := sipgo.NewUA()
	defer uaReg.Close()
	reg := mustClient(t, uaReg)
	registerForTest(t, reg, mcsPort, "1001", "pass-1001", "sip:1001@127.0.0.1:16011")
	registerForTest(t, reg, mcsPort, "1002", "pass-1002", fmt.Sprintf("sip:1002@127.0.0.1:%d", slowPort))
	registerForTest(t, reg, mcsPort, "1003", "pass-1003", fmt.Sprintf("sip:1003@127.0.0.1:%d", fastPort))

	uaA, _ := sipgo.NewUA()
	defer uaA.Close()
	duaA := &sipgo.DialogUA{Client: mustClient(t, uaA), ContactHDR: sip.ContactHeader{Address: sip.Uri{
		Scheme: "sip", User: "1001", Host: "127.0.0.1", Port: 16011,
	}}}
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: "8000", Host: "127.0.0.1", Port: mcsPort})
	req.AppendHeader(sip.NewHeader("From", "<sip:1001@mcs.local>;tag=escalate"))
	req.AppendHeader(sip.NewHeader("To", "<sip:8000@mcs.local>"))
	req.AppendHeader(sip.NewHeader("Call-ID", "call-escalate"))
	req.AppendHeader(sip.NewHeader("CSeq", "1 INVITE"))
	req.AppendHeader(sip.NewHeader("Contact", "<sip:1001@127.0.0.1:16011>"))
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody(testSDP(27011))

	callCtx, callCancel := context.WithTimeout(ctx, 6*time.Second)
	defer callCancel()
	dcsA, err := duaA.WriteInvite(callCtx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := dcsA.WaitAnswer(callCtx, sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("escalation answer: %v", err)
	}
	if err := dcsA.Ack(callCtx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	list := alarms.List(1)
	if len(list) != 1 {
		t.Fatalf("alarms = %d", len(list))
	}
	a := list[0]
	if a.Level != 1 || a.LevelName != "L2" {
		t.Fatalf("escalation level = %d/%q, want 1/L2", a.Level, a.LevelName)
	}
	if a.State != "answered" || a.AnsweredBy != "1003" {
		t.Fatalf("alarm state=%q answered_by=%q", a.State, a.AnsweredBy)
	}
	if err := dcsA.Bye(callCtx); err != nil {
		t.Fatal(err)
	}
}

func TestContactURIForViewUsesSourceForFakeIP(t *testing.T) {
	v := store.View{
		Contact: `"1002" <sip:1002@198.18.0.1:60171;ob>`,
		Source:  "192.168.68.47:60171",
	}
	uri, err := contactURIForView(v)
	if err != nil {
		t.Fatal(err)
	}
	if uri.Host != "192.168.68.47" || uri.Port != 60171 {
		t.Fatalf("uri = %s:%d", uri.Host, uri.Port)
	}

	real := store.View{Contact: "<sip:1001@192.168.68.126:5060>", Source: "192.168.68.126:5060"}
	uri, err = contactURIForView(real)
	if err != nil {
		t.Fatal(err)
	}
	if uri.Host != "192.168.68.126" || uri.Port != 5060 {
		t.Fatalf("real uri = %s:%d", uri.Host, uri.Port)
	}
}

func TestMediaIPForSource(t *testing.T) {
	if got := mediaIPForSource("198.18.0.1", "192.168.68.47:60171"); got != "192.168.68.47" {
		t.Fatalf("fake sdp ip = %q", got)
	}
	if got := mediaIPForSource("192.168.68.47", "192.168.68.47:60171"); got != "192.168.68.47" {
		t.Fatalf("real sdp ip = %q", got)
	}
	if got := mediaIPForSource("127.0.0.1", "127.0.0.1:54321"); got != "127.0.0.1" {
		t.Fatalf("loopback = %q", got)
	}
}
