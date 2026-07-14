package blackboard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SnapshotReader exposes graph records from one SQLite read transaction. The
// first revision read anchors the snapshot before any requested records are
// resolved, so a concurrent Apply cannot produce a mixed-revision response.
type SnapshotReader struct {
	tx *sql.Tx
}

func (r SnapshotReader) ReadNode(ctx context.Context, req ReadNodeRequest) (ReadNodeResult, error) {
	return readNode(ctx, r.tx, req)
}

func (r SnapshotReader) ReadLiteralNode(ctx context.Context, req ReadLiteralNodeRequest) (ReadLiteralNodeResult, error) {
	return readLiteralNode(ctx, r.tx, req)
}

func (r SnapshotReader) ReadEdge(ctx context.Context, req ReadEdgeRequest) (EdgeRecord, error) {
	return readEdge(ctx, r.tx, req)
}

// WithReadSnapshot runs read against records and a graph revision from one
// read-only transaction. A Project with no graph state has revision zero.
func (s *GraphService) WithReadSnapshot(ctx context.Context, projectID string, read func(int, SnapshotReader) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin graph read snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var revision int
	err = tx.QueryRowContext(ctx,
		`SELECT current_graph_revision FROM blackboard_graph_state WHERE project_id = ?`, projectID,
	).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		revision = 0
	} else if err != nil {
		return fmt.Errorf("read graph snapshot revision: %w", err)
	}
	if err := read(revision, SnapshotReader{tx: tx}); err != nil {
		return err
	}
	return nil
}
