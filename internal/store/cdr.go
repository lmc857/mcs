package store

import (
	"sync"
	"time"
)

// CDR is one completed call record (M0: in-memory ring).
type CDR struct {
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	From      string `json:"from"`
	To        string `json:"to"`
	DeviceID  string `json:"device_id,omitempty"`
	StartAt   string `json:"start_at"`
	AnswerAt  string `json:"answer_at,omitempty"`
	EndAt     string `json:"end_at,omitempty"`
	Duration  int    `json:"duration_s"`
	Result    string `json:"result"`
	Recording string `json:"recording,omitempty"`
}

type CDRStore struct {
	mu    sync.RWMutex
	items []CDR
	max   int
	db    *DB
}

func NewCDRStore(max int) *CDRStore {
	if max <= 0 {
		max = 2000
	}
	return &CDRStore{max: max}
}

func NewCDRStoreDB(max int, db *DB) *CDRStore {
	s := NewCDRStore(max)
	if db != nil {
		s.db = db
		s.items = db.LoadCDR(max)
	}
	return s
}

func (s *CDRStore) Add(c CDR) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, c)
	if len(s.items) > s.max {
		s.items = s.items[len(s.items)-s.max:]
	}
	if s.db != nil {
		_ = s.db.SaveCDR(c)
	}
}

// List returns records newest first.
func (s *CDRStore) List(limit int) []CDR {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.items) {
		limit = len(s.items)
	}
	out := make([]CDR, 0, limit)
	for i := len(s.items) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.items[i])
	}
	return out
}

func Now() string { return time.Now().Format(time.RFC3339) }
