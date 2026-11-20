package sipserver

import (
	"os"
	"strings"
	"testing"

	opus "github.com/kazzmir/opus-go/opus"
)

func TestParseMP3AndOpusEncode(t *testing.T) {
	data, err := os.ReadFile("testdata/tone.mp3")
	if err != nil {
		t.Fatal(err)
	}
	pcm, rate, format, err := parseAudio(data)
	if err != nil {
		t.Fatal(err)
	}
	if format != "mp3" || rate != 24000 || len(pcm) == 0 {
		t.Fatalf("format=%s rate=%d samples=%d", format, rate, len(pcm))
	}
	if raceDetector {
		t.Skip("opus-go uses unsafe pointer arithmetic that checkptr rejects under -race")
	}
	frames, err := encodeOpusFrames(pcm, rate)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) == 0 || len(frames[0]) == 0 {
		t.Fatalf("opus frames = %d", len(frames))
	}
	dec, err := opus.NewDecoder(48000, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	pcmOut := make([]int16, opusFrameSamples)
	if _, err := dec.Decode(frames[0], pcmOut, opusFrameSamples, false); err != nil {
		t.Fatalf("decode opus frame: %v", err)
	}
}

func TestBroadcastOpusSDPAndMP3Library(t *testing.T) {
	lib := newMediaLibrary(t.TempDir())
	data, err := os.ReadFile("testdata/tone.mp3")
	if err != nil {
		t.Fatal(err)
	}
	item, err := lib.add("tone", data)
	if err != nil {
		t.Fatal(err)
	}
	if item.Format != "mp3" || item.DurationMS <= 0 {
		t.Fatalf("item = %+v", item)
	}
	if _, got, ok := lib.read(item.ID); !ok || len(got) != len(data) {
		t.Fatal("mp3 media read failed")
	}

	conn, err := newRelay()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.close()
	b := &broadcastSession{codec: "opus", conn: conn.conn, server: &Server{advertiseHost: "127.0.0.1"}}
	sdp := string(b.buildSDP())
	if !strings.Contains(sdp, "m=audio") || !strings.Contains(sdp, "opus/48000/2") || !strings.Contains(sdp, "RTP/AVP 111") {
		t.Fatalf("opus sdp:\n%s", sdp)
	}
}
