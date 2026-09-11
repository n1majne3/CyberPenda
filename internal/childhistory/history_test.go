package childhistory_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/childhistory"
	"pentest/internal/store"
	"pentest/internal/transcript"
)

func TestChildHistorySurvivesHoursPagingAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	owner := childhistory.Owner{Kind: "session", ID: "owner", Base: "/children"}
	startChildRuntime(t, db, owner)
	seq := 0
	record := func(text string) {
		t.Helper()
		seq++
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		event := transcript.Event{ID: fmt.Sprintf("event-%d", seq), Seq: seq, Kind: "runtime_output", Payload: map[string]any{"text": text}, CreatedAt: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC).Add(time.Duration(seq) * time.Minute)}
		if err = childhistory.Record(tx, owner, event); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	// The child continues inside the retained Runtime context for hours.
	record(`{"type":"system","subtype":"task_started","task_id":"child","tool_use_id":"spawn","description":"Child","task_type":"local_agent"}`)
	for i := 0; i < 430; i++ {
		record(fmt.Sprintf(`{"type":"assistant","parent_tool_use_id":"spawn","message":{"role":"assistant","content":[{"type":"text","text":"item %d"}]}}`, i))
	}
	// A lifecycle update with only the durable identity still joins the index.
	record(`{"type":"system","subtype":"task_updated","task_id":"child","patch":{"status":"completed"}}`)
	summary, found, err := childhistory.Summary(db.DB, owner, "subagent-child")
	if err != nil || !found || summary.ID != "subagent-spawn" || summary.Details["spawn_tool_use_id"] != "spawn" || summary.Status != "completed" {
		t.Fatalf("summary=%+v found=%v error=%v", summary, found, err)
	}
	latest, found, err := childhistory.Read(db.DB, owner, summary.ID, childhistory.Query{})
	if err != nil || !found || len(latest.Entries) != 200 || !latest.HasOlder {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	seen := map[string]bool{}
	p := latest
	for {
		raw, _ := json.Marshal(p)
		if len(raw) > childhistory.MaxBytes {
			t.Fatalf("page bytes=%d", len(raw))
		}
		for _, entry := range p.Entries {
			if seen[entry.ID] {
				t.Fatalf("duplicate %s", entry.ID)
			}
			seen[entry.ID] = true
		}
		if !p.HasOlder {
			break
		}
		p, _, err = childhistory.Read(db.DB, owner, summary.ID, childhistory.Query{Before: p.Before})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 430 {
		t.Fatalf("visited=%d", len(seen))
	}
	record(`{"type":"assistant","parent_tool_use_id":"spawn","message":{"role":"assistant","content":[{"type":"text","text":"work after parent turn"}]}}`)
	delta, _, err := childhistory.Read(db.DB, owner, summary.ID, childhistory.Query{AfterSet: true, After: latest.Cursor})
	if err != nil || len(delta.Entries) != 1 || delta.Entries[0].Text != "work after parent turn" {
		t.Fatalf("delta=%+v err=%v", delta, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	restored, _, err := childhistory.Read(db.DB, owner, summary.ID, childhistory.Query{AfterSet: true, After: latest.Cursor})
	if err != nil || len(restored.Entries) != 1 || restored.Cursor != delta.Cursor {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
	other := owner
	other.ID = "other"
	if _, found, err := childhistory.Read(db.DB, other, summary.ID, childhistory.Query{}); err != nil || found {
		t.Fatalf("owner isolation: found=%v err=%v", found, err)
	}
}

func TestChildReasoningUsesBoundedPreviewAndImmutableDetail(t *testing.T) {
	db, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := childhistory.Owner{Kind: "task", ID: "owner", Base: "/children"}
	startChildRuntime(t, db, owner)
	appendDelta := func(seq int, text string) {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		event := transcript.Event{ID: fmt.Sprintf("event-%d", seq), Seq: seq, Kind: "runtime_output", Payload: map[string]any{"text": fmt.Sprintf(`{"type":"stream_event","parent_tool_use_id":"spawn","uuid":"thought","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":%q}}}`, text)}, CreatedAt: time.Now()}
		if err = childhistory.Record(tx, owner, event); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	appendDelta(1, strings.Repeat("a", 5000))
	first, _, err := childhistory.Read(db.DB, owner, "subagent-spawn", childhistory.Query{})
	if err != nil || len(first.Entries) != 1 || !first.Entries[0].Truncated {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	appendDelta(2, " tail")
	latest, _, err := childhistory.Read(db.DB, owner, "subagent-spawn", childhistory.Query{})
	if err != nil || len(latest.Entries) != 1 || len(latest.Entries[0].Text) > 4000 {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	full, found, err := childhistory.Detail(db.DB, owner, "subagent-spawn", latest.Cursor)
	if err != nil || !found || full.Text != strings.Repeat("a", 5000)+" tail" || full.Incremental {
		t.Fatalf("full length=%d err=%v", len(full.Text), err)
	}
	old, found, err := childhistory.Detail(db.DB, owner, "subagent-spawn", first.Cursor)
	if err != nil || !found || old.Seq != 1 || old.Text != strings.Repeat("a", 5000) {
		t.Fatalf("old detail changed: seq=%d len=%d err=%v", old.Seq, len(old.Text), err)
	}
	raw, _, err := childhistory.Read(db.DB, owner, "subagent-spawn", childhistory.Query{Changes: true, After: first.Cursor, AfterEvent: 1, Through: 2})
	if err != nil || len(raw.Entries) != 1 || raw.Entries[0].Text != " tail" || !raw.Entries[0].Incremental {
		t.Fatalf("export=%+v err=%v", raw, err)
	}
}

func startChildRuntime(t *testing.T, db *store.DB, owner childhistory.Owner) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = childhistory.Record(tx, owner, transcript.Event{ID: "started", Seq: 0, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "claude_code"}}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// Bridge-relayed pi child transcript lines (agentId-attributed, Claude
// Code-format) join the same child the settled subagents:record creates, and
// the child's operator-invisible prompt stays out of the page.
func TestPiChildOutputLinesJoinTheSettledBlock(t *testing.T) {
	db, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := childhistory.Owner{Kind: "session", ID: "owner", Base: "/children"}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = childhistory.Record(tx, owner, transcript.Event{ID: "started", Seq: 0, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "pi"}}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	record := func(seq int, text string) {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		event := transcript.Event{ID: fmt.Sprintf("event-%d", seq), Seq: seq, Kind: "runtime_output", CreatedAt: time.Now(),
			Payload: map[string]any{"provider": "pi", "stream": "pi_rpc", "text": text}}
		if err = childhistory.Record(tx, owner, event); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	record(1, `{"isSidechain":true,"agentId":"d62e4d35-5898-450","type":"user","message":{"role":"user","content":"child prompt"}}`)
	record(2, `{"isSidechain":true,"agentId":"d62e4d35-5898-450","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"child progress"}]}}`)
	record(3, `{"isSidechain":true,"agentId":"d62e4d35-5898-450","type":"toolResult","message":{"role":"toolResult","toolCallId":"call_1","toolName":"bash","content":[{"type":"text","text":"graph/"}],"isError":false}}`)
	record(4, `{"type":"custom","customType":"subagents:record","data":{"id":"d62e4d35-5898-450","type":"execute","description":"d-01 S3","status":"completed"}}`)

	summary, found, err := childhistory.Summary(db.DB, owner, "subagent-d62e4d35-5898-450")
	if err != nil || !found {
		t.Fatalf("summary found=%v err=%v", found, err)
	}
	if summary.Status != "completed" || summary.Details["description"] != "d-01 S3" {
		t.Fatalf("summary=%+v", summary)
	}
	page, found, err := childhistory.Read(db.DB, owner, summary.ID, childhistory.Query{})
	if err != nil || !found {
		t.Fatalf("read found=%v err=%v", found, err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("expected 2 child items (prompt excluded), got %+v", page.Entries)
	}
	if page.Entries[0].Text != "child progress" || page.Entries[1].Kind != transcript.KindToolResult || page.Entries[1].Text != "graph/" {
		t.Fatalf("entries=%+v", page.Entries)
	}
}

func TestChildPageBoundsProviderMetadata(t *testing.T) {
	db, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := childhistory.Owner{Kind: "task", ID: "owner", Base: "/children"}
	startChildRuntime(t, db, owner)
	huge := strings.Repeat("id", 200000)
	text, err := json.Marshal(map[string]any{"type": "assistant", "parent_tool_use_id": "spawn", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": huge, "name": huge, "input": map[string]any{"value": huge}}}}})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = childhistory.Record(tx, owner, transcript.Event{ID: "source", Seq: 1, Kind: "runtime_output", Payload: map[string]any{"text": string(text)}}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	page, _, err := childhistory.Read(db.DB, owner, "subagent-spawn", childhistory.Query{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(page)
	if len(page.Entries) != 1 || len(raw) > childhistory.MaxBytes || !page.Entries[0].Truncated {
		t.Fatalf("oversized page: items=%d bytes=%d", len(page.Entries), len(raw))
	}
	full, _, err := childhistory.Detail(db.DB, owner, "subagent-spawn", page.Cursor)
	if err != nil || full.ID != page.Entries[0].ID || full.ToolCallID != huge || full.ToolName != huge {
		t.Fatalf("full metadata did not survive: err=%v", err)
	}
}
