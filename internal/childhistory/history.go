// Package childhistory stores the child Transcript read model in the source
// Event transaction. Reads use indexed pages; no request replays owner history.
package childhistory

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"pentest/internal/transcript"
)

const snapshotPageSQL = `SELECT i.position,c.cursor,c.preview_json FROM child_history_items i JOIN child_history_changes c ON c.cursor=(SELECT v.cursor FROM child_history_changes v WHERE v.owner_kind=i.owner_kind AND v.owner_id=i.owner_id AND v.child_id=i.child_id AND v.item_id=i.item_id AND v.event_seq<=? ORDER BY v.event_seq DESC,v.cursor DESC LIMIT 1) WHERE i.owner_kind=? AND i.owner_id=? AND i.child_id=? AND i.position<? AND i.first_seq<=? ORDER BY i.position DESC LIMIT ?`

const cursorAtEventSQL = `SELECT COALESCE((SELECT cursor FROM child_history_changes WHERE owner_kind=? AND owner_id=? AND child_id=? AND event_seq<=? ORDER BY event_seq DESC,cursor DESC LIMIT 1),0)`

const MaxItems = 200
const MaxBytes = 256 << 10

// Owner identifies one isolated Runtime Owner and its authorized child route.
type Owner struct{ Kind, ID, Base string }

// Record folds one new source Event into durable child state before commit.
// A failed projection rolls back together with its source Event.
func Record(tx *sql.Tx, owner Owner, event transcript.Event) error {
	if event.Kind != "runtime_output" && event.Kind != "lifecycle" {
		return nil
	}
	var context transcript.WindowContext
	err := tx.QueryRow(`SELECT continuation,adapter FROM child_history_context WHERE owner_kind=? AND owner_id=?`, owner.Kind, owner.ID).Scan(&context.Continuation, &context.Adapter)
	if errors.Is(err, sql.ErrNoRows) {
		// This one-time write-side initialization supports an owner that was live
		// during upgrade without reinterpreting its older child output.
		table, column := "task_events", "task_id"
		if owner.Kind == "session" {
			table, column = "session_events", "session_id"
		}
		err = tx.QueryRow(`SELECT COUNT(*),COALESCE((SELECT json_extract(payload_json,'$.adapter') FROM `+table+` WHERE `+column+`=? AND seq<? AND kind='lifecycle' AND json_extract(payload_json,'$.phase')='started' ORDER BY seq DESC LIMIT 1),'') FROM `+table+` WHERE `+column+`=? AND seq<? AND kind='lifecycle' AND json_extract(payload_json,'$.phase')='started'`, owner.ID, event.Seq, owner.ID, event.Seq).Scan(&context.Continuation, &context.Adapter)
	}
	if err != nil {
		return err
	}
	if event.Kind == "lifecycle" {
		if event.Payload["phase"] == "started" {
			context.Continuation++
			context.Adapter, _ = event.Payload["adapter"].(string)
		}
	}
	if _, err = tx.Exec(`INSERT INTO child_history_context VALUES (?,?,?,?) ON CONFLICT(owner_kind,owner_id) DO UPDATE SET continuation=excluded.continuation,adapter=excluded.adapter`, owner.Kind, owner.ID, context.Continuation, context.Adapter); err != nil {
		return err
	}
	if event.Kind != "runtime_output" {
		return nil
	}
	event.Payload = maps.Clone(event.Payload)
	event.Payload["child_history_v1"] = true
	for _, block := range transcript.BuildWindow(transcript.Subject{}, []transcript.Event{event}, context) {
		if block.Kind != transcript.KindSubagentBlock {
			continue
		}
		if err := recordBlock(tx, owner, block); err != nil {
			return err
		}
	}
	return nil
}

