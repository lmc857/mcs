package sipserver

import (
	"net"
	"testing"
	"time"
)

func freeUDP(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRelayH264PTAndRTCPPort(t *testing.T) {
	src := freeUDP(t)
	defer src.Close()
	rtpDst := freeUDP(t)
	defer rtpDst.Close()
	rtcpDst := freeUDP(t)
	defer rtcpDst.Close()

	r, err := newRelay()
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	dst, err := newRelay()
	if err != nil {
		t.Fatal(err)
	}
	defer dst.close()
	dst.setPeerRTCP("127.0.0.1", rtpDst.LocalAddr().(*net.UDPAddr).Port, rtcpDst.LocalAddr().(*net.UDPAddr).Port, false)
	r.setOutPT(97)
	r.start(dst, nil)

	// A video RTP packet with PT 96 must arrive at the RTP port as PT 97.
	rtpPkt := []byte{0x80, 96, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 0xaa, 0xbb}
	relayAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: r.conn.LocalAddr().(*net.UDPAddr).Port}
	if _, err := src.WriteToUDP(rtpPkt, relayAddr); err != nil {
		t.Fatal(err)
	}
	_ = rtpDst.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, _, err := rtpDst.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("rtp: %v", err)
	}
	if n < 2 || buf[1]&0x7f != 97 {
		t.Fatalf("forwarded PT = %d, want 97", buf[1]&0x7f)
	}

	// RTCP must go to the RTCP port, not the RTP port.
	rtcpPkt := []byte{0x81, 206, 0, 1, 0, 0, 0, 1, 0, 0, 0, 0} // PLI-like packet
	if _, err := src.WriteToUDP(rtcpPkt, relayAddr); err != nil {
		t.Fatal(err)
	}
	_ = rtcpDst.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := rtcpDst.ReadFromUDP(buf); err != nil {
		t.Fatalf("rtcp: %v", err)
	}

	// An RTCP packet from the source must not make subsequent RTP go to the
	// source's RTCP port. Send another RTP and verify it still reaches RTP.
	rtpPkt[13] = 0xcc
	if _, err := src.WriteToUDP(rtpPkt, relayAddr); err != nil {
		t.Fatal(err)
	}
	_ = rtpDst.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err = rtpDst.ReadFromUDP(buf)
	if err != nil || n < 2 || buf[1]&0x7f != 97 {
		t.Fatalf("second rtp: n=%d err=%v pt=%d", n, err, buf[1]&0x7f)
	}
}

func TestParseSDPRTCP(t *testing.T) {
	info := parseSDP([]byte("v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 4000 RTP/AVP 0\r\na=rtcp:4001\r\na=rtcp-mux\r\nm=video 5000 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=rtcp:5001 IN IP4 127.0.0.1\r\n"))
	if info.AudioRTCPPort != 4001 || !info.AudioRTCPMux || info.VideoRTCPPort != 5001 || info.VideoPT != 96 {
		t.Fatalf("rtcp info: %+v", info)
	}
}
