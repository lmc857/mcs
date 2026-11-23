package store

import "time"

type PagingEvent struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	SourceName string `json:"source_name"`
	ZoneID     string `json:"zone_id"`
	ZoneName   string `json:"zone_name"`
	Kind       string `json:"kind"`
	StartedAt  string `json:"started_at"`
	EndedAt    string `json:"ended_at"`
	Status     string `json:"status"`
	Delivered  int    `json:"delivered"`
	Total      int    `json:"total"`
	Error      string `json:"error,omitempty"`
}

func (d *DB) SavePagingEvent(e PagingEvent) error {
	if d == nil {
		return nil
	}
	if e.StartedAt == "" {
		e.StartedAt = time.Now().Format(time.RFC3339)
	}
	_, err := d.sql.Exec(`INSERT INTO paging_events(id,source,source_name,zone_id,zone_name,kind,started_at,ended_at,status,delivered,total,error)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET ended_at=excluded.ended_at,status=excluded.status,delivered=excluded.delivered,total=excluded.total,error=excluded.error`,
		e.ID, e.Source, e.SourceName, e.ZoneID, e.ZoneName, e.Kind, e.StartedAt, e.EndedAt, e.Status, e.Delivered, e.Total, e.Error)
	return err
}

func (d *DB) ListPagingEvents(limit int) []PagingEvent {
	if d == nil {
		return nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := d.sql.Query(`SELECT id,source,source_name,zone_id,zone_name,kind,started_at,ended_at,status,delivered,total,error
		FROM paging_events ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []PagingEvent
	for rows.Next() {
		var e PagingEvent
		if rows.Scan(&e.ID, &e.Source, &e.SourceName, &e.ZoneID, &e.ZoneName, &e.Kind, &e.StartedAt, &e.EndedAt, &e.Status, &e.Delivered, &e.Total, &e.Error) == nil {
			out = append(out, e)
		}
	}
	return out
}

func (s *Store) SavePagingEvent(e PagingEvent) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.SavePagingEvent(e)
}

func (s *Store) ListPagingEvents(limit int) []PagingEvent {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.ListPagingEvents(limit)
}
