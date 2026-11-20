package sipserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// Schedule is a recurring TTS/media broadcast.
type Schedule struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ZoneID    string `json:"zone_id"`
	Text      string `json:"text,omitempty"`
	Voice     string `json:"voice,omitempty"`
	MediaID   string `json:"media_id,omitempty"`
	Priority  int    `json:"priority"`
	Cron      string `json:"cron"`
	Enabled   bool   `json:"enabled"`
	LastRun   string `json:"last_run,omitempty"`
	NextRun   string `json:"next_run,omitempty"`
	CreatedAt string `json:"created_at"`
}

type scheduleStore struct {
	mu    sync.Mutex
	path  string
	items []Schedule
}

func newScheduleStore(dataDir string) *scheduleStore {
	dir := filepath.Join(dataDir, "broadcast")
	_ = os.MkdirAll(dir, 0o755)
	s := &scheduleStore{path: filepath.Join(dir, "schedules.json")}
	s.load()
	return s
}

func (s *scheduleStore) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(raw, &s.items)
}

func nextRun(spec string, from time.Time) (time.Time, error) {
	parsed, err := cron.ParseStandard(spec)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron %q: %w", spec, err)
	}
	return parsed.Next(from), nil
}

func (s *scheduleStore) list() []Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Schedule(nil), s.items...)
	return out
}

func (s *scheduleStore) add(sch Schedule) (Schedule, error) {
	sch.Cron = strings.TrimSpace(sch.Cron)
	if sch.Cron == "" {
		return Schedule{}, fmt.Errorf("cron is required")
	}
	if sch.Text == "" && sch.MediaID == "" {
		return Schedule{}, fmt.Errorf("text or media_id is required")
	}
	next, err := nextRun(sch.Cron, time.Now())
	if err != nil {
		return Schedule{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sch.ID = fmt.Sprintf("sch-%d", time.Now().UnixNano())
	sch.CreatedAt = time.Now().Format(time.RFC3339)
	sch.NextRun = next.Format(time.RFC3339)
	s.items = append(s.items, sch)
	if err := s.save(); err != nil {
		s.items = s.items[:len(s.items)-1]
		return Schedule{}, err
	}
	return sch, nil
}

func (s *scheduleStore) update(id string, fn func(*Schedule)) (Schedule, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID != id {
			continue
		}
		old := s.items[i]
		fn(&s.items[i])
		next, err := nextRun(s.items[i].Cron, time.Now())
		if err != nil {
			s.items[i] = old
			return Schedule{}, true, err
		}
		s.items[i].NextRun = next.Format(time.RFC3339)
		if err := s.save(); err != nil {
			s.items[i] = old
			return Schedule{}, true, err
		}
		return s.items[i], true, nil
	}
	return Schedule{}, false, nil
}

func (s *scheduleStore) remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID != id {
			continue
		}
		s.items = append(s.items[:i], s.items[i+1:]...)
		_ = s.save()
		return true
	}
	return false
}

// save must be called with mu held.
func (s *scheduleStore) save() error {
	raw, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// due advances and returns schedules whose NextRun is at or before now.
func (s *scheduleStore) due(now time.Time) []Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Schedule
	for i := range s.items {
		sch := &s.items[i]
		if !sch.Enabled || sch.NextRun == "" {
			continue
		}
		next, err := time.Parse(time.RFC3339, sch.NextRun)
		if err != nil || next.After(now) {
			continue
		}
		out = append(out, *sch)
		sch.LastRun = now.Format(time.RFC3339)
		n, err := nextRun(sch.Cron, now.Add(time.Second))
		if err != nil {
			sch.Enabled = false
			continue
		}
		sch.NextRun = n.Format(time.RFC3339)
	}
	if len(out) > 0 {
		_ = s.save()
	}
	return out
}

// SchedulePatch is a partial update from the API.
type SchedulePatch struct {
	Name     *string `json:"name"`
	Text     *string `json:"text"`
	Voice    *string `json:"voice"`
	MediaID  *string `json:"media_id"`
	Priority *int    `json:"priority"`
	Cron     *string `json:"cron"`
	Enabled  *bool   `json:"enabled"`
}

func (s *Server) ListSchedules() []Schedule { return s.schedules.list() }

func (s *Server) AddSchedule(sch Schedule) (Schedule, error) {
	if _, ok := s.findZone(sch.ZoneID); !ok {
		return Schedule{}, fmt.Errorf("zone %q not found", sch.ZoneID)
	}
	if sch.Priority < 0 || sch.Priority > 4 {
		sch.Priority = 3
	}
	if sch.Name == "" {
		sch.Name = "定时广播"
	}
	return s.schedules.add(sch)
}

func (s *Server) UpdateSchedule(id string, p SchedulePatch) (Schedule, bool, error) {
	return s.schedules.update(id, func(sch *Schedule) {
		if p.Name != nil {
			sch.Name = *p.Name
		}
		if p.Text != nil {
			sch.Text = *p.Text
		}
		if p.Voice != nil {
			sch.Voice = *p.Voice
		}
		if p.MediaID != nil {
			sch.MediaID = *p.MediaID
		}
		if p.Priority != nil {
			sch.Priority = *p.Priority
		}
		if p.Cron != nil {
			sch.Cron = *p.Cron
		}
		if p.Enabled != nil {
			sch.Enabled = *p.Enabled
		}
	})
}

func (s *Server) DeleteSchedule(id string) bool { return s.schedules.remove(id) }

// StartScheduler runs due schedules in the background.
func (s *Server) StartScheduler(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		s.RunDueSchedules(time.Now())
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				s.RunDueSchedules(now)
			}
		}
	}()
}

// RunDueSchedules triggers schedules that are due; exposed for tests.
func (s *Server) RunDueSchedules(now time.Time) int {
	due := s.schedules.due(now)
	for _, sch := range due {
		go func(sch Schedule) {
			view, err := s.StartBroadcast(context.Background(), BroadcastRequest{
				ZoneID: sch.ZoneID, Text: sch.Text, Voice: sch.Voice, MediaID: sch.MediaID, Priority: sch.Priority,
			})
			if err != nil {
				s.log.Warn("scheduled broadcast failed", "schedule", sch.ID, "name", sch.Name, "error", err)
				return
			}
			s.log.Info("scheduled broadcast started", "schedule", sch.ID, "broadcast", view.ID, "zone", sch.ZoneID)
		}(sch)
	}
	return len(due)
}
