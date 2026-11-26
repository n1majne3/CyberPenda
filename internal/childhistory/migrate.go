package childhistory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"pentest/internal/transcript"
)

type storedEventReference struct {
	ownerKind string
	ownerID   string
	id        string
	seq       int
	kind      string
}

func MigrateLegacyEventReferences(tx *sql.Tx) error {
	if tx == nil {
		return errors.New("child history migration transaction is required")
	}
	references, err := storedRuntimeEventReferences(tx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM child_history_context`); err != nil {
		return fmt.Errorf("reset child history migration context: %w", err)
	}
	for _, reference := range references {
		if reference.kind == "runtime_output" {
			indexed, err := eventIndexed(tx, reference)
			if err != nil {
				return err
			}
			if indexed {
				continue
			}
		}
		event, err := readStoredEvent(tx, reference)
		if err != nil {
			return err
		}
		if err := Record(tx, Owner{Kind: reference.ownerKind, ID: reference.ownerID}, event); err != nil {
			return fmt.Errorf("migrate %s Event %s: %w", reference.ownerKind, reference.id, err)
		}
	}
	return nil
}

func storedRuntimeEventReferences(tx *sql.Tx) ([]storedEventReference, error) {
	var references []storedEventReference
	for _, source := range []struct {
		ownerKind string
		table     string
		column    string
	}{
		{ownerKind: "task", table: "task_events", column: "task_id"},
		{ownerKind: "session", table: "session_events", column: "session_id"},
	} {
		rows, err := tx.Query(`SELECT ` + source.column + `,id,seq,kind FROM ` + source.table + ` WHERE kind IN ('lifecycle','runtime_output') ORDER BY ` + source.column + `,seq`)
		if err != nil {
			return nil, fmt.Errorf("list %s Events for child history migration: %w", source.ownerKind, err)
		}
		for rows.Next() {
			var reference storedEventReference
			reference.ownerKind = source.ownerKind
			if err := rows.Scan(&reference.ownerID, &reference.id, &reference.seq, &reference.kind); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan %s Event reference: %w", source.ownerKind, err)
			}
			references = append(references, reference)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("read %s Event references: %w", source.ownerKind, err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close %s Event references: %w", source.ownerKind, err)
		}
	}
	return references, nil
}

func eventIndexed(tx *sql.Tx, reference storedEventReference) (bool, error) {
	var count int
	err := tx.QueryRow(`SELECT COUNT(*) FROM child_history_observations WHERE owner_kind=? AND owner_id=? AND event_seq=?`, reference.ownerKind, reference.ownerID, reference.seq).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check migrated child history Event: %w", err)
	}
	return count > 0, nil
}

func readStoredEvent(tx *sql.Tx, reference storedEventReference) (transcript.Event, error) {
	table, column := "task_events", "task_id"
	if reference.ownerKind == "session" {
		table, column = "session_events", "session_id"
	}
	var payloadJSON, createdAt string
	err := tx.QueryRow(`SELECT payload_json,created_at FROM `+table+` WHERE `+column+`=? AND id=?`, reference.ownerID, reference.id).Scan(&payloadJSON, &createdAt)
	if err != nil {
		return transcript.Event{}, fmt.Errorf("read stored %s Event %s: %w", reference.ownerKind, reference.id, err)
	}
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return transcript.Event{}, fmt.Errorf("decode stored %s Event %s: %w", reference.ownerKind, reference.id, err)
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return transcript.Event{}, fmt.Errorf("parse stored %s Event %s time: %w", reference.ownerKind, reference.id, err)
	}
	return transcript.Event{ID: reference.id, Seq: reference.seq, Kind: reference.kind, Payload: payload, CreatedAt: created}, nil
}
