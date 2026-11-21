package blackboardv2

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type CurrentDetail struct {
	Schema        string              `json:"schema"`
	Revision      int                 `json:"revision"`
	Key           string              `json:"key"`
	Type          string              `json:"type"`
	Version       int                 `json:"version"`
	Record        Record              `json:"record"`
	Relationships []RelationshipTuple `json:"relationships"`
}

type RelationshipTuple []any

type HistoryOptions struct {
	Cursor string
	Limit  int
}

type SemanticHistory struct {
	Schema     string        `json:"schema"`
	Revision   int           `json:"revision"`
	Key        string        `json:"key"`
	Items      []HistoryItem `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

type HistoryItem struct {
	Kind     string  `json:"kind"`
	Key      string  `json:"key,omitempty"`
	Version  int     `json:"version"`
	Type     string  `json:"type,omitempty"`
	Record   *Record `json:"record,omitempty"`
	From     string  `json:"from,omitempty"`
	Relation string  `json:"relation,omitempty"`
	To       string  `json:"to,omitempty"`
	Reason   string  `json:"reason,omitempty"`
}

func (s *Service) ReadCurrent(ctx context.Context, projectID, key string) (CurrentDetail, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CurrentDetail{}, fmt.Errorf("begin Blackboard v2 read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureProjectExists(ctx, tx, projectID); err != nil {
		return CurrentDetail{}, err
	}
	revision, err := currentRevisionOrZero(ctx, tx, projectID)
	if err != nil {
		return CurrentDetail{}, err
	}
	found, err := loadCurrentRecord(ctx, tx, projectID, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CurrentDetail{}, semanticError("not_found", fmt.Sprintf("%s was not found", key), "key", map[string]any{"key": key})
		}
		return CurrentDetail{}, err
	}
	relationships, err := loadCurrentRelationshipsForKey(ctx, tx, projectID, found.key)
	if err != nil {
		return CurrentDetail{}, err
	}
	return CurrentDetail{
		Schema:        recordSchema,
		Revision:      revision,
		Key:           found.key,
		Type:          found.typ,
		Version:       found.version,
		Record:        found.record,
		Relationships: relationships,
	}, nil
}

func (s *Service) ReadHistory(ctx context.Context, projectID, key string, options HistoryOptions) (SemanticHistory, error) {
	if err := validateKey(key, "key"); err != nil {
		return SemanticHistory{}, err
	}
	if options.Limit < 0 || options.Limit > 100 {
		return SemanticHistory{}, semanticError("semantic_validation", "history limit must be between 1 and 100", "limit", nil)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SemanticHistory{}, fmt.Errorf("begin Blackboard v2 history read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureProjectExists(ctx, tx, projectID); err != nil {
		return SemanticHistory{}, err
	}
	cursorKey, err := loadHistoryCursorKey(ctx, tx)
	if err != nil {
		return SemanticHistory{}, err
	}
	cursor, err := parseCursor(options.Cursor, projectID, cursorKey)
	if err != nil {
		return SemanticHistory{}, err
	}
	limit := options.Limit
	if cursor.present {
		if limit == 0 {
			limit = cursor.limit
		} else if limit != cursor.limit {
			return SemanticHistory{}, invalidHistoryCursorError("page_size_mismatch")
		}
	} else if limit == 0 {
		limit = 20
	}
	revision, err := currentRevisionOrZero(ctx, tx, projectID)
	if err != nil {
		return SemanticHistory{}, err
	}
	canonicalKey, _, err := resolveKeyRedirect(ctx, tx, projectID, key)
	if err != nil {
		return SemanticHistory{}, err
	}
	key = canonicalKey
	if cursor.present && cursor.key != canonicalKey {
		return SemanticHistory{}, invalidHistoryCursorError("key_mismatch")
	}
	if cursor.present && cursor.revision != revision {
		return SemanticHistory{}, semanticError("semantic_validation", "history cursor is stale", "cursor", map[string]any{
			"reason":           "stale_cursor",
			"cursor_revision":  float64(cursor.revision),
			"current_revision": float64(revision),
			"next_action":      "restart_history_read",
		})
	}
	offset := cursor.offset
	hasCurrent := true
	if _, err := loadCurrentRecord(ctx, tx, projectID, canonicalKey); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return SemanticHistory{}, err
		}
		hasCurrent = false
	}
	if !hasCurrent {
		var recordHistoryCount int
		err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM blackboard_v2_record_history
			WHERE project_id = ? AND (key = ? OR key IN (
				SELECT source_key FROM blackboard_v2_key_redirects WHERE project_id = ? AND canonical_key = ?
			))`, projectID, key, projectID, key).Scan(&recordHistoryCount)
		if err != nil {
			return SemanticHistory{}, fmt.Errorf("check Blackboard v2 history: %w", err)
		}
		if recordHistoryCount == 0 {
			return SemanticHistory{}, semanticError("not_found", fmt.Sprintf("%s was not found", key), "key", map[string]any{"key": key})
		}
	}
	total, err := countSemanticHistoryItems(ctx, tx, projectID, key)
	if err != nil {
		return SemanticHistory{}, err
	}
	if cursor.present && offset >= total {
		return SemanticHistory{}, invalidHistoryCursorError("offset_out_of_range")
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT identity_key, kind, version, type, record_json, from_key, relation, to_key, reason
		FROM (
			SELECT 0 AS sort_group, recorded_at AS sort_time, key AS identity_key, 'record' AS kind, version, type, record_json,
			       '' AS from_key, '' AS relation, '' AS to_key, '' AS reason
			FROM blackboard_v2_record_history
			WHERE project_id = ? AND (
				key = ? OR key IN (
					SELECT source_key FROM blackboard_v2_key_redirects
					WHERE project_id = ? AND canonical_key = ?
				)
			)
			UNION ALL
			SELECT 1 AS sort_group, recorded_at AS sort_time, from_key || char(0) || relation || char(0) || to_key AS identity_key, 'relationship' AS kind, version, '' AS type, '' AS record_json,
			       from_key, relation, to_key, reason
			FROM blackboard_v2_relationship_history
			WHERE project_id = ? AND (
				from_key = ? OR to_key = ? OR
				from_key IN (SELECT source_key FROM blackboard_v2_key_redirects WHERE project_id = ? AND canonical_key = ?) OR
				to_key IN (SELECT source_key FROM blackboard_v2_key_redirects WHERE project_id = ? AND canonical_key = ?)
			)
		)
		ORDER BY sort_group ASC,
		         CASE WHEN sort_group = 0 THEN sort_time ELSE '' END ASC,
		         CASE WHEN sort_group = 0 THEN version ELSE 0 END ASC,
		         CASE WHEN sort_group = 0 THEN identity_key ELSE '' END ASC,
		         relation ASC, from_key ASC, to_key ASC, version ASC
		LIMIT ? OFFSET ?`,
		projectID, key, projectID, key,
		projectID, key, key, projectID, key, projectID, key,
		limit, offset,
	)
	if err != nil {
		return SemanticHistory{}, fmt.Errorf("read Blackboard v2 history: %w", err)
	}
	defer rows.Close()

	items := make([]HistoryItem, 0, limit)
	for rows.Next() {
		var version int
		var identityKey, kind, typ, raw, from, relation, to, reason string
		if err := rows.Scan(&identityKey, &kind, &version, &typ, &raw, &from, &relation, &to, &reason); err != nil {
			return SemanticHistory{}, fmt.Errorf("scan Blackboard v2 history: %w", err)
		}
		if len(items) < limit {
			if kind == "record" {
				record, err := decodeStoredRecord(typ, raw)
				if err != nil {
					return SemanticHistory{}, fmt.Errorf("decode Blackboard v2 history record: %w", err)
				}
				items = append(items, HistoryItem{Kind: kind, Key: identityKey, Version: version, Type: typ, Record: &record})
			} else {
				items = append(items, HistoryItem{Kind: kind, Version: version, From: from, Relation: relation, To: to, Reason: reason})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return SemanticHistory{}, fmt.Errorf("iterate Blackboard v2 history: %w", err)
	}
	next := ""
	if offset+len(items) < total {
		next, err = makeCursor(revision, canonicalKey, limit, offset+len(items), projectID, cursorKey)
		if err != nil {
			return SemanticHistory{}, err
		}
	}
	return SemanticHistory{Schema: historySchema, Revision: revision, Key: canonicalKey, Items: items, NextCursor: next}, nil
}

func countSemanticHistoryItems(ctx context.Context, tx *sql.Tx, projectID, key string) (int, error) {
	var total int
	err := tx.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM blackboard_v2_record_history WHERE project_id = ? AND (
				key = ? OR key IN (SELECT source_key FROM blackboard_v2_key_redirects WHERE project_id = ? AND canonical_key = ?)
			)) +
			(SELECT COUNT(*) FROM blackboard_v2_relationship_history WHERE project_id = ? AND (
				from_key = ? OR to_key = ? OR
				from_key IN (SELECT source_key FROM blackboard_v2_key_redirects WHERE project_id = ? AND canonical_key = ?) OR
				to_key IN (SELECT source_key FROM blackboard_v2_key_redirects WHERE project_id = ? AND canonical_key = ?)
			))`,
		projectID, key, projectID, key,
		projectID, key, key, projectID, key, projectID, key,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("count Blackboard v2 history: %w", err)
	}
	return total, nil
}

