package sipserver

import "testing"

func TestSRTPPolicy(t *testing.T) {
	plain := sdpInfo{HasAudio: true, AudioPort: 10000}
	secure := sdpInfo{HasAudio: true, AudioPort: 10000, AudioSecure: true}
	secure.AudioCrypto = cryptoInfo{OK: true, Suite: "AES_CM_128_HMAC_SHA1_80"}

	req := &Server{srtpMode: "required"}
	if err := req.checkSRTPPolicy(plain); err == nil {
		t.Fatal("required mode accepted plain RTP")
	}
	if err := req.checkSRTPPolicy(secure); err != nil {
		t.Fatalf("required mode rejected SRTP: %v", err)
	}

	off := &Server{srtpMode: "off"}
	if err := off.checkSRTPPolicy(secure); err == nil {
		t.Fatal("off mode accepted SRTP")
	}
	if err := off.checkSRTPPolicy(plain); err != nil {
		t.Fatalf("off mode rejected plain RTP: %v", err)
	}

	auto := &Server{srtpMode: "auto"}
	if err := auto.checkSRTPPolicy(plain); err != nil {
		t.Fatalf("auto rejected plain RTP: %v", err)
	}
	if err := auto.checkSRTPPolicy(secure); err != nil {
		t.Fatalf("auto rejected SRTP: %v", err)
	}
}
