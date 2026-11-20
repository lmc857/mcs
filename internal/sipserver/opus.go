package sipserver

import (
	"fmt"

	opus "github.com/kazzmir/opus-go/opus"
)

const opusFrameSamples = 960 // 20 ms at 48 kHz

func encodeOpusFrames(pcm []int16, srcRate int) ([][]byte, error) {
	if len(pcm) == 0 || srcRate <= 0 {
		return nil, fmt.Errorf("empty audio")
	}
	pcm = resampleMono(pcm, srcRate, 48000)
	if len(pcm) == 0 {
		return nil, fmt.Errorf("resample produced no audio")
	}
	enc, err := opus.NewEncoder(48000, 1, opus.ApplicationAudio)
	if err != nil {
		return nil, fmt.Errorf("opus encoder: %w", err)
	}
	defer enc.Close()
	_ = enc.SetBitrate(32000)
	_ = enc.SetComplexity(5)

	var frames [][]byte
	buf := make([]byte, 4000)
	for off := 0; off < len(pcm); off += opusFrameSamples {
		frame := make([]int16, opusFrameSamples)
		end := off + opusFrameSamples
		if end > len(pcm) {
			copy(frame, pcm[off:])
		} else {
			copy(frame, pcm[off:end])
		}
		n, err := enc.Encode(frame, opusFrameSamples, buf)
		if err != nil {
			return nil, fmt.Errorf("opus encode: %w", err)
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		frames = append(frames, pkt)
	}
	return frames, nil
}
