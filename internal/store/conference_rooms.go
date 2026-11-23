package store

import (
	"database/sql"
	"time"
)

type ConferenceRoom struct {
	Number     string
	Name       string
	MaxMembers int
	Record     bool
	Enabled    bool
	CreatedAt  string
	UpdatedAt  string
}

func (d *DB) ListConferenceRooms() []ConferenceRoom {
	if d == nil {
		return nil
	}
	rows, err := d.sql.Query(`SELECT number,name,max_members,record,enabled,created_at,updated_at FROM conference_rooms ORDER BY number`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ConferenceRoom
	for rows.Next() {
		var r ConferenceRoom
		var record, enabled int
		if rows.Scan(&r.Number, &r.Name, &r.MaxMembers, &record, &enabled, &r.CreatedAt, &r.UpdatedAt) == nil {
			r.Record = record != 0
			r.Enabled = enabled != 0
			out = append(out, r)
		}
	}
	return out
}

func (d *DB) GetConferenceRoom(number string) (ConferenceRoom, bool) {
	if d == nil {
		return ConferenceRoom{}, false
	}
	var r ConferenceRoom
	var record, enabled int
	err := d.sql.QueryRow(`SELECT number,name,max_members,record,enabled,created_at,updated_at FROM conference_rooms WHERE number=?`, number).
		Scan(&r.Number, &r.Name, &r.MaxMembers, &record, &enabled, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return ConferenceRoom{}, false
	}
	r.Record = record != 0
	r.Enabled = enabled != 0
	return r, true
}

func (d *DB) SaveConferenceRoom(r ConferenceRoom) error {
	if d == nil {
		return sql.ErrConnDone
	}
	now := time.Now().Format(time.RFC3339)
	if r.CreatedAt == "" {
		r.CreatedAt = now
	}
	if r.UpdatedAt == "" {
		r.UpdatedAt = now
	}
	record, enabled := 0, 0
	if r.Record {
		record = 1
	}
	if r.Enabled {
		enabled = 1
	}
	_, err := d.sql.Exec(`INSERT INTO conference_rooms(number,name,max_members,record,enabled,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(number) DO UPDATE SET name=excluded.name,max_members=excluded.max_members,record=excluded.record,enabled=excluded.enabled,updated_at=excluded.updated_at`,
		r.Number, r.Name, r.MaxMembers, record, enabled, r.CreatedAt, r.UpdatedAt)
	return err
}

func (d *DB) DeleteConferenceRoom(number string) error {
	if d == nil {
		return sql.ErrConnDone
	}
	_, err := d.sql.Exec(`DELETE FROM conference_rooms WHERE number=?`, number)
	return err
}

func (s *Store) ListConferenceRooms() []ConferenceRoom {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.ListConferenceRooms()
}

func (s *Store) SaveConferenceRoom(r ConferenceRoom) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.SaveConferenceRoom(r)
}

func (s *Store) DeleteConferenceRoom(number string) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.DeleteConferenceRoom(number)
}