func recordBlock(tx *sql.Tx, owner Owner, block transcript.Entry) error {
	items, _ := block.Details["items"].([]any)
	delete(block.Details, "items")
	canonical := boundedID(block.ID)
	// Durable task IDs and spawn tool IDs are aliases within this owner only.
	for _, alias := range []string{block.ID, "subagent-" + stringDetail(block, "agent_id"), "subagent-" + stringDetail(block, "spawn_tool_use_id")} {
		if alias == "subagent-" {
			continue
		}
		var found string
		err := tx.QueryRow(`SELECT child_id FROM child_history_aliases WHERE owner_kind=? AND owner_id=? AND alias=?`, owner.Kind, owner.ID, alias).Scan(&found)
		if err == nil {
			canonical = found
			break
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	var previous transcript.Entry
	var raw string
	err := tx.QueryRow(`SELECT summary_json FROM child_history_blocks WHERE owner_kind=? AND owner_id=? AND child_id=?`, owner.Kind, owner.ID, canonical).Scan(&raw)
	if err == nil {
		if err = json.Unmarshal([]byte(raw), &previous); err != nil {
			return err
		}
		previous.Seq = block.Seq
		if block.Status != "" {
			previous.Status = block.Status
		}
		if stringDetail(block, "description") != "" {
			previous.Text = block.Text
		}
		if block.ToolName != "" {
			previous.ToolName = block.ToolName
		}
		for k, v := range block.Details {
			// Item-only observations use the spawn id as agent_id. Keep a known
			// durable identity when these observations follow a lifecycle record.
			if (k == "agent_id" && v == stringDetail(previous, "spawn_tool_use_id")) || (k == "spawn_tool_use_id" && v == stringDetail(block, "agent_id") && stringDetail(previous, "spawn_tool_use_id") != "") {
				continue
			}
			previous.Details[k] = v
		}
		block = previous
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	block.ID = canonical
	rawBytes, err := json.Marshal(summaryPreview(block))
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO child_history_blocks VALUES (?,?,?,?) ON CONFLICT(owner_kind,owner_id,child_id) DO UPDATE SET summary_json=excluded.summary_json`, owner.Kind, owner.ID, canonical, string(rawBytes)); err != nil {
		return err
	}
	for _, alias := range []string{canonical, "subagent-" + stringDetail(block, "agent_id"), "subagent-" + stringDetail(block, "spawn_tool_use_id")} {
		if alias == "subagent-" {
			continue
		}
		if _, err = tx.Exec(`INSERT INTO child_history_aliases VALUES (?,?,?,?) ON CONFLICT(owner_kind,owner_id,alias) DO NOTHING`, owner.Kind, owner.ID, alias, canonical); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO child_history_observations VALUES (?,?,?,?) ON CONFLICT DO NOTHING`, owner.Kind, owner.ID, canonical, block.Seq); err != nil {
		return err
	}
	for _, item := range items {
		raw, err := json.Marshal(item)
		if err != nil {
			return err
		}
		var entry transcript.Entry
		if err = json.Unmarshal(raw, &entry); err != nil {
			return err
		}
		if err = recordItem(tx, owner, canonical, entry); err != nil {
			return err
		}
	}
	return nil
}

func recordItem(tx *sql.Tx, owner Owner, child string, entry transcript.Entry) error {
	entry.ID = boundedID(entry.ID)
	var previousJSON string
	var base int
	err := tx.QueryRow(`SELECT preview_json,base_cursor FROM child_history_changes WHERE owner_kind=? AND owner_id=? AND child_id=? AND item_id=? ORDER BY event_seq DESC,cursor DESC LIMIT 1`, owner.Kind, owner.ID, child, entry.ID).Scan(&previousJSON, &base)
	cumulative := entry
	if err == nil && entry.Incremental {
		var previous transcript.Entry
		if err = json.Unmarshal([]byte(previousJSON), &previous); err != nil {
			return err
		}
		cumulative.Text = previous.Text + entry.Text
		cumulative.Truncated = previous.Truncated
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	cumulative.Incremental = false
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	small, err := json.Marshal(preview(cumulative))
	if err != nil {
		return err
	}
	rawSmall, err := json.Marshal(preview(entry))
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO child_history_items(owner_kind,owner_id,child_id,item_id,first_seq) VALUES (?,?,?,?,?) ON CONFLICT(owner_kind,owner_id,child_id,item_id) DO NOTHING`, owner.Kind, owner.ID, child, entry.ID, entry.Seq); err != nil {
		return err
	}
	result, err := tx.Exec(`INSERT INTO child_history_changes(owner_kind,owner_id,child_id,item_id,event_seq,entry_json,preview_json,raw_preview_json,base_cursor) VALUES (?,?,?,?,?,?,?,?,?)`, owner.Kind, owner.ID, child, entry.ID, entry.Seq, string(raw), string(small), string(rawSmall), base)
	if err != nil {
		return err
	}
	if !entry.Incremental || base == 0 {
		cursor, err := result.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE child_history_changes SET base_cursor=? WHERE cursor=?`, cursor, cursor)
		return err
	}
	return nil
}

func stringDetail(entry transcript.Entry, key string) string {
	value, _ := entry.Details[key].(string)
	return value
}

// Summary returns the current child identity and state. The caller keeps its
// source-window Seq so a newer status cannot skip that window's event cursor.
func Summary(db *sql.DB, owner Owner, ref string) (transcript.Entry, bool, error) {
	var raw string
	err := db.QueryRow(`SELECT summary_json FROM child_history_blocks WHERE owner_kind=? AND owner_id=? AND child_id=COALESCE((SELECT child_id FROM child_history_aliases WHERE owner_kind=? AND owner_id=? AND alias=?),?)`, owner.Kind, owner.ID, owner.Kind, owner.ID, ref, ref).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return transcript.Entry{}, false, nil
	}
	if err != nil {
		return transcript.Entry{}, false, err
	}
	var entry transcript.Entry
	if err = json.Unmarshal([]byte(raw), &entry); err != nil {
		return entry, false, err
	}
	entry.Details["history"] = owner.Base + "/" + url.PathEscape(entry.ID)
	entry.Details["updated_seq"] = entry.Seq
	return entry, true, nil
}

