package sipserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mcs/internal/config"
	"mcs/internal/store"
)

func TestTLSCalleeOutbound(t *testing.T) {
	const (
		mcsPort    = 25082
		calleePort = 28018
	)
	certPath, keyPath := testTLSCert(t)
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := store.New([]config.DeviceConfig{{Ext: "1002", Name: "tls callee", Password: "pass-1002"}})
	// Simulates a TLS REGISTER from the callee. The contact carries the TLS
	// transport, and REGISTER source transport is also stored as a fallback.
	if !st.Register("1002", "<sip:1002@127.0.0.1:28018;transport=tls>", "127.0.0.1:28018", "tls", "", "", "", time.Minute) {
		t.Fatal("register store")
	}
	srv, err := New(st, NewAuthenticator("mcs", "test"), store.NewCDRStore(10), store.NewAlarmStore(10), Options{
		Domain: "mcs.local", Advertise: fmt.Sprintf("127.0.0.1:%d", mcsPort),
		SIPTLS: "127.0.0.1:25083", DataDir: t.TempDir(),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ctx, fmt.Sprintf("127.0.0.1:%d", mcsPort)) }()
	defer srv.Close()
	time.Sleep(100 * time.Millisecond)

	uaB, _ := sipgo.NewUA()
	defer uaB.Close()
	srvB, _ := sipgo.NewServer(uaB)
	got := make(chan struct{}, 1)
	srvB.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		ch := req.Contact()
		if ch == nil {
			t.Errorf("B-leg INVITE has no Contact")
		} else {
			if ch.Address.Scheme != "sips" {
				t.Errorf("B-leg Contact scheme = %q, want sips", ch.Address.Scheme)
			}
			if ch.Address.Port != 25083 {
				t.Errorf("B-leg Contact port = %d, want 25083", ch.Address.Port)
			}
			if tp, _ := ch.Address.UriParams.Get("transport"); !strings.EqualFold(tp, "tls") {
				t.Errorf("B-leg Contact transport = %q, want tls", tp)
			}
		}
		res := sip.NewResponseFromRequest(req, 200, "OK", testSDP(28119))
		res.AppendHeader(sip.NewHeader("Contact", "<sip:1002@127.0.0.1:28018;transport=tls>"))
		_ = tx.Respond(res)
		select {
		case got <- struct{}{}:
		default:
		}
	})
	srvB.OnAck(func(*sip.Request, sip.ServerTransaction) {})
	srvB.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	go func() {
		_ = srvB.ListenAndServeTLS(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", calleePort), &tls.Config{
			Certificates: []tls.Certificate{cert},
		})
	}()
	time.Sleep(150 * time.Millisecond)

	uaA, _ := sipgo.NewUA()
	defer uaA.Close()
	duaA := &sipgo.DialogUA{Client: mustClient(t, uaA), ContactHDR: sip.ContactHeader{Address: sip.Uri{
		Scheme: "sip", User: "1001", Host: "127.0.0.1", Port: 16018,
	}}}
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: "1002", Host: "127.0.0.1", Port: mcsPort})
	req.AppendHeader(sip.NewHeader("From", "<sip:1001@mcs.local>;tag=tls-out"))
	req.AppendHeader(sip.NewHeader("To", "<sip:1002@mcs.local>"))
	req.AppendHeader(sip.NewHeader("Call-ID", "tls-out-call"))
	req.AppendHeader(sip.NewHeader("CSeq", "1 INVITE"))
	req.AppendHeader(sip.NewHeader("Contact", "<sip:1001@127.0.0.1:16018>"))
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody(testSDP(28110))
	callCtx, callCancel := context.WithTimeout(ctx, 5*time.Second)
	defer callCancel()
	dcs, err := duaA.WriteInvite(callCtx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(callCtx, sipgo.AnswerOptions{}); err != nil {
		t.Fatal(err)
	}
	if dcs.InviteResponse == nil || dcs.InviteResponse.StatusCode != 200 {
		t.Fatalf("answer = %+v", dcs.InviteResponse)
	}
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("TLS callee did not receive INVITE")
	}
	_ = dcs.Bye(callCtx)
}

func testTLSCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath = filepath.Join(dir, "server.crt")
	keyPath = filepath.Join(dir, "server.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
