package sipserver

import (
	"encoding/binary"
	"testing"
)

func TestResampleAndULawFrames(t *testing.T) {
	const srcRate = 24000
	const seconds = 0.1
	samples := int(srcRate * seconds)
	dataSize := samples * 2
	wav := make([]byte, 44+dataSize)
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(36+dataSize))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)
	binary.LittleEndian.PutUint16(wav[20:22], 1)
	binary.LittleEndian.PutUint16(wav[22:24], 1)
	binary.LittleEndian.PutUint32(wav[24:28], srcRate)
	binary.LittleEndian.PutUint32(wav[28:32], srcRate*2)
	binary.LittleEndian.PutUint16(wav[32:34], 2)
	binary.LittleEndian.PutUint16(wav[34:36], 16)
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], uint32(dataSize))

	pcm, rate, err := parseWAV(wav)
	if err != nil {
		t.Fatal(err)
	}
	if rate != srcRate {
		t.Fatalf("rate = %d", rate)
	}
	frames := ulawFrames(pcm, rate)
	if len(frames) != 5 {
		t.Fatalf("frames = %d, want 5", len(frames))
	}
	for _, f := range frames {
		if len(f) != 160 {
			t.Fatalf("frame len = %d", len(f))
		}
	}
}

func TestTranscodeG711RTP(t *testing.T) {
	pkt := make([]byte, 12+160)
	pkt[0] = 0x80
	pkt[1] = 0
	for i := 12; i < len(pkt); i++ {
		pkt[i] = 0xFF
	}
	alaw := transcodeRTP(pkt, "PCMU", "PCMA")
	if alaw[1]&0x7f != 8 {
		t.Fatalf("PCMA pt = %d", alaw[1]&0x7f)
	}
	ulaw := transcodeRTP(alaw, "PCMA", "PCMU")
	if ulaw[1]&0x7f != 0 {
		t.Fatalf("PCMU pt = %d", ulaw[1]&0x7f)
	}
	if len(ulaw) != len(pkt) {
		t.Fatalf("len = %d", len(ulaw))
	}
}

func TestALawRoundTrip(t *testing.T) {
	samples := []int16{-30000, -1000, -1, 0, 1, 1000, 30000}
	enc := encodeALaw(samples)
	for i, b := range enc {
		got := decodeALaw(b)
		want := samples[i]
		diff := int(got) - int(want)
		if diff < 0 {
			diff = -diff
		}
		if diff > 256 {
			t.Fatalf("sample %d: got %d want %d", i, got, want)
		}
	}
}
