package sipserver

import (
	"math"

	"mcs/internal/config"
)

// conferenceAudioState is the per-participant DSP state. It is protected by
// conferenceParticipant.mu.
type conferenceAudioState struct {
	hpX, hpY float64
	gateGain float64
	agcGain  float64
}

func newConferenceAudioState() conferenceAudioState {
	return conferenceAudioState{gateGain: 1, agcGain: 1}
}

func (p *conferenceParticipant) processPCM(samples []int16, cfg config.ConferenceAudioConfig) {
	if len(samples) == 0 {
		return
	}
	if cfg.Highpass {
		p.highpassPCM(samples)
	}
	if cfg.NoiseGate {
		p.noiseGatePCM(samples, cfg)
	}
	if cfg.AGC {
		p.agcPCM(samples, cfg)
	}
	for i, s := range samples {
		samples[i] = int16(clamp16(int32(s)))
	}
}

func (p *conferenceParticipant) highpassPCM(samples []int16) {
	const (
		sampleRate = 8000.0
		cutoff     = 80.0
	)
	rc := 1.0 / (2 * math.Pi * cutoff)
	dt := 1.0 / sampleRate
	alpha := rc / (rc + dt)
	for i, sample := range samples {
		x := float64(sample)
		y := alpha * (p.audioProc.hpY + x - p.audioProc.hpX)
		p.audioProc.hpX, p.audioProc.hpY = x, y
		samples[i] = int16(clamp16(int32(y)))
	}
}

func (p *conferenceParticipant) noiseGatePCM(samples []int16, cfg config.ConferenceAudioConfig) {
	rms := pcmRMS(samples)
	threshold := float64(cfg.NoiseGateThreshold)
	if threshold <= 0 {
		threshold = 400
	}
	target := 1.0
	if rms < threshold {
		ratio := rms / threshold
		target = ratio * ratio
	}
	// Slow opening, faster closing. This avoids the "pumping" and clicks of a
	// hard gate while still pulling down steady room noise.
	coef := 0.12
	if target < p.audioProc.gateGain {
		coef = 0.35
	}
	p.audioProc.gateGain += (target - p.audioProc.gateGain) * coef
	if p.audioProc.gateGain < 0 {
		p.audioProc.gateGain = 0
	} else if p.audioProc.gateGain > 1 {
		p.audioProc.gateGain = 1
	}
	if p.audioProc.gateGain >= 0.999 {
		return
	}
	for i, sample := range samples {
		samples[i] = int16(clamp16(int32(float64(sample) * p.audioProc.gateGain)))
	}
}

func (p *conferenceParticipant) agcPCM(samples []int16, cfg config.ConferenceAudioConfig) {
	rms := pcmRMS(samples)
	if rms < 80 { // silence: do not pump gain up into noise
		return
	}
	target := float64(cfg.AGCTargetRMS)
	if target <= 0 {
		target = 2200
	}
	maxGain := cfg.AGCMaxGain
	if maxGain < 1 {
		maxGain = 4
	}
	gain := target / rms
	if gain > maxGain {
		gain = maxGain
	}
	if gain < 0.5 {
		gain = 0.5
	}
	coef := 0.08
	if gain < p.audioProc.agcGain {
		coef = 0.25
	}
	p.audioProc.agcGain += (gain - p.audioProc.agcGain) * coef
	if p.audioProc.agcGain < 0.5 {
		p.audioProc.agcGain = 0.5
	} else if p.audioProc.agcGain > maxGain {
		p.audioProc.agcGain = maxGain
	}
	if math.Abs(p.audioProc.agcGain-1) < 0.01 {
		return
	}
	for i, sample := range samples {
		samples[i] = int16(clamp16(int32(float64(sample) * p.audioProc.agcGain)))
	}
}

func pcmRMS(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		v := float64(s)
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(samples)))
}

func clamp16(v int32) int32 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return v
}

// limitSample applies a soft knee above 30000 so a loud multi-party mix is
// compressed rather than hard-clipped.
func limitSample(v int32) int16 {
	const knee = 30000
	if v > knee {
		x := float64(v-knee) / float64(32767-knee)
		return int16(knee + int32(float64(32767-knee)*math.Tanh(x)))
	}
	if v < -knee {
		x := float64(-v-knee) / float64(32768-knee)
		return int16(-knee - int32(float64(32768-knee)*math.Tanh(x)))
	}
	return int16(v)
}
