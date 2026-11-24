package sipserver

import (
	"math"
	"testing"

	"mcs/internal/config"
)

func TestConferenceHighpass(t *testing.T) {
	p := &conferenceParticipant{audioProc: newConferenceAudioState()}
	samples := make([]int16, 800)
	for i := range samples {
		samples[i] = 1000
	}
	p.processPCM(samples, config.ConferenceAudioConfig{Highpass: true})
	if rms := pcmRMS(samples[400:]); rms > 50 {
		t.Fatalf("highpass residual RMS = %.1f", rms)
	}
}

func TestConferenceNoiseGate(t *testing.T) {
	p := &conferenceParticipant{audioProc: newConferenceAudioState()}
	cfg := config.ConferenceAudioConfig{NoiseGate: true, NoiseGateThreshold: 400}
	for i := 0; i < 40; i++ {
		s := make([]int16, conferenceFrameSamples)
		for j := range s {
			s[i%len(s)] = 0 // keep deterministic
			s[j] = 100
		}
		p.processPCM(s, cfg)
		if i == 39 {
			if rms := pcmRMS(s); rms > 40 {
				t.Fatalf("gate output RMS = %.1f", rms)
			}
		}
	}
}

func TestConferenceAGC(t *testing.T) {
	p := &conferenceParticipant{audioProc: newConferenceAudioState()}
	cfg := config.ConferenceAudioConfig{AGC: true, AGCTargetRMS: 2200, AGCMaxGain: 4}
	var last []int16
	for i := 0; i < 100; i++ {
		last = make([]int16, conferenceFrameSamples)
		for j := range last {
			last[j] = 500
		}
		p.processPCM(last, cfg)
	}
	if rms := pcmRMS(last); rms < 700 {
		t.Fatalf("AGC output RMS = %.1f, want > 700", rms)
	}
}

func TestConferenceLimiter(t *testing.T) {
	out := limitSample(32767)
	if out <= 30000 || out >= 32767 {
		t.Fatalf("limited sample = %d", out)
	}
	if math.Abs(float64(limitSample(-32768))) > 32768 {
		t.Fatal("negative limiter overflow")
	}
}
