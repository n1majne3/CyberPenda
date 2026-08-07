package task

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/project"
	"pentest/internal/store"
)

// Datastore regression for the bounded Project Navigation Projection (#201).
// An HTTP response-shape test cannot prove that historical Task rows were not
// scanned, so these tests run the exact query the navigation projection uses
// and require the query plan to be served by the navigation index rather than
// a full history scan.

func openNavigationStore(t *testing.T) (*store.DB, *Service) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	return db, NewService(db, project.NewService(db))
}

// seedNavigationTasks inserts count Tasks for one Project directly so a
// navigation test can create a deep history without launch machinery. Tasks
// are terminal (completed) so they are deletable, and updated_at advances with
// creation order.
func seedNavigationTasks(t *testing.T, db *store.DB, projectID string, count int) {
	t.Helper()
	base := time.Now().UTC().Add(-time.Duration(count) * time.Minute)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	for i := 0; i < count; i++ {
		now := base.Add(time.Duration(i) * time.Second)
		if _, err := tx.Exec(
			`INSERT INTO tasks (id, project_id, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at)
			 VALUES (?, ?, 'task', 'completed', 'sandbox', '', '{}', '{}', ?, ?)`,
			fmt.Sprintf("task-%s-%08d", projectID, i), projectID,
			now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		); err != nil {
			_ = tx.Rollback()
			t.Fatalf("seed task %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
}

// explainQueryPlan returns the detail lines of EXPLAIN QUERY PLAN for the
// given query and bound arguments.
func explainQueryPlan(t *testing.T, db *store.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	return details
}

// TestRecentPerProjectQueryPlanIsBoundedByIndex pins the query-plan regression:
// the per-Project recent query must be served by idx_tasks_project_activity
// (SEARCH by Project, ordered by the index) and must never SCAN the full Task
// table or sort it with a temp B-tree, no matter how deep the history is.
func TestRecentPerProjectQueryPlanIsBoundedByIndex(t *testing.T) {
	db, _ := openNavigationStore(t)
	projects := project.NewService(db)
	created, err := projects.Create("Plan", "plan project", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	seedNavigationTasks(t, db, created.ID, 5000)

	for _, excludeCount := range []int{0, 2} {
		args := []any{created.ID}
		for i := 0; i < excludeCount; i++ {
			args = append(args, fmt.Sprintf("excluded-%d", i))
		}
		args = append(args, 5)
		plan := explainQueryPlan(t, db, recentPerProjectSQL(5, excludeCount), args...)
		joined := strings.Join(plan, "\n")
		if !strings.Contains(joined, "USING INDEX idx_tasks_project_activity") {
			t.Fatalf("recent query plan does not use the navigation index:\n%s", joined)
		}
		if strings.Contains(joined, "SCAN tasks") {
			t.Fatalf("recent query plan scans the full Task table:\n%s", joined)
		}
		if strings.Contains(joined, "TEMP B-TREE") {
			t.Fatalf("recent query plan sorts the full history with a temp b-tree:\n%s", joined)
		}
	}
}

// TestListRecentPerProjectReturnsBoundedRows proves the recent selection stays
// at the fixed limit and honors recency and exclusions at the datastore level,
// independent of a Project's total Task count.
func TestListRecentPerProjectReturnsBoundedRows(t *testing.T) {
	db, svc := openNavigationStore(t)
	projects := project.NewService(db)
	shallow, err := projects.Create("Shallow", "shallow project", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create shallow project: %v", err)
	}
	deep, err := projects.Create("Deep", "deep project", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create deep project: %v", err)
	}
	seedNavigationTasks(t, db, shallow.ID, 3)
	seedNavigationTasks(t, db, deep.ID, 5000)

	recent, err := svc.ListRecentPerProject([]string{shallow.ID, deep.ID}, 5)
	if err != nil {
		t.Fatalf("list recent per project: %v", err)
	}
	if got := len(recent[shallow.ID]); got != 3 {
		t.Fatalf("shallow recent tasks = %d, want 3", got)
	}
	if got := len(recent[deep.ID]); got != 5 {
		t.Fatalf("deep recent tasks = %d, want bounded at 5", got)
	}
	// Newest first, newest of the deep history is the last seeded row.
	if got := recent[deep.ID][0].ID; got != fmt.Sprintf("task-%s-%08d", deep.ID, 4999) {
		t.Fatalf("most recent deep task = %q, want the last seeded row", got)
	}

	// Excluding the two newest Tasks must surface the next two without ever
	// changing the returned row count (busy/selected exclusion rule).
	newest := recent[deep.ID][0].ID
	second := recent[deep.ID][1].ID
	excluded, err := svc.ListRecentPerProject([]string{deep.ID}, 5, newest, second)
	if err != nil {
		t.Fatalf("list recent with exclusions: %v", err)
	}
	if got := len(excluded[deep.ID]); got != 5 {
		t.Fatalf("excluded recent tasks = %d, want 5", got)
	}
	for _, id := range []string{newest, second} {
		for _, found := range excluded[deep.ID] {
			if found.ID == id {
				t.Fatalf("excluded task %q still returned", id)
			}
		}
	}
}

// TestLatestUpdateAdvancesOnTaskChange pins the navigation revision epoch: the
// latest update must reflect creation and advance again when a Task is deleted
// (deletion bumps updated_at so a cached navigation projection cannot keep
// showing the deleted Task).
func TestLatestUpdateAdvancesOnTaskChange(t *testing.T) {
	db, svc := openNavigationStore(t)
	projects := project.NewService(db)
	created, err := projects.Create("Epoch", "epoch project", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	seedNavigationTasks(t, db, created.ID, 3)

	epoch, err := svc.LatestUpdate()
	if err != nil {
		t.Fatalf("latest update: %v", err)
	}
	if epoch.IsZero() {
		t.Fatalf("latest update is zero after seeding")
	}
	newest, err := svc.Get(fmt.Sprintf("task-%s-%08d", created.ID, 2))
	if err != nil {
		t.Fatalf("get newest task: %v", err)
	}
	if err := svc.Delete(newest.ID); err != nil {
		t.Fatalf("delete newest task: %v", err)
	}
	after, err := svc.LatestUpdate()
	if err != nil {
		t.Fatalf("latest update after delete: %v", err)
	}
	if !after.After(epoch) {
		t.Fatalf("latest update did not advance after deletion: before=%v after=%v", epoch, after)
	}
}
