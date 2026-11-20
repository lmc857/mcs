package sipserver

import "testing"

func TestRewriteSDPWithVideo(t *testing.T) {
	body := []byte("v=0\r\no=- 1 1 IN IP4 10.0.0.5\r\ns=-\r\nc=IN IP4 10.0.0.5\r\nt=0 0\r\n" +
		"m=audio 10000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n" +
		"m=video 10002 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=fmtp:96 packetization-mode=1\r\n")
	out, err := rewriteSDP(body, 20000, 20002, "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	info := parseSDP(out)
	if info.AudioPort != 20000 || info.VideoPort != 20002 {
		t.Fatalf("ports audio=%d video=%d", info.AudioPort, info.VideoPort)
	}
	if info.ConnIP != "192.0.2.10" {
		t.Fatalf("conn = %s", info.ConnIP)
	}
	if !info.HasVideo || info.VideoCodec != "H264" {
		t.Fatalf("video = %+v", info)
	}
}

func TestRewriteSDPDisablesVideo(t *testing.T) {
	body := []byte("v=0\r\no=- 1 1 IN IP4 10.0.0.5\r\ns=-\r\nc=IN IP4 10.0.0.5\r\nt=0 0\r\n" +
		"m=audio 10000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n" +
		"m=video 10002 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\n")
	out, err := rewriteSDP(body, 20000, 0, "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	info := parseSDP(out)
	if info.AudioPort != 20000 || info.VideoPort != 0 {
		t.Fatalf("ports audio=%d video=%d", info.AudioPort, info.VideoPort)
	}
}
