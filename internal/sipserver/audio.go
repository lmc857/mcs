package sipserver

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/hajimehoshi/go-mp3"
)

// parseWAV decodes PCM16 WAV to mono samples.
func parseWAV(data []byte) ([]int16, int, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("not a WAV file")
	}
	var (
		audioFormat   uint16
		channels      uint16
		sampleRate    int
		bitsPerSample uint16
		pcm           []byte
	)
	for off := 12; off+8 <= len(data); {
		id := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		body := off + 8
		if size < 0 || body+size > len(data) {
			break
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, 0, fmt.Errorf("short fmt chunk")
			}
			audioFormat = binary.LittleEndian.Uint16(data[body : body+2])
			channels = binary.LittleEndian.Uint16(data[body+2 : body+4])
			sampleRate = int(binary.LittleEndian.Uint32(data[body+4 : body+8]))
			bitsPerSample = binary.LittleEndian.Uint16(data[body+14 : body+16])
		case "data":
			pcm = data[body : body+size]
		}
		off = body + size
		if size%2 == 1 {
			off++
		}
	}
	if audioFormat != 1 || bitsPerSample != 16 || channels == 0 || sampleRate <= 0 {
		return nil, 0, fmt.Errorf("unsupported WAV: format=%d bits=%d channels=%d", audioFormat, bitsPerSample, channels)
	}
	frames := len(pcm) / (int(channels) * 2)
	out := make([]int16, frames)
	for i := 0; i < frames; i++ {
		var sum int32
		for ch := 0; ch < int(channels); ch++ {
			off := (i*int(channels) + ch) * 2
			sum += int32(int16(binary.LittleEndian.Uint16(pcm[off : off+2])))
		}
		out[i] = int16(sum / int32(channels))
	}
	return out, sampleRate, nil
}

// parseAudio accepts PCM16 WAV or MPEG audio (MP3) and returns mono PCM.
func parseAudio(data []byte) ([]int16, int, string, error) {
	if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WAVE" {
		pcm, rate, err := parseWAV(data)
		return pcm, rate, "wav", err
	}
	if len(data) >= 3 && (string(data[0:3]) == "ID3" || (data[0] == 0xFF && data[1]&0xE0 == 0xE0)) {
		pcm, rate, err := parseMP3(data)
		return pcm, rate, "mp3", err
	}
	return nil, 0, "", fmt.Errorf("unsupported audio: expected WAV or MP3")
}

func parseMP3(data []byte) ([]int16, int, error) {
	dec, err := mp3.NewDecoder(bytes.NewReader(data))
	if err != nil {
		return nil, 0, fmt.Errorf("mp3: %w", err)
	}
	raw, err := io.ReadAll(dec)
	if err != nil {
		return nil, 0, fmt.Errorf("mp3 decode: %w", err)
	}
	if len(raw)%4 != 0 {
		raw = raw[:len(raw)-len(raw)%4]
	}
	out := make([]int16, len(raw)/4)
	for i := range out {
		l := int16(binary.LittleEndian.Uint16(raw[i*4 : i*4+2]))
		r := int16(binary.LittleEndian.Uint16(raw[i*4+2 : i*4+4]))
		out[i] = int16((int32(l) + int32(r)) / 2)
	}
	return out, dec.SampleRate(), nil
}

// resampleMono does linear interpolation to the target sample rate.
func resampleMono(in []int16, src, dst int) []int16 {
	if src == dst || len(in) == 0 {
		return in
	}
	ratio := float64(src) / float64(dst)
	outLen := int(float64(len(in)) / ratio)
	if outLen < 1 {
		return nil
	}
	out := make([]int16, outLen)
	for i := 0; i < outLen; i++ {
		pos := float64(i) * ratio
		j := int(pos)
		if j >= len(in)-1 {
			out[i] = in[len(in)-1]
			continue
		}
		frac := pos - float64(j)
		v := float64(in[j])*(1-frac) + float64(in[j+1])*frac
		out[i] = int16(v)
	}
	return out
}

func encodeULaw(samples []int16) []byte {
	const (
		BIAS = 0x84
		CLIP = 32635
	)
	out := make([]byte, len(samples))
	for i, s := range samples {
		sign := (s >> 8) & 0x80
		v := int(s)
		if v < 0 {
			v = -v
		}
		if v > CLIP {
			v = CLIP
		}
		v += BIAS
		exponent := 7
		for mask := 0x4000; exponent > 0 && v&mask == 0; mask >>= 1 {
			exponent--
		}
		mantissa := (v >> (exponent + 3)) & 0x0F
		u := byte(sign) | byte(exponent<<4) | byte(mantissa)
		out[i] = ^u
	}
	return out
}

func encodeALaw(samples []int16) []byte {
	out := make([]byte, len(samples))
	for i, sample := range samples {
		s := int(sample)
		sign := 0x00
		if s >= 0 {
			sign = 0x80
		} else {
			s = -s - 1
		}
		if s > 32767 {
			s = 32767
		}
		exponent := 0
		for v := s >> 8; v > 0; v >>= 1 {
			exponent++
		}
		if exponent > 7 {
			exponent = 7
		}
		var mantissa int
		if exponent == 0 {
			mantissa = (s >> 4) & 0x0F
		} else {
			mantissa = (s >> (exponent + 3)) & 0x0F
		}
		out[i] = byte(sign|(exponent<<4)|int(mantissa)) ^ 0x55
	}
	return out
}

// ulawFrames resamples to 8k and returns 20ms G.711 frames (160 bytes).
func ulawFrames(pcm []int16, sampleRate int) [][]byte {
	pcm = resampleMono(pcm, sampleRate, 8000)
	encoded := encodeULaw(pcm)
	var frames [][]byte
	for off := 0; off < len(encoded); off += 160 {
		end := off + 160
		if end > len(encoded) {
			end = len(encoded)
		}
		frame := make([]byte, 160)
		copy(frame, encoded[off:end])
		frames = append(frames, frame)
	}
	return frames
}
