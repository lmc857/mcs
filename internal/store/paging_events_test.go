package store

import (
	"path/filepath"
	"testing"
)

func TestPagingEventsPersist(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "mcs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := NewWithDB(nil, db)
	if err := st.SavePagingEvent(PagingEvent{ID: "p1", Source: "2010", ZoneID: "a", ZoneName: "A", Kind: "sip", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePagingEvent(PagingEvent{ID: "p1", Source: "2010", ZoneID: "a", ZoneName: "A", Kind: "sip", Status: "stopped", Delivered: 2, Total: 2}); err != nil {
		t.Fatal(err)
	}
	list := st.ListPagingEvents(10)
	if len(list) != 1 || list[0].Status != "stopped" || list[0].Delivered != 2 {
		t.Fatalf("events = %+v", list)
	}
}