// Query uses separate stable item positions for browsing and change cursors
// for live updates. Through freezes a source Event boundary for export.
type Query struct {
	Before     int
	After      int
	AfterSet   bool
	Through    int
	AfterEvent int
	Changes    bool
}

type Page struct {
	Entries  []transcript.Entry `json:"entries"`
	Cursor   int                `json:"cursor"`
	Before   int                `json:"before"`
	HasOlder bool               `json:"has_older"`
	HasNewer bool               `json:"has_newer"`
}

// Read returns one bounded page. Snapshot browsing picks one version per
// stable item; live reads return updates after a committed change cursor.
func Read(db *sql.DB, owner Owner, child string, q Query) (Page, bool, error) {
	summary, found, err := Summary(db, owner, child)
	if err != nil || !found {
		return Page{}, found, err
	}
	child = summary.ID
	through := q.Through
	if through <= 0 || through > summary.Seq {
		through = summary.Seq
	}
	var tail int
	err = db.QueryRow(cursorAtEventSQL, owner.Kind, owner.ID, child, through).Scan(&tail)
	if err != nil {
		return Page{}, true, err
	}
	p := Page{Entries: []transcript.Entry{}, Cursor: tail}
	var rows *sql.Rows
	if q.AfterSet || q.Changes {
		if q.AfterEvent > 0 {
			var floor int
			err = db.QueryRow(cursorAtEventSQL, owner.Kind, owner.ID, child, q.AfterEvent).Scan(&floor)
			if err != nil {
				return p, true, err
			}
			q.After = max(q.After, floor)
		}
		previewColumn := "c.preview_json"
		if q.Changes {
			previewColumn = "c.raw_preview_json"
		}
		rows, err = db.Query(`SELECT i.position,c.cursor,`+previewColumn+` FROM child_history_changes c JOIN child_history_items i ON i.owner_kind=c.owner_kind AND i.owner_id=c.owner_id AND i.child_id=c.child_id AND i.item_id=c.item_id WHERE c.owner_kind=? AND c.owner_id=? AND c.child_id=? AND c.cursor>? AND c.cursor<=? AND c.event_seq>? ORDER BY c.cursor LIMIT ?`, owner.Kind, owner.ID, child, q.After, tail, q.AfterEvent, MaxItems+1)
		p.Cursor = q.After
	} else {
		var lastPosition int
		err = db.QueryRow(`SELECT COALESCE((SELECT position FROM child_history_items WHERE owner_kind=? AND owner_id=? AND child_id=? AND first_seq<=? ORDER BY first_seq DESC,position DESC LIMIT 1),0)`, owner.Kind, owner.ID, child, through).Scan(&lastPosition)
		if err != nil {
			return p, true, err
		}
		before := lastPosition + 1
		if q.Before > 0 {
			before = min(before, q.Before)
		}
		// Each selected item performs an indexed lookup of its latest version at
		// the snapshot boundary. Neither query scans unrelated owner Events.
		rows, err = db.Query(snapshotPageSQL, through, owner.Kind, owner.ID, child, before, through, MaxItems+1)
	}
	if err != nil {
		return p, true, err
	}
	defer rows.Close()
	size := 0
	for rows.Next() {
		var position, cursor int
		var raw string
		if err = rows.Scan(&position, &cursor, &raw); err != nil {
			return p, true, err
		}
		var entry transcript.Entry
		if err = json.Unmarshal([]byte(raw), &entry); err != nil {
			return p, true, err
		}
		entry.Position = position
		if entry.Truncated {
			part := "/items/"
			if q.Changes {
				part = "/changes/"
			}
			entry.Detail = owner.Base + "/" + url.PathEscape(child) + part + strconv.Itoa(cursor)
		}
		encoded, _ := json.Marshal(entry)
		if len(p.Entries) >= MaxItems || (size+len(encoded) > MaxBytes-4096 && len(p.Entries) > 0) {
			if q.AfterSet || q.Changes {
				p.HasNewer = true
			} else {
				p.HasOlder = true
			}
			break
		}
		size += len(encoded)
		p.Entries = append(p.Entries, entry)
		p.Before = position
		if q.AfterSet || q.Changes {
			p.Cursor = cursor
		}
	}
	if err = rows.Err(); err != nil {
		return p, true, err
	}
	if !q.AfterSet && !q.Changes {
		slices.Reverse(p.Entries)
	} else if !p.HasNewer {
		p.Cursor = max(p.Cursor, tail)
	}
	return p, true, nil
}

