package tts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Config selects an OpenAI-compatible or MiniMax TTS endpoint.
type Config struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
	VoiceZH  string
	VoiceEN  string
	Timeout  time.Duration
}

// Client calls a TTS HTTP API and caches the returned audio.
type Client struct {
	provider string
	baseURL  string
	apiKey   string
	model    string
	voiceZH  string
	voiceEN  string
	http     *http.Client
	cacheDir string
}

func New(baseURL, voiceZH, voiceEN string, timeout time.Duration, cacheDir string) *Client {
	return NewFromConfig(Config{
		Provider: "openai", BaseURL: baseURL, VoiceZH: voiceZH, VoiceEN: voiceEN,
		Timeout: timeout,
	}, cacheDir)
}

func NewFromConfig(cfg Config, cacheDir string) *Client {
	if cfg.Provider == "" {
		cfg.Provider = "openai"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://127.0.0.1:8399"
	}
	return &Client{
		provider: strings.ToLower(cfg.Provider),
		baseURL:  strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:   cfg.APIKey,
		model:    cfg.Model,
		voiceZH:  cfg.VoiceZH,
		voiceEN:  cfg.VoiceEN,
		http:     &http.Client{Timeout: cfg.Timeout},
		cacheDir: cacheDir,
	}
}

// VoiceFor picks the Chinese or English voice by a simple CJK check.
func (c *Client) VoiceFor(text string) string {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			return c.voiceZH
		}
	}
	return c.voiceEN
}

type speechRequest struct {
	Model          string `json:"model,omitempty"`
	Input          string `json:"input"`
	Voice          string `json:"voice"`
	ResponseFormat string `json:"response_format"`
}

func isWAV(data []byte) bool {
	return len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WAVE"
}

func isMP3(data []byte) bool {
	return len(data) >= 3 && (string(data[0:3]) == "ID3" || (data[0] == 0xFF && data[1]&0xE0 == 0xE0))
}

// Synthesize returns WAV/MP3 bytes for text, using cache when available.
func (c *Client) Synthesize(ctx context.Context, text, voice string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("tts: empty text")
	}
	if voice == "" {
		voice = c.VoiceFor(text)
	}
	cacheKey := sha256.Sum256([]byte(c.provider + "\x00" + c.model + "\x00" + voice + "\x00" + text))
	name := hex.EncodeToString(cacheKey[:]) + ".wav"
	if c.cacheDir != "" {
		if data, err := os.ReadFile(filepath.Join(c.cacheDir, name)); err == nil && (isWAV(data) || isMP3(data)) {
			return data, nil
		}
	}

	var (
		data []byte
		err  error
	)
	if c.provider == "minimax" {
		data, err = c.synthesizeMiniMax(ctx, text, voice)
	} else {
		data, err = c.synthesizeOpenAI(ctx, text, voice)
	}
	if err != nil {
		return nil, err
	}
	if !isWAV(data) && !isMP3(data) {
		return nil, fmt.Errorf("tts returned non-audio")
	}
	if c.cacheDir != "" {
		if err := os.MkdirAll(c.cacheDir, 0o755); err == nil {
			_ = os.WriteFile(filepath.Join(c.cacheDir, name), data, 0o644)
		}
	}
	return data, nil
}

func (c *Client) speechURL() string {
	u := strings.TrimRight(c.baseURL, "/")
	if strings.HasSuffix(u, "/v1") {
		return u + "/audio/speech"
	}
	return u + "/v1/audio/speech"
}

func (c *Client) synthesizeOpenAI(ctx context.Context, text, voice string) ([]byte, error) {
	body, _ := json.Marshal(speechRequest{
		Model: c.model, Input: text, Voice: voice, ResponseFormat: "wav",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.speechURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tts request: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("tts status %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

type miniMaxRequest struct {
	Model        string         `json:"model"`
	Text         string         `json:"text"`
	Stream       bool           `json:"stream"`
	VoiceSetting map[string]any `json:"voice_setting"`
	AudioSetting map[string]any `json:"audio_setting"`
}

type miniMaxResponse struct {
	Data struct {
		Audio string `json:"audio"`
	} `json:"data"`
	BaseResp struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

func (c *Client) synthesizeMiniMax(ctx context.Context, text, voice string) ([]byte, error) {
	model := c.model
	if model == "" {
		model = "speech-01-turbo"
	}
	u := c.baseURL
	if !strings.Contains(u, "t2a_v2") {
		u += "/v1/t2a_v2"
	}
	body, _ := json.Marshal(miniMaxRequest{
		Model: model, Text: text, Stream: false,
		VoiceSetting: map[string]any{"voice_id": voice, "speed": 1, "vol": 1, "pitch": 0},
		AudioSetting: map[string]any{"sample_rate": 32000, "bitrate": 128000, "format": "mp3", "channel": 1},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("minimax request: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("minimax status %d: %s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out miniMaxResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("minimax response: %w", err)
	}
	if out.Data.Audio == "" {
		return nil, fmt.Errorf("minimax: empty audio (status=%d %s)", out.BaseResp.StatusCode, out.BaseResp.StatusMsg)
	}
	if data, err := hex.DecodeString(out.Data.Audio); err == nil {
		return data, nil
	}
	data, err := base64.StdEncoding.DecodeString(out.Data.Audio)
	if err != nil {
		return nil, fmt.Errorf("minimax audio decode: %w", err)
	}
	return data, nil
}
