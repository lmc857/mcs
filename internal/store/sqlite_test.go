package store

import (
	"path/filepath"
	"testing"

	"mcs/internal/config"
)

func TestSQLiteCDRAndAlarmPersistence(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "mcs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cdr := NewCDRStoreDB(10, db)
	cdr.Add(CDR{ID: "c1", CallID: "c1", From: "1001", To: "1002", StartAt: "2026-01-01T00:00:00Z", Result: "answered", Duration: 3})
	cdr.Add(CDR{ID: "c2", CallID: "c2", From: "1003", To: "8000", StartAt: "2026-01-01T00:01:00Z", Result: "failed"})

	reloadedCDR := NewCDRStoreDB(10, db)
	items := reloadedCDR.List(10)
	if len(items) != 2 || items[0].ID != "c2" || items[1].ID != "c1" {
		t.Fatalf("cdr reload = %#v", items)
	}

	alarms := NewAlarmStoreDB(10, db)
	a := alarms.Create(Alarm{From: "1003", To: "8000", State: "ringing", StartedAt: "2026-01-01T00:02:00Z"})
	if _, ok := alarms.Update(a.ID, func(x *Alarm) { x.State = "closed"; x.Disposition = "test" }); !ok {
		t.Fatal("update failed")
	}

	reloadedAlarms := NewAlarmStoreDB(10, db)
	got, ok := reloadedAlarms.Get(a.ID)
	if !ok || got.State != "closed" || got.Disposition != "test" {
		t.Fatalf("alarm reload = %#v ok=%v", got, ok)
	}
}

func TestSQLiteDeviceCRUD(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "mcs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	st := NewWithDB([]config.DeviceConfig{{Ext: "1001", Name: "seed", Password: "p1"}}, db)
	if _, ok := st.Get("1001"); !ok {
		t.Fatal("seed device missing")
	}
	if err := st.UpsertDevice("1002", "door", "p2", "A", ""); err != nil {
		t.Fatal(err)
	}
	reloaded := NewWithDB(nil, db)
	if _, ok := reloaded.Get("1002"); !ok {
		t.Fatal("device not persisted")
	}
	if !reloaded.DeleteDevice("1002") {
		t.Fatal("delete failed")
	}
	again := NewWithDB(nil, db)
	if _, ok := again.Get("1002"); ok {
		t.Fatal("deleted device reappeared")
	}
}

func TestSQLiteSchemaIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcs.db")
	db1, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db1.Close()
	db2, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
}