// Detail resolves an immutable version, so a growing item still matches the
// exact ID and Seq in its preview while new Events are being appended.
func Detail(db *sql.DB, owner Owner, child string, cursor int) (transcript.Entry, bool, error) {
	entry, found, err := RawDetail(db, owner, child, cursor)
	if err != nil || !found {
		return entry, found, err
	}
	if !entry.Incremental {
		return entry, true, nil
	}
	var base int
	if err = db.QueryRow(`SELECT base_cursor FROM child_history_changes WHERE cursor=? AND owner_kind=? AND owner_id=? AND child_id=?`, cursor, owner.Kind, owner.ID, child).Scan(&base); err != nil {
		return entry, false, err
	}
	rows, err := db.Query(`SELECT entry_json FROM child_history_changes WHERE owner_kind=? AND owner_id=? AND child_id=? AND item_id=? AND cursor>=? AND cursor<=? ORDER BY cursor`, owner.Kind, owner.ID, child, entry.ID, base, cursor)
	if err != nil {
		return entry, false, err
	}
	defer rows.Close()
	var text strings.Builder
	for rows.Next() {
		var raw string
		var part transcript.Entry
		if err = rows.Scan(&raw); err != nil {
			return entry, false, err
		}
		if err = json.Unmarshal([]byte(raw), &part); err != nil {
			return entry, false, err
		}
		text.WriteString(part.Text)
	}
	if err = rows.Err(); err != nil {
		return entry, false, err
	}
	entry.Text = text.String()
	entry.Incremental = false
	return entry, true, nil
}

