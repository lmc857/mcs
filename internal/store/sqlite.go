package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DB is the MCS SQLite database (call records + alarm events).
// Config, media index and schedules keep their own JSON files for now.
type DB struct {
	sql *sql.DB
}

func OpenDB(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	raw.SetMaxOpenConns(1)
	if err := raw.Ping(); err != nil {
		_ = raw.Close()
		return nil, err
	}
	if _, err := raw.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = raw.Close()
		return nil, err
	}
	if _, err := raw.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = raw.Close()
		return nil, err
	}
	db := &DB{sql: raw}
	if err := db.migrate(); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) Close() error {
	if d == nil || d.sql == nil {
		return nil
	}
	return d.sql.Close()
}

func (d *DB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS cdr (
			id TEXT PRIMARY KEY,
			call_id TEXT NOT NULL,
			from_ext TEXT NOT NULL,
			to_ext TEXT NOT NULL,
			device_id TEXT NOT NULL DEFAULT '',
			start_at TEXT NOT NULL,
			answer_at TEXT NOT NULL DEFAULT '',
			end_at TEXT NOT NULL DEFAULT '',
			duration_s INTEGER NOT NULL DEFAULT 0,
			result TEXT NOT NULL,
			recording TEXT NOT NULL DEFAULT '',
			b_leg_code INTEGER NOT NULL DEFAULT 0,
			b_leg_reason TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cdr_start ON cdr(start_at)`,
		`CREATE TABLE IF NOT EXISTS alarms (
			id TEXT PRIMARY KEY,
			started_at TEXT NOT NULL,
			state TEXT NOT NULL,
			data TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_alarms_started ON alarms(started_at)`,
		`CREATE TABLE IF NOT EXISTS devices (
			ext TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			password TEXT NOT NULL,
			zone TEXT NOT NULL DEFAULT '',
			snapshot TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS paging_events (
			id TEXT PRIMARY KEY,
			source TEXT NOT NULL,
			source_name TEXT NOT NULL DEFAULT '',
			zone_id TEXT NOT NULL,
			zone_name TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT 'sip',
			started_at TEXT NOT NULL,
			ended_at TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			delivered INTEGER NOT NULL DEFAULT 0,
			total INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS conference_rooms (
			number TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			max_members INTEGER NOT NULL DEFAULT 8,
			record INTEGER NOT NULL DEFAULT 1,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			username TEXT PRIMARY KEY,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			last_login_at TEXT
		)`,
	}
	for _, stmt := range stmts {
		if _, err := d.sql.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// Upgrade pre-0.15 databases in place.
	_, _ = d.sql.Exec(`ALTER TABLE cdr ADD COLUMN b_leg_code INTEGER NOT NULL DEFAULT 0`)
	_, _ = d.sql.Exec(`ALTER TABLE cdr ADD COLUMN b_leg_reason TEXT NOT NULL DEFAULT ''`)
	_, _ = d.sql.Exec(`INSERT OR REPLACE INTO schema_meta(key,value) VALUES('schema_version','2')`)
	return nil
}

func (d *DB) GetSetting(key string) (string, bool) {
	if d == nil {
		return "", false
	}
	var value string
	if err := d.sql.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&value); err != nil {
		return "", false
	}
	return value, true
}

func (d *DB) SetSetting(key, value string) error {
	if d == nil {
		return nil
	}
	_, err := d.sql.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (d *DB) SaveCDR(c CDR) error {
	if d == nil {
		return nil
	}
	_, err := d.sql.Exec(`INSERT OR REPLACE INTO cdr
		(id,call_id,from_ext,to_ext,device_id,start_at,answer_at,end_at,duration_s,result,recording,b_leg_code,b_leg_reason)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.CallID, c.From, c.To, c.DeviceID, c.StartAt, c.AnswerAt, c.EndAt, c.Duration, c.Result, c.Recording, c.BLegCode, c.BLegReason)
	return err
}

func (d *DB) LoadCDR(limit int) []CDR {
	if d == nil {
		return nil
	}
	if limit <= 0 {
		limit = 2000
	}
	rows, err := d.sql.Query(`SELECT id,call_id,from_ext,to_ext,device_id,start_at,answer_at,end_at,duration_s,result,recording,b_leg_code,b_leg_reason
		FROM (SELECT * FROM cdr ORDER BY start_at DESC LIMIT ?) ORDER BY start_at ASC`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []CDR
	for rows.Next() {
		var c CDR
		if err := rows.Scan(&c.ID, &c.CallID, &c.From, &c.To, &c.DeviceID, &c.StartAt, &c.AnswerAt, &c.EndAt, &c.Duration, &c.Result, &c.Recording, &c.BLegCode, &c.BLegReason); err == nil {
			out = append(out, c)
		}
	}
	return out
}

func (d *DB) SaveDevice(dev Device) error {
	if d == nil {
		return nil
	}
	_, err := d.sql.Exec(`INSERT INTO devices(ext,name,password,zone,snapshot,enabled,updated_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(ext) DO UPDATE SET name=excluded.name,password=excluded.password,
			zone=excluded.zone,snapshot=excluded.snapshot,enabled=excluded.enabled,updated_at=excluded.updated_at`,
		dev.Ext, dev.Name, dev.Password, dev.Zone, dev.Snapshot, dev.Enabled, time.Now().Format(time.RFC3339))
	return err
}

func (d *DB) DeleteDevice(ext string) error {
	if d == nil {
		return nil
	}
	_, err := d.sql.Exec(`DELETE FROM devices WHERE ext=?`, ext)
	return err
}

func (d *DB) LoadDevices() []Device {
	if d == nil {
		return nil
	}
	rows, err := d.sql.Query(`SELECT ext,name,password,zone,snapshot,enabled FROM devices ORDER BY ext`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var dev Device
		var enabled int
		if err := rows.Scan(&dev.Ext, &dev.Name, &dev.Password, &dev.Zone, &dev.Snapshot, &enabled); err == nil {
			dev.Enabled = enabled != 0
			out = append(out, dev)
		}
	}
	return out
}

func (d *DB) SaveAlarm(a Alarm) error {
	if d == nil {
		return nil
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`INSERT OR REPLACE INTO alarms(id,started_at,state,data) VALUES(?,?,?,?)`,
		a.ID, a.StartedAt, a.State, string(raw))
	return err
}

func (d *DB) LoadAlarms(limit int) []Alarm {
	if d == nil {
		return nil
	}
	if limit <= 0 {
		limit = 1000
	}
	rows, err := d.sql.Query(`SELECT data FROM (SELECT * FROM alarms ORDER BY started_at DESC LIMIT ?) ORDER BY started_at ASC`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Alarm
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var a Alarm
		if err := json.Unmarshal([]byte(raw), &a); err == nil {
			out = append(out, a)
		}
	}
	return out
}
