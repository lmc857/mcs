package sipserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// MediaItem is an uploaded WAV in the broadcast audio library.
type MediaItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Format     string `json:"format,omitempty"`
	Size       int64  `json:"size"`
	DurationMS int64  `json:"duration_ms"`
	CreatedAt  string `json:"created_at"`
}

type mediaLibrary struct {
	mu      sync.RWMutex
	dir     string
	index   string
	items   []MediaItem
	loadErr error
}

func newMediaLibrary(dataDir string) *mediaLibrary {
	dir := filepath.Join(dataDir, "audio", "library")
	m := &mediaLibrary{dir: dir, index: filepath.Join(dir, "index.json")}
	_ = os.MkdirAll(dir, 0o755)
	m.load()
	return m
}

func (m *mediaLibrary) load() {
	raw, err := os.ReadFile(m.index)
	if err != nil {
		if !os.IsNotExist(err) {
			m.loadErr = err
		}
		return
	}
	var items []MediaItem
	if err := json.Unmarshal(raw, &items); err != nil {
		m.loadErr = err
		return
	}
	for _, it := range items {
		if it.Format == "" {
			it.Format = "wav"
		}
		if _, err := os.Stat(m.path(it)); err == nil {
			m.items = append(m.items, it)
		}
	}
}

func (m *mediaLibrary) saveLocked() error {
	raw, err := json.MarshalIndent(m.items, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.index + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.index)
}

func (m *mediaLibrary) path(it MediaItem) string {
	ext := it.Format
	if ext == "" {
		ext = "wav"
	}
	return filepath.Join(m.dir, it.ID+"."+ext)
}

func (m *mediaLibrary) add(name string, data []byte) (MediaItem, error) {
	pcm, rate, format, err := parseAudio(data)
	if err != nil {
		return MediaItem{}, fmt.Errorf("only WAV/MP3 is supported: %w", err)
	}
	if len(pcm) == 0 || rate <= 0 {
		return MediaItem{}, fmt.Errorf("empty audio")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "未命名音频"
	}
	if r := []rune(name); len(r) > 80 {
		name = string(r[:80])
	}
	it := MediaItem{
		ID:         fmt.Sprintf("med-%d", time.Now().UnixNano()),
		Name:       name,
		Format:     format,
		Size:       int64(len(data)),
		DurationMS: int64(len(pcm)) * 1000 / int64(rate),
		CreatedAt:  time.Now().Format(time.RFC3339),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.WriteFile(m.path(it), data, 0o644); err != nil {
		return MediaItem{}, err
	}
	m.items = append(m.items, it)
	if err := m.saveLocked(); err != nil {
		_ = os.Remove(m.path(it))
		m.items = m.items[:len(m.items)-1]
		return MediaItem{}, err
	}
	return it, nil
}

func (m *mediaLibrary) list() []MediaItem {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := append([]MediaItem(nil), m.items...)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

func (m *mediaLibrary) get(id string) (MediaItem, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, it := range m.items {
		if it.ID == id {
			return it, true
		}
	}
	return MediaItem{}, false
}

func (m *mediaLibrary) read(id string) (MediaItem, []byte, bool) {
	it, ok := m.get(id)
	if !ok {
		return MediaItem{}, nil, false
	}
	data, err := os.ReadFile(m.path(it))
	if err != nil {
		return MediaItem{}, nil, false
	}
	return it, data, true
}

func (m *mediaLibrary) delete(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, it := range m.items {
		if it.ID != id {
			continue
		}
		_ = os.Remove(m.path(it))
		m.items = append(m.items[:i], m.items[i+1:]...)
		_ = m.saveLocked()
		return true
	}
	return false
}

// AddMedia validates and stores a WAV or MP3 in the broadcast library.
func (s *Server) AddMedia(name string, data []byte) (MediaItem, error) {
	return s.media.add(name, data)
}

func (s *Server) ListMedia() []MediaItem {
	return s.media.list()
}

func (s *Server) DeleteMedia(id string) bool {
	return s.media.delete(id)
}

func (s *Server) MediaData(id string) (MediaItem, []byte, bool) {
	return s.media.read(id)
}
