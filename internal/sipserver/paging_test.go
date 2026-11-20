package sipserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mcs/internal/config"
	"mcs/internal/store"
)

func TestLivePagingToZone(t *testing.T) {
	const mcsPort = 25079
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := store.New([]config.DeviceConfig{
		{Ext: "2006", Name: "speaker 1", Password: "pass-2006"},
		{Ext: "2007", Name: "speaker 2", Password: "pass-2007"},
	})
	srv, err := New(st, NewAuthenticator("mcs", "test"), store.NewCDRStore(10), store.NewAlarmStore(10), Options{
		Domain: "mcs.local", Advertise: fmt.Sprintf("127.0.0.1:%d", mcsPort), DataDir: t.TempDir(),
		Alarm: config.AlarmConfig{Number: "8000"},
		Broadcast: config.BroadcastConfig{
			AutoAnswer: "<http://127.0.0.1>;info=alert-autoanswer", InviteTimeoutS: 3, MaxConcurrency: 4,
		},
		TTS: config.TTSConfig{BaseURL: "http://127.0.0.1:8399", VoiceZH: "zf_001", VoiceEN: "af_heart", TimeoutS: 5},
		Zones: []config.ZoneConfig{{
			ID: "zpage", Name: "寻呼分区", Dial: "5001", Members: []string{"2006", "2007"}, Mode: "unicast",
		}},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ctx, fmt.Sprintf("127.0.0.1:%d", mcsPort)) }()
	defer srv.Close()
	time.Sleep(150 * time.Millisecond)

	startAutoAnswerUAS(t, ctx, "2006", 28006, 28107)
	startAutoAnswerUAS(t, ctx, "2007", 28007, 28108)
	conn1, count1 := listenRTP(t, 28107)
	defer conn1.Close()
	conn2, count2 := listenRTP(t, 28108)
	defer conn2.Close()
	time.Sleep(100 * time.Millisecond)

	uaReg, _ := sipgo.NewUA()
	defer uaReg.Close()
	reg := mustClient(t, uaReg)
	registerForTest(t, reg, mcsPort, "2006", "pass-2006", "sip:2006@127.0.0.1:28006")
	registerForTest(t, reg, mcsPort, "2007", "pass-2007", "sip:2007@127.0.0.1:28007")

	uaA, _ := sipgo.NewUA()
	defer uaA.Close()
	duaA := &sipgo.DialogUA{Client: mustClient(t, uaA), ContactHDR: sip.ContactHeader{Address: sip.Uri{
		Scheme: "sip", User: "2010", Host: "127.0.0.1", Port: 16016,
	}}}
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: "5001", Host: "127.0.0.1", Port: mcsPort})
	req.AppendHeader(sip.NewHeader("From", "<sip:2010@mcs.local>;tag=paging"))
	req.AppendHeader(sip.NewHeader("To", "<sip:5001@mcs.local>"))
	req.AppendHeader(sip.NewHeader("Call-ID", "paging-test"))
	req.AppendHeader(sip.NewHeader("CSeq", "1 INVITE"))
	req.AppendHeader(sip.NewHeader("Contact", "<sip:2010@127.0.0.1:16016>"))
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody(testSDP(28106))

	callCtx, callCancel := context.WithTimeout(ctx, 8*time.Second)
	defer callCancel()
	dcs, err := duaA.WriteInvite(callCtx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(callCtx, sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("paging answer: %v", err)
	}
	if err := dcs.Ack(callCtx); err != nil {
		t.Fatal(err)
	}
	answer := parseSDP(dcs.InviteResponse.Body())
	if answer.AudioPort == 0 {
		t.Fatal("no paging RTP port")
	}

	callerRTP, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 28106})
	if err != nil {
		t.Fatal(err)
	}
	defer callerRTP.Close()
	dst := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: answer.AudioPort}
	for i := 0; i < 50; i++ {
		_, _ = callerRTP.WriteToUDP(rtpPacket(uint16(i), 0xFF), dst)
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if count1.Load() == 0 || count2.Load() == 0 {
		t.Fatalf("speakers received %d / %d packets", count1.Load(), count2.Load())
	}

	if err := dcs.Bye(callCtx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		found := false
		for _, v := range srv.BroadcastSessions(false) {
			if v.ZoneID == "zpage" {
				found = true
				if v.State == "stopped" || v.State == "finished" {
					return
				}
			}
		}
		if !found {
			t.Fatal("paging broadcast session disappeared")
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("paging session did not stop")
}
