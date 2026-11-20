package store

import (
	"sync"
	"time"
)

// Alarm is one alarm event tied to a call (M0: in-memory).
type Alarm struct {
	ID           string `json:"id"`
	CallID       string `json:"call_id"`
	From         string `json:"from"`
	To           string `json:"to"`
	AlarmNumber  string `json:"alarm_number"`
	DeviceName   string `json:"device_name"`
	Zone         string `json:"zone"`
	State        string `json:"state"` // ringing, answered, pending, closed, unanswered
	Level        int    `json:"level"`
	LevelName    string `json:"level_name"`
	AnsweredBy   string `json:"answered_by,omitempty"`
	StartedAt    string `json:"started_at"`
	AnsweredAt   string `json:"answered_at,omitempty"`
	EndedAt      string `json:"ended_at,omitempty"`
	ClosedAt     string `json:"closed_at,omitempty"`
	Disposition  string `json:"disposition,omitempty"`
	Note         string `json:"note,omitempty"`
	Recording    string `json:"recording,omitempty"`
	Test         bool   `json:"test,omitempty"`
	LastUpdateAt string `json:"last_update_at,omitempty"`
}

type AlarmStore struct {
	mu    sync.RWMutex
	items map[string]*Alarm
	order []string
	max   int
	db    *DB
}

func NewAlarmStore(max int) *AlarmStore {
	if max <= 0 {
		max = 1000
	}
	return &AlarmStore{items: map[string]*Alarm{}, max: max}
}

func NewAlarmStoreDB(max int, db *DB) *AlarmStore {
	s := NewAlarmStore(max)
	if db == nil {
		return s
	}
	s.db = db
	for _, a := range db.LoadAlarms(max) {
		cp := a
		s.items[a.ID] = &cp
		s.order = append(s.order, a.ID)
	}
	return s
}

func copyAlarm(a *Alarm) Alarm { return *a }

func (s *AlarmStore) Create(a Alarm) Alarm {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.ID == "" {
		a.ID = "alm-" + time.Now().Format("20060102150405.000000")
	}
	if a.StartedAt == "" {
		a.StartedAt = time.Now().Format(time.RFC3339)
	}
	if a.State == "" {
		a.State = "ringing"
	}
	a.LastUpdateAt = time.Now().Format(time.RFC3339)
	cp := a
	s.items[a.ID] = &cp
	s.order = append(s.order, a.ID)
	if s.db != nil {
		_ = s.db.SaveAlarm(cp)
	}
	if len(s.order) > s.max {
		old := s.order[0]
		s.order = s.order[1:]
		delete(s.items, old)
	}
	return cp
}

func (s *AlarmStore) Update(id string, fn func(*Alarm)) (Alarm, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.items[id]
	if !ok {
		return Alarm{}, false
	}
	fn(a)
	a.LastUpdateAt = time.Now().Format(time.RFC3339)
	if s.db != nil {
		_ = s.db.SaveAlarm(*a)
	}
	return copyAlarm(a), true
}

func (s *AlarmStore) Get(id string) (Alarm, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.items[id]
	if !ok {
		return Alarm{}, false
	}
	return copyAlarm(a), true
}

// List returns newest first.
func (s *AlarmStore) List(limit int) []Alarm {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.order) {
		limit = len(s.order)
	}
	out := make([]Alarm, 0, limit)
	for i := len(s.order) - 1; i >= 0 && len(out) < limit; i-- {
		if a, ok := s.items[s.order[i]]; ok {
			out = append(out, copyAlarm(a))
		}
	}
	return out
}
