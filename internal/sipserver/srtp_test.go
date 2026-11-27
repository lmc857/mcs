package sipserver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtcp"
	"github.com/pion/srtp/v3"

	"mcs/internal/config"
	"mcs/internal/store"
)

func TestSRTPKeysAndSDPRewrite(t *testing.T) {
	offerKey, err := newSRTPKey()
	if err != nil {
		t.Fatal(err)
	}
	answerKey, err := newSRTPKey()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("v=0\r\no=- 1 1 IN IP4 10.0.0.5\r\ns=-\r\nc=IN IP4 10.0.0.5\r\nt=0 0\r\n" +
		"m=audio 10000 RTP/SAVP 0\r\na=rtpmap:0 PCMU/8000\r\na=crypto:" + offerKey.SDP + "\r\n" +
		"m=video 10002 RTP/SAVPF 96\r\na=rtpmap:96 H264/90000\r\na=crypto:" + offerKey.SDP + "\r\n")
	info := parseSDP(body)
	if !info.AudioSecure || !info.AudioCrypto.OK || info.AudioCrypto.Suite != "AES_CM_128_HMAC_SHA1_80" {
		t.Fatalf("parse = %+v", info)
	}
	if !info.VideoSecure || !info.VideoCrypto.OK || info.VideoCodec != "H264" {
		t.Fatalf("parse video = %+v", info)
	}
	out, err := rewriteSDPWithCrypto(body, 20000, 20002, "192.0.2.1", answerKey.SDP, answerKey.SDP)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(out, []byte("a=crypto:")) != 2 {
		t.Fatalf("crypto lines = %d\n%s", bytes.Count(out, []byte("a=crypto:")), out)
	}
	info2 := parseSDP(out)
	if info2.AudioPort != 20000 || !info2.AudioSecure || !info2.AudioCrypto.OK {
		t.Fatalf("rewritten = %+v", info2)
	}
	if info2.VideoPort != 20002 || !info2.VideoSecure || !info2.VideoCrypto.OK {
		t.Fatalf("rewritten video = %+v", info2)
	}
	if info2.AudioCrypto.Key != answerKey.SDP[strings.Index(answerKey.SDP, "inline:")+7:] {
		t.Fatalf("rewritten key mismatch")
	}
}

func secureSDP(port int, key *srtpKey) []byte {
	return []byte(fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n"+
		"m=audio %d RTP/SAVP 0\r\na=rtpmap:0 PCMU/8000\r\na=crypto:%s\r\n", port, key.SDP))
}

