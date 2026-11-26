package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"pentest/internal/childhistory"
	"pentest/internal/session"
	"testing"

	"pentest/internal/task"
)

func TestChildHistoryDoesNotDependOnMainWindow(t *testing.T) {
	server, projectID, taskID := newHistoryWindowFixture(t)
	appendOutput := func(text string) {
		t.Helper()
		if _, err := server.tasks.AppendEvent(taskID, task.EventKindRuntimeOutput, task.EventPayload{"stream": "stdout", "text": text}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{"phase": "started", "adapter": "claude_code"}); err != nil {
		t.Fatal(err)
	}
	appendOutput(`{"type":"system","subtype":"task_started","task_id":"child-a","tool_use_id":"spawn-a","description":"Child A","task_type":"local_agent"}`)
	for i := 0; i < 3; i++ {
		appendOutput(fmt.Sprintf(`{"type":"assistant","parent_tool_use_id":"spawn-a","message":{"role":"assistant","content":[{"type":"text","text":"child item %d"}]}}`, i))
	}
	appendConversationMessages(t, server, taskID, 850, 0)
	appendOutput(`{"type":"system","subtype":"task_updated","task_id":"child-a","tool_use_id":"spawn-a","patch":{"status":"completed"}}`)
	base := fmt.Sprintf("/api/projects/%s/tasks/%s/transcript", projectID, taskID)
	page := getHistoryURL(t, server, base)
	for _, entry := range page.Entries {
		if entry.Kind != "subagent_block" {
			continue
		}
		if entry.Status != "completed" {
			t.Fatalf("status = %s", entry.Status)
		}
		if _, inline := entry.Details["items"]; inline {
			t.Fatal("summary includes inline child history")
		}
		ref, _ := entry.Details["history"].(string)
		if ref == "" {
			t.Fatal("child summary has no independent history reference")
		}
		child := getHistoryURL(t, server, ref)
		if len(child.Entries) != 3 {
			t.Fatalf("child history has %d items, want 3", len(child.Entries))
		}
		for i, item := range child.Entries {
			if item.Text != fmt.Sprintf("child item %d", i) {
				t.Fatalf("child item %d = %q", i, item.Text)
			}
		}
		return
	}
	t.Fatal("missing child summary in lifecycle window")
}

func TestSessionChildHistorySplitsSameEventAndRollsBackWithSource(t *testing.T) {
	server, _, _ := newHistoryWindowFixture(t)
	owner, err := server.sessions.Create(session.CreateRequest{Input: "Session child history"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.sessions.AppendEvent(owner.ID, session.EventKindLifecycle, session.EventPayload{"phase": "started", "adapter": "claude_code"}); err != nil {
		t.Fatal(err)
	}
	parts := []map[string]any{}
	for i := 0; i < 230; i++ {
		parts = append(parts, map[string]any{"type": "text", "text": fmt.Sprintf("part %d", i)})
	}
	raw, err := json.Marshal(map[string]any{"type": "assistant", "parent_tool_use_id": "spawn", "message": map[string]any{"role": "assistant", "content": parts}})
	if err != nil {
		t.Fatal(err)
	}
	payload := session.EventPayload{"text": string(raw)}
	if _, err = server.sessions.AppendEvent(owner.ID, session.EventKindRuntimeOutput, payload); err != nil {
		t.Fatal(err)
	}
	base := "/api/sessions/" + owner.ID + "/transcript/children/subagent-spawn"
	read := func(path string) childhistory.Page {
		t.Helper()
		w := httptest.NewRecorder()
		server.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 200 {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		var p childhistory.Page
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := read(base)
	if len(p.Entries) != 200 || !p.HasOlder {
		t.Fatalf("initial count=%d hasOlder=%v", len(p.Entries), p.HasOlder)
	}
	older := read(fmt.Sprintf("%s?before=%d", base, p.Before))
	if len(older.Entries) != 30 || older.HasOlder {
		t.Fatalf("older count=%d hasOlder=%v", len(older.Entries), older.HasOlder)
	}
	if older.Entries[0].Text != "part 0" || p.Entries[0].Text != "part 30" || p.Entries[199].Text != "part 229" {
		t.Fatal("same-source page boundary lost content")
	}
	delta := read(base + "?after=0")
	if len(delta.Entries) != 200 || !delta.HasNewer {
		t.Fatalf("delta=%+v", delta)
	}
	rest := read(fmt.Sprintf("%s?after=%d", base, delta.Cursor))
	if len(rest.Entries) != 30 || rest.HasNewer {
		t.Fatalf("rest=%+v", rest)
	}
	tx, err := server.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.sessions.AppendEventTx(tx, owner.ID, session.EventKindRuntimeOutput, payload); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if after := read(fmt.Sprintf("%s?after=%d", base, p.Cursor)); len(after.Entries) != 0 {
		t.Fatal("rolled-back source left visible child items")
	}
}

func TestLegacyChildHistoryReferencesMigrateOnce(t *testing.T) {
	server, projectID, taskID := newHistoryWindowFixture(t)
	append := func(text string) {
		t.Helper()
		if _, err := server.tasks.AppendEvent(taskID, task.EventKindRuntimeOutput, task.EventPayload{"text": text}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{"phase": "started", "adapter": "claude_code"}); err != nil {
		t.Fatal(err)
	}
	append(`{"type":"assistant","parent_tool_use_id":"spawn-old","message":{"role":"assistant","content":[{"type":"text","text":"pre-upgrade work"}]}}`)
	for _, table := range []string{"child_history_changes", "child_history_items", "child_history_observations", "child_history_aliases", "child_history_blocks"} {
		if _, err := server.db.Exec("DELETE FROM " + table); err != nil {
			t.Fatal(err)
		}
	}
	append(`{"type":"assistant","parent_tool_use_id":"spawn-old","message":{"role":"assistant","content":[{"type":"text","text":"post-upgrade work"}]}}`)
	if _, err := server.db.Exec(`UPDATE schema_migrations SET version=1`); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyEventReferencesOnce(server.db); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := server.db.QueryRow(`SELECT version FROM schema_migrations`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("migration marker version=%d err=%v, want 2", version, err)
	}
	base := fmt.Sprintf("/api/projects/%s/tasks/%s/transcript", projectID, taskID)
	page := getHistoryURL(t, server, base)
	for _, entry := range page.Entries {
		if entry.Kind == "subagent_block" && entry.Details["legacy_items"] != nil {
			t.Fatal("request path still emits legacy_items")
		}
	}
	child := getHistoryURL(t, server, base+"/children/subagent-spawn-old")
	if len(child.Entries) != 2 || child.Entries[0].Text != "pre-upgrade work" || child.Entries[1].Text != "post-upgrade work" {
		t.Fatalf("migrated mixed child history=%+v", child.Entries)
	}
	if err := migrateLegacyEventReferencesOnce(server.db); err != nil {
		t.Fatal(err)
	}
	again := getHistoryURL(t, server, base+"/children/subagent-spawn-old")
	if len(again.Entries) != 2 || again.Entries[0].Text != "pre-upgrade work" || again.Entries[1].Text != "post-upgrade work" {
		t.Fatalf("migration was not one-time/idempotent: %+v", again.Entries)
	}
}
