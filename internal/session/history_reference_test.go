package session

import (
	"path/filepath"
	"pentest/internal/store"
	"reflect"
	"strings"
	"testing"
)

func TestHistoryReferenceCandidatesPreserveEveryDashPrefix(t *testing.T) {
	got := historyReferenceCandidates("event-a-b-child")
	want := []string{"event-a-b-child", "event", "event-a", "event-a-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
}

func TestHistoryEventSeqByRefKeepsEarliestMatchingEventAndOwner(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewService(db, filepath.Join(t.TempDir(), "sessions"))
	a, err := s.Create(CreateRequest{Input: "one"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(CreateRequest{Input: "two"})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, owner string
		seq       int
	}{{"event", a.ID, 11}, {"event-a", a.ID, 12}, {"event-a-b-child", b.ID, 30}} {
		_, err = db.Exec(`INSERT INTO session_events(id,session_id,seq,kind,payload_json,created_at) VALUES(?,?,?,'runtime_output','{}','2026-01-01T00:00:00Z')`, row.id, row.owner, row.seq)
		if err != nil {
			t.Fatal(err)
		}
	}
	seq, ok, err := s.HistoryEventSeqByRef(a.ID, "event-a-b-child")
	if err != nil || !ok || seq != 11 {
		t.Fatalf("resolve = %d,%v,%v", seq, ok, err)
	}
	_, ok, err = s.HistoryEventSeqByRef(a.ID, "unrelated")
	if err != nil || ok {
		t.Fatalf("unknown = %v,%v", ok, err)
	}
}

func TestHistoryEventSeqByRefUsesEventIDIndexWithoutAnalyze(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "history-plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	candidates := historyReferenceCandidates("event-a-b-child")
	args := make([]any, 0, len(candidates)+1)
	args = append(args, "session-1")
	for _, candidate := range candidates {
		args = append(args, candidate)
	}
	rows, err := db.Query("EXPLAIN QUERY PLAN "+historyEventSeqByRefQuery(len(candidates)), args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(details, "\n")
	if !strings.Contains(plan, "USING INDEX sqlite_autoindex_session_events_1 (id=?)") {
		t.Fatalf("expected event ID index lookup, got plan:\n%s", plan)
	}
	if strings.Contains(plan, "idx_session_events_session_seq") {
		t.Fatalf("unexpected session_seq index scan in plan:\n%s", plan)
	}
}