func newTestSRTPContext(t *testing.T, k *srtpKey) *srtp.Context {
	t.Helper()
	ctx, err := k.context()
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestSRTPCall(t *testing.T) {
	const mcsPort = 25081
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := store.New([]config.DeviceConfig{{Ext: "1002", Name: "secure phone", Password: "pass-1002"}})
	srv, err := New(st, NewAuthenticator("mcs", "test"), store.NewCDRStore(10), store.NewAlarmStore(10), Options{
		Domain: "mcs.local", Advertise: fmt.Sprintf("127.0.0.1:%d", mcsPort), DataDir: t.TempDir(),
		Alarm: config.AlarmConfig{Number: "8000"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ctx, fmt.Sprintf("127.0.0.1:%d", mcsPort)) }()
	defer srv.Close()
	time.Sleep(150 * time.Millisecond)

	aKey, _ := newSRTPKey()
	bKey, _ := newSRTPKey()
	uaB, _ := sipgo.NewUA()
	defer uaB.Close()
	srvB, _ := sipgo.NewServer(uaB)
	duaB := &sipgo.DialogUA{Client: mustClient(t, uaB), ContactHDR: sip.ContactHeader{Address: sip.Uri{
		Scheme: "sip", User: "1002", Host: "127.0.0.1", Port: 28011,
	}}}
	var mcsBKey atomic.Pointer[srtpKey]
	var mcsBPort atomic.Int32
	srvB.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		offer := parseSDP(req.Body())
		if !offer.AudioSecure || !offer.AudioCrypto.OK {
			_ = tx.Respond(sip.NewResponseFromRequest(req, 488, "SRTP Required", nil))
			return
		}
		k, err := parseSRTPKey(offer.AudioCrypto)
		if err != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, 488, "Bad Crypto", nil))
			return
		}
		mcsBKey.Store(k)
		mcsBPort.Store(int32(offer.AudioPort))
		dss, err := duaB.ReadInvite(req, tx)
		if err != nil {
			return
		}
		_ = dss.Respond(180, "Ringing", nil)
		_ = dss.RespondSDP(secureSDP(28121, bKey))
	})
	srvB.OnAck(func(*sip.Request, sip.ServerTransaction) {})
	srvB.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	go func() { _ = srvB.ListenAndServe(ctx, "udp", "127.0.0.1:28011") }()
	time.Sleep(150 * time.Millisecond)

	uaReg, _ := sipgo.NewUA()
	defer uaReg.Close()
	registerForTest(t, mustClient(t, uaReg), mcsPort, "1002", "pass-1002", "sip:1002@127.0.0.1:28011")

	uaA, _ := sipgo.NewUA()
	defer uaA.Close()
	duaA := &sipgo.DialogUA{Client: mustClient(t, uaA), ContactHDR: sip.ContactHeader{Address: sip.Uri{
		Scheme: "sip", User: "1001", Host: "127.0.0.1", Port: 16021,
	}}}
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: "1002", Host: "127.0.0.1", Port: mcsPort})
	req.AppendHeader(sip.NewHeader("From", "<sip:1001@mcs.local>;tag=srtp"))
	req.AppendHeader(sip.NewHeader("To", "<sip:1002@mcs.local>"))
	req.AppendHeader(sip.NewHeader("Call-ID", "srtp-call"))
	req.AppendHeader(sip.NewHeader("CSeq", "1 INVITE"))
	req.AppendHeader(sip.NewHeader("Contact", "<sip:1001@127.0.0.1:16021>"))
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody(secureSDP(28120, aKey))

	callCtx, callCancel := context.WithTimeout(ctx, 8*time.Second)
	defer callCancel()
	dcs, err := duaA.WriteInvite(callCtx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(callCtx, sipgo.AnswerOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := dcs.Ack(callCtx); err != nil {
		t.Fatal(err)
	}
	answer := parseSDP(dcs.InviteResponse.Body())
	if !answer.AudioSecure || !answer.AudioCrypto.OK {
		t.Fatalf("answer not SRTP: %+v", answer)
	}
	mcsAKey, err := parseSRTPKey(answer.AudioCrypto)
	if err != nil {
		t.Fatal(err)
	}
	offerBKey := mcsBKey.Load()
	if offerBKey == nil {
		t.Fatal("callee saw no SRTP offer")
	}
	time.Sleep(100 * time.Millisecond)

	aEnc := newTestSRTPContext(t, aKey)
	aDec := newTestSRTPContext(t, mcsAKey)
	bEnc := newTestSRTPContext(t, bKey)
	bDec := newTestSRTPContext(t, offerBKey)

	aConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 28120})
	if err != nil {
		t.Fatal(err)
	}
	defer aConn.Close()
	bConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 28121})
	if err != nil {
		t.Fatal(err)
	}
	defer bConn.Close()

	var aGot, bGot, bRTCP atomic.Int32
	go func() {
		buf := make([]byte, 2048)
		for {
			_ = aConn.SetReadDeadline(time.Now().Add(3 * time.Second))
			n, _, err := aConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if _, err := aDec.DecryptRTP(nil, buf[:n], nil); err == nil {
				aGot.Add(1)
			}
		}
	}()
	go func() {
		buf := make([]byte, 2048)
		for {
			_ = bConn.SetReadDeadline(time.Now().Add(3 * time.Second))
			n, _, err := bConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if isRTCP(buf[:n]) {
				if _, err := bDec.DecryptRTCP(nil, buf[:n], nil); err == nil {
					bRTCP.Add(1)
				}
				continue
			}
			if _, err := bDec.DecryptRTP(nil, buf[:n], nil); err == nil {
				bGot.Add(1)
			}
		}
	}()

	aToMCS := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: answer.AudioPort}
	bPort := int(mcsBPort.Load())
	if bPort == 0 {
		t.Fatal("B-leg relay port not captured")
	}
	bToMCS := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: bPort}

	for i := 0; i < 30; i++ {
		pkt := rtpPacket(uint16(i), 0xFF)
		enc, err := aEnc.EncryptRTP(nil, pkt, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = aConn.WriteToUDP(enc, aToMCS)
		encB, err := bEnc.EncryptRTP(nil, pkt, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = bConn.WriteToUDP(encB, bToMCS)
		time.Sleep(5 * time.Millisecond)
	}
	rr := &rtcp.ReceiverReport{SSRC: 0x11223344}
	rawRR, err := rr.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	encRR, err := aEnc.EncryptRTCP(nil, rawRR, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aConn.WriteToUDP(encRR, aToMCS)
	time.Sleep(300 * time.Millisecond)
	if aGot.Load() == 0 || bGot.Load() == 0 {
		t.Fatalf("SRTP relay a=%d b=%d", aGot.Load(), bGot.Load())
	}
	if bRTCP.Load() == 0 {
		t.Fatal("SRTCP was not forwarded")
	}
	_ = dcs.Bye(callCtx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(srv.ActiveCalls()) > 0 {
		time.Sleep(30 * time.Millisecond)
	}
}

func TestSDPRewriteCanonicalMediaOrder(t *testing.T) {
	body := []byte("v=0\r\no=- 1 1 IN IP4 10.0.0.5\r\ns=-\r\nc=IN IP4 10.0.0.5\r\nt=0 0\r\n" +
		"m=audio 10000 RTP/SAVP 0\r\n" +
		"i=audio media\r\n" +
		"c=IN IP4 10.0.0.5\r\n" +
		"b=AS:64\r\n" +
		"k=prompt\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n" +
		"a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:old\r\n" +
		"m=video 10002 RTP/SAVPF 96\r\n" +
		"i=video media\r\n" +
		"c=IN IP4 10.0.0.5\r\n" +
		"b=TIAS:64000\r\n" +
		"k=prompt\r\n" +
		"a=rtpmap:96 H264/90000\r\n" +
		"a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:old\r\n")
	out, err := rewriteSDPWithCrypto(body, 20000, 20002, "192.0.2.1",
		"1 AES_CM_128_HMAC_SHA1_80 inline:audio", "1 AES_CM_128_HMAC_SHA1_80 inline:video")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	var mediaStart []int
	for i, line := range lines {
		if strings.HasPrefix(line, "m=") {
			mediaStart = append(mediaStart, i)
		}
	}
	if len(mediaStart) != 2 {
		t.Fatalf("media sections = %d\n%s", len(mediaStart), out)
	}
	for n, start := range mediaStart {
		end := len(lines)
		if n+1 < len(mediaStart) {
			end = mediaStart[n+1]
		}
		order := []string{}
		for _, line := range lines[start:end] {
			if len(line) > 0 {
				order = append(order, line[:2])
			}
		}
		want := []string{"m=", "i=", "c=", "b=", "k=", "a=", "a="}
		if n == 0 {
			want = []string{"m=", "i=", "c=", "b=", "k=", "a=", "a="}
		}
		if strings.Join(order, ",") != strings.Join(want, ",") {
			t.Fatalf("media %d order = %v, want %v\n%s", n, order, want, out)
		}
	}
	if bytes.Count(out, []byte("a=crypto:")) != 2 {
		t.Fatalf("crypto lines = %d\n%s", bytes.Count(out, []byte("a=crypto:")), out)
	}
	if strings.Contains(string(out), "inline:old") {
		t.Fatalf("old crypto key leaked:\n%s", out)
	}
}

func TestSDPRewriteTransportAddresses(t *testing.T) {
	body := []byte("v=0\r\n" +
		"o=- 4004784293 4004784293 IN IP4 198.18.0.1\r\n" +
		"s=-\r\n" +
		"c=IN IP4 198.18.0.1\r\n" +
		"t=0 0\r\n" +
		"m=audio 10000 RTP/SAVP 0\r\n" +
		"a=rtcp:4019 IN IP4 198.18.0.1\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n" +
		"a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:old\r\n")
	out, err := rewriteSDPWithCrypto(body, 20000, 0, "192.168.68.106",
		"1 AES_CM_128_HMAC_SHA1_80 inline:new", "")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if strings.Contains(text, "198.18.0.1") {
		t.Fatalf("fake IP remained:\n%s", text)
	}
	if !strings.Contains(text, "o=- 4004784293 4004784293 IN IP4 192.168.68.106") {
		t.Fatalf("origin not rewritten:\n%s", text)
	}
	if !strings.Contains(text, "c=IN IP4 192.168.68.106") {
		t.Fatalf("connection not rewritten:\n%s", text)
	}
	if !strings.Contains(text, "a=rtcp:20000 IN IP4 192.168.68.106") {
		t.Fatalf("RTCP port/address not rewritten:\n%s", text)
	}
	if strings.Contains(text, "a=rtcp:4019") {
		t.Fatalf("old RTCP port remained:\n%s", text)
	}
}
