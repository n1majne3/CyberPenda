package childhistory

import (
	"pentest/internal/store"
	"strings"
	"testing"
)

// Query-plan assertions exercise the exact production statement. Response
// limits alone would not detect a scan of a multi-hour owner's history.
func TestSnapshotPageUsesIndexedSeeks(t *testing.T) {
	db, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+snapshotPageSQL, 100000, "task", "owner", "child", 100000, 100000, MaxItems+1)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
		if strings.Contains(detail, "SCAN ") || strings.Contains(detail, "TEMP B-TREE") {
			t.Fatalf("unbounded page plan: %s", detail)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "child_history_items_page") || !strings.Contains(joined, "child_history_changes_item") {
		t.Fatalf("missing page/version indexes: %s", joined)
	}
}

func TestHostedChangeFloorUsesEventIndex(t *testing.T) {
	db, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+cursorAtEventSQL, "task", "owner", "child", 100000)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "child_history_changes_event") {
			found = true
		}
		if strings.Contains(detail, "SCAN child_history_changes") || strings.Contains(detail, "TEMP B-TREE") {
			t.Fatalf("unbounded floor seek: %s", detail)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("Hosted floor does not use Event index")
	}
}
