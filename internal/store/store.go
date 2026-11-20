package store

import (
	"crypto/subtle"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"mcs/internal/config"
)

// Device is the in-memory registration record for M0.
// Persistence (SQLite) and password hashing land in M1.
type Device struct {
	Ext       string
	Name      string
	Zone      string
	Password  string
	Enabled   bool
	Contact   string
	Source    string
	UserAgent string
	Model     string
	Firmware  string
	Snapshot  string
	ExpiresAt time.Time
	LastSeen  time.Time
}

// View is the API-facing projection of a device.
type View struct {
	Ext       string `json:"ext"`
	Name      string `json:"name"`
	Zone      string `json:"zone"`
	Online    bool   `json:"online"`
	Contact   string `json:"contact"`
	Source    string `json:"source"`
	UserAgent string `json:"user_agent"`
	Model     string `json:"model"`
	Firmware  string `json:"firmware"`
	Snapshot  string `json:"snapshot,omitempty"`
	Enabled   bool   `json:"enabled"`
	ExpiresAt string `json:"expires_at,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
}

type Store struct {
	mu      sync.RWMutex
	devices map[string]*Device
	order   []string
	db      *DB
}

func New(devices []config.DeviceConfig) *Store {
	s := &Store{devices: make(map[string]*Device, len(devices))}
	for _, d := range devices {
		s.putLocked(Device{
			Ext: d.Ext, Name: d.Name, Zone: d.Zone, Password: d.Password,
			Snapshot: d.Snapshot, Enabled: true,
		})
	}
	s.sortLocked()
	return s
}

// NewWithDB loads devices from SQLite. On an empty DB the config file is
// imported once; after that the database is the source of truth.
func NewWithDB(devices []config.DeviceConfig, db *DB) *Store {
	s := &Store{devices: make(map[string]*Device), db: db}
	rows := db.LoadDevices()
	if len(rows) > 0 {
		for _, d := range rows {
			s.putLocked(d)
		}
	} else {
		for _, d := range devices {
			dev := Device{
				Ext: d.Ext, Name: d.Name, Zone: d.Zone, Password: d.Password,
				Snapshot: d.Snapshot, Enabled: true,
			}
			s.putLocked(dev)
			_ = db.SaveDevice(dev)
		}
	}
	s.sortLocked()
	return s
}

func (s *Store) putLocked(d Device) {
	s.devices[d.Ext] = &d
	found := false
	for _, ext := range s.order {
		if ext == d.Ext {
			found = true
			break
		}
	}
	if !found {
		s.order = append(s.order, d.Ext)
	}
}

func (s *Store) sortLocked() { sort.Strings(s.order) }

// UpsertDevice creates or updates a persisted device.
func (s *Store) UpsertDevice(ext, name, password, zone, snapshot string) error {
	ext = strings.TrimSpace(ext)
	if ext == "" || name == "" {
		return fmt.Errorf("ext and name are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[ext]
	if !ok {
		if password == "" {
			return fmt.Errorf("password is required for a new device")
		}
		d = &Device{Ext: ext, Enabled: true}
	}
	d.Name = name
	d.Zone = zone
	d.Snapshot = snapshot
	if password != "" {
		d.Password = password
	}
	if !ok {
		s.putLocked(*d)
		s.sortLocked()
	}
	if s.db != nil {
		if err := s.db.SaveDevice(*d); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteDevice(ext string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.devices[ext]; !ok {
		return false
	}
	delete(s.devices, ext)
	for i, e := range s.order {
		if e == ext {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	if s.db != nil {
		_ = s.db.DeleteDevice(ext)
	}
	return true
}

// Authenticate compares the configured device password in constant time.
func (s *Store) Authenticate(ext, password string) bool {
	s.mu.RLock()
	d, ok := s.devices[ext]
	s.mu.RUnlock()
	if !ok || !d.Enabled {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(d.Password), []byte(password)) == 1
}

// Password returns the configured password for digest verification.
func (s *Store) Password(ext string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[ext]
	if !ok || !d.Enabled {
		return "", false
	}
	return d.Password, true
}

// Register stores a contact and returns false when the extension is unknown.
func (s *Store) Register(ext, contact, source, ua, model, firmware string, ttl time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[ext]
	if !ok || !d.Enabled {
		return false
	}
	now := time.Now()
	d.Contact = contact
	d.Source = source
	d.UserAgent = ua
	d.Model = model
	d.Firmware = firmware
	d.LastSeen = now
	d.ExpiresAt = now.Add(ttl)
	return true
}

// Touch marks a registration as alive after a successful OPTIONS probe.
// The lease is extended so a device that re-registers less often than the
// server heartbeat interval does not flap offline.
func (s *Store) Touch(ext string, ttl time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[ext]
	if !ok || !d.Enabled || d.Contact == "" {
		return false
	}
	now := time.Now()
	d.LastSeen = now
	if ttl > 0 {
		d.ExpiresAt = now.Add(ttl)
	}
	return true
}

// Unregister removes the contact of an extension.
func (s *Store) Unregister(ext string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[ext]
	if !ok {
		return false
	}
	d.Contact = ""
	d.Source = ""
	d.ExpiresAt = time.Time{}
	d.LastSeen = time.Now()
	return true
}

// List returns all devices with computed online state, ordered by extension.
func (s *Store) List() []View {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	out := make([]View, 0, len(s.order))
	for _, ext := range s.order {
		d := s.devices[ext]
		v := View{
			Ext:       d.Ext,
			Name:      d.Name,
			Zone:      d.Zone,
			Contact:   d.Contact,
			Source:    d.Source,
			UserAgent: d.UserAgent,
			Model:     d.Model,
			Firmware:  d.Firmware,
			Snapshot:  d.Snapshot,
			Enabled:   d.Enabled,
			Online:    d.Enabled && d.Contact != "" && d.ExpiresAt.After(now),
		}
		if !d.ExpiresAt.IsZero() {
			v.ExpiresAt = d.ExpiresAt.Format(time.RFC3339)
		}
		if !d.LastSeen.IsZero() {
			v.LastSeen = d.LastSeen.Format(time.RFC3339)
		}
		out = append(out, v)
	}
	return out
}

// Count returns total and online device counts.
func (s *Store) Count() (total, online int) {
	for _, v := range s.List() {
		total++
		if v.Online {
			online++
		}
	}
	return total, online
}

// Get returns one device view.
func (s *Store) Get(ext string) (View, bool) {
	for _, v := range s.List() {
		if v.Ext == ext {
			return v, true
		}
	}
	return View{}, false
}
