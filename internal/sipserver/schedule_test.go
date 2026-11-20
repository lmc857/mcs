package sipserver

import (
	"testing"
	"time"
)

func TestScheduleStoreDueAndPersist(t *testing.T) {
	dir := t.TempDir()
	st := newScheduleStore(dir)
	sch, err := st.add(Schedule{
		Name: "test", ZoneID: "z", Text: "hello", Cron: "* * * * *", Priority: 3, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sch.NextRun == "" {
		t.Fatal("next_run empty")
	}

	due := st.due(time.Now().Add(2 * time.Minute))
	if len(due) != 1 || due[0].ID != sch.ID {
		t.Fatalf("due = %#v", due)
	}
	if again := st.due(time.Now().Add(2 * time.Minute)); len(again) != 0 {
		t.Fatalf("schedule fired twice: %#v", again)
	}

	reloaded := newScheduleStore(dir)
	if got := reloaded.list(); len(got) != 1 || got[0].ID != sch.ID {
		t.Fatalf("persist failed: %#v", got)
	}
}

func TestScheduleRejectsBadCron(t *testing.T) {
	st := newScheduleStore(t.TempDir())
	if _, err := st.add(Schedule{Name: "bad", ZoneID: "z", Text: "x", Cron: "not a cron"}); err == nil {
		t.Fatal("expected bad cron error")
	}
	if _, err := st.add(Schedule{Name: "empty", ZoneID: "z", Cron: "* * * * *"}); err == nil {
		t.Fatal("expected missing source error")
	}
}