type historyCursor struct {
	revision int
	key      string
	limit    int
	offset   int
	present  bool
}

type historyCursorPayload struct {
	Schema   string `json:"schema"`
	Revision int    `json:"revision"`
	Key      string `json:"key"`
	Limit    int    `json:"limit"`
	Offset   int    `json:"offset"`
}

const historyCursorMACDomain = "CyberPenda.Blackboard.SemanticHistoryCursor.v2"

func loadHistoryCursorKey(ctx context.Context, tx *sql.Tx) ([]byte, error) {
	var key []byte
	if err := tx.QueryRowContext(ctx, `SELECT cursor_secret FROM blackboard_read_state WHERE id=1`).Scan(&key); err != nil {
		return nil, fmt.Errorf("load Semantic History cursor integrity key: %w", err)
	}
	if len(key) != sha256.Size {
		return nil, fmt.Errorf("Semantic History cursor integrity key has invalid length %d", len(key))
	}
	return append([]byte(nil), key...), nil
}

func parseCursor(cursor, projectID string, cursorKey []byte) (historyCursor, error) {
	if cursor == "" {
		return historyCursor{}, nil
	}
	if !strings.HasPrefix(cursor, "opaque:") {
		return historyCursor{}, invalidHistoryCursorError("malformed")
	}
	parts := strings.Split(strings.TrimPrefix(cursor, "opaque:"), ".")
	if len(parts) != 2 {
		return historyCursor{}, invalidHistoryCursorError("malformed")
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return historyCursor{}, invalidHistoryCursorError("malformed")
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size || !hmac.Equal(signature, historyCursorMAC(cursorKey, projectID, payload)) {
		return historyCursor{}, invalidHistoryCursorError("integrity_check_failed")
	}
	var decoded historyCursorPayload
	if err := strictDecodeJSON(payload, &decoded); err != nil {
		return historyCursor{}, invalidHistoryCursorError("malformed")
	}
	if decoded.Schema != "semantic-history-cursor/v2" || decoded.Revision < 0 || decoded.Limit < 1 || decoded.Limit > 100 || decoded.Offset < 1 {
		return historyCursor{}, invalidHistoryCursorError("malformed")
	}
	if err := validateKey(decoded.Key, "cursor"); err != nil {
		return historyCursor{}, invalidHistoryCursorError("malformed")
	}
	return historyCursor{revision: decoded.Revision, key: decoded.Key, limit: decoded.Limit, offset: decoded.Offset, present: true}, nil
}

func makeCursor(revision int, key string, limit, offset int, projectID string, cursorKey []byte) (string, error) {
	payload, err := json.Marshal(historyCursorPayload{Schema: "semantic-history-cursor/v2", Revision: revision, Key: key, Limit: limit, Offset: offset})
	if err != nil {
		return "", fmt.Errorf("encode Semantic History cursor: %w", err)
	}
	signature := historyCursorMAC(cursorKey, projectID, payload)
	return "opaque:" + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func historyCursorMAC(cursorKey []byte, projectID string, payload []byte) []byte {
	mac := hmac.New(sha256.New, cursorKey)
	for _, framed := range [][]byte{[]byte(historyCursorMACDomain), []byte(projectID), payload} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(framed)))
		_, _ = mac.Write(size[:])
		_, _ = mac.Write(framed)
	}
	return mac.Sum(nil)
}

func invalidHistoryCursorError(reason string) error {
	return semanticError("semantic_validation", "history cursor is invalid", "cursor", map[string]any{"reason": reason, "next_action": "restart_history_read"})
}
