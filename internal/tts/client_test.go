package tts

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func tinyWAV() []byte {
	const samples = 800 // 0.1s @ 8kHz
	dataSize := samples * 2
	h := make([]byte, 44)
	copy(h[0:4], "RIFF")
	binary.LittleEndian.PutUint32(h[4:8], uint32(36+dataSize))
	copy(h[8:12], "WAVE")
	copy(h[12:16], "fmt ")
	binary.LittleEndian.PutUint32(h[16:20], 16)
	binary.LittleEndian.PutUint16(h[20:22], 1)
	binary.LittleEndian.PutUint16(h[22:24], 1)
	binary.LittleEndian.PutUint32(h[24:28], 8000)
	binary.LittleEndian.PutUint32(h[28:32], 16000)
	binary.LittleEndian.PutUint16(h[32:34], 2)
	binary.LittleEndian.PutUint16(h[34:36], 16)
	copy(h[36:40], "data")
	binary.LittleEndian.PutUint32(h[40:44], uint32(dataSize))
	return append(h, make([]byte, dataSize)...)
}

func TestSynthesizeVoiceAndCache(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req speechRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if req.ResponseFormat != "wav" {
			t.Errorf("format = %q", req.ResponseFormat)
		}
		if req.Voice == "" {
			t.Errorf("empty voice")
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(tinyWAV())
	}))
	defer srv.Close()

	c := New(srv.URL, "zf_001", "af_heart", 5*time.Second, t.TempDir())
	zh, err := c.Synthesize(context.Background(), "你好，测试广播", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(zh) < 44 {
		t.Fatalf("short wav: %d", len(zh))
	}
	en, err := c.Synthesize(context.Background(), "hello broadcast", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(en) < 44 {
		t.Fatalf("short wav: %d", len(en))
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
	// Second call for the same text must hit the cache.
	if _, err := c.Synthesize(context.Background(), "你好，测试广播", ""); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("cache miss, calls = %d", calls.Load())
	}
}

func TestNonWAVResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("not wave"))
	}))
	defer srv.Close()
	c := New(srv.URL, "zf_001", "af_heart", 5*time.Second, "")
	if _, err := c.Synthesize(context.Background(), "hello", ""); err == nil {
		t.Fatal("expected non-WAV error")
	}
}

func TestOpenAICompatibleAuthAndModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization = %q", got)
		}
		var req speechRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "tts-1" || req.Input != "hello" {
			t.Errorf("request = %+v", req)
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(tinyWAV())
	}))
	defer srv.Close()
	c := NewFromConfig(Config{Provider: "openai", BaseURL: srv.URL + "/v1", APIKey: "secret", Model: "tts-1", VoiceEN: "alloy"}, t.TempDir())
	if _, err := c.Synthesize(context.Background(), "hello", "alloy"); err != nil {
		t.Fatal(err)
	}
}

func TestMiniMaxProvider(t *testing.T) {
	wav := tinyWAV()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/t2a_v2" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer mini" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"audio": hex.EncodeToString(wav)}})
	}))
	defer srv.Close()
	c := NewFromConfig(Config{Provider: "minimax", BaseURL: srv.URL, APIKey: "mini", Model: "speech-01-turbo", VoiceZH: "female-shaonv"}, t.TempDir())
	data, err := c.Synthesize(context.Background(), "你好", "female-shaonv")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(wav) {
		t.Fatal("minimax audio mismatch")
	}
}