// RawDetail returns one retained patch for streaming export.
func RawDetail(db *sql.DB, owner Owner, child string, cursor int) (transcript.Entry, bool, error) {
	var raw string
	err := db.QueryRow(`SELECT entry_json FROM child_history_changes WHERE owner_kind=? AND owner_id=? AND child_id=? AND cursor=?`, owner.Kind, owner.ID, child, cursor).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return transcript.Entry{}, false, nil
	}
	if err != nil {
		return transcript.Entry{}, false, err
	}
	var entry transcript.Entry
	err = json.Unmarshal([]byte(raw), &entry)
	return entry, err == nil, err
}

// preview bounds every nested value and the overall payload at write time.
// Page reads never load the full value of an oversized item.
func preview(entry transcript.Entry) transcript.Entry {
	raw, _ := json.Marshal(entry)
	if len(raw) <= 32<<10 && len(entry.Text) <= 4000 && !entry.Truncated {
		return entry
	}
	entry.Truncated = true
	if len(entry.Text) > 4000 {
		entry.Text = boundedText(entry.Text, 4000)
	}
	entry.Details = map[string]any{"preview": "Content is available in the full entry."}
	// Stable bounded IDs keep oversized Provider metadata out of page payloads.
	// Full detail retains the original fields except the public item ID.
	entry.ID = boundedID(entry.ID)
	entry.ToolCallID = boundedID(entry.ToolCallID)
	entry.ToolName = boundedText(entry.ToolName, 512)
	entry.Stream = boundedText(entry.Stream, 128)
	entry.Status = boundedText(entry.Status, 128)
	entry.Role = boundedText(entry.Role, 128)
	entry.Kind = boundedText(entry.Kind, 128)
	return entry
}

// ParseQuery rejects malformed cursors instead of silently resetting progress.
func ParseQuery(values url.Values) (Query, error) {
	q := Query{}
	fields := map[string]*int{"before": &q.Before, "after": &q.After, "through": &q.Through, "after_event": &q.AfterEvent}
	for name, target := range fields {
		if raw := values.Get(name); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil || v < 0 {
				return q, fmt.Errorf("invalid %s cursor", name)
			}
			*target = v
		}
	}
	q.AfterSet = values.Has("after")
	q.Changes = values.Get("view") == "changes"
	if q.Before > 0 && q.AfterSet {
		return q, fmt.Errorf("choose before or after")
	}
	return q, nil
}

// MarkIndexedEvents enables the forward-only child stream parser only for
// Events captured by this read model. The Event window is already bounded.
func MarkIndexedEvents(db *sql.DB, owner Owner, events []transcript.Event) error {
	if len(events) == 0 {
		return nil
	}
	rows, err := db.Query(`SELECT DISTINCT event_seq FROM child_history_observations WHERE owner_kind=? AND owner_id=? AND event_seq>=? AND event_seq<=?`, owner.Kind, owner.ID, events[0].Seq, events[len(events)-1].Seq)
	if err != nil {
		return err
	}
	defer rows.Close()
	indexed := map[int]bool{}
	for rows.Next() {
		var seq int
		if err = rows.Scan(&seq); err != nil {
			return err
		}
		indexed[seq] = true
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for i := range events {
		if indexed[events[i].Seq] {
			events[i].Payload = maps.Clone(events[i].Payload)
			events[i].Payload["child_history_v1"] = true
		}
	}
	return nil
}

func boundedID(value string) string {
	if len(value) <= 512 {
		return value
	}
	return fmt.Sprintf("child-id-%x", sha256.Sum256([]byte(value)))
}
func boundedText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return strings.ToValidUTF8(value[:limit], "")
}
func summaryPreview(entry transcript.Entry) transcript.Entry {
	entry.Text = boundedText(entry.Text, 4000)
	entry.ToolName = boundedText(entry.ToolName, 512)
	entry.Status = boundedText(entry.Status, 128)
	entry.Details = maps.Clone(entry.Details)
	for key, value := range entry.Details {
		if text, ok := value.(string); ok {
			entry.Details[key] = boundedText(text, 4000)
		}
	}
	return entry
}
