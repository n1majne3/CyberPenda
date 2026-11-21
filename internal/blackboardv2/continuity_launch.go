package blackboardv2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pentest/internal/projectinterface"
	"pentest/internal/task"
)

func (s *ContinuityService) CreateContinuation(ctx context.Context, req ContinuationLaunchRequest) (ContinuationLaunch, error) {
	if s.db == nil || s.board == nil || s.tasks == nil {
		return ContinuationLaunch{}, fmt.Errorf("Blackboard v2 Continuation launch is unavailable")
	}
	s.board.snapshotMu.Lock()
	defer s.board.snapshotMu.Unlock()
	s.board.writeMu.Lock()
	defer s.board.writeMu.Unlock()
	var stagedProjection RuntimeSnapshotProjection
	if req.Precommit != nil {
		var err error
		stagedProjection, err = s.board.ProjectRuntimeSnapshot(ctx, req.ProjectID)
		if err != nil {
			return ContinuationLaunch{}, fmt.Errorf("stage current Runtime Blackboard Snapshot: %w", err)
		}
		if err := req.Precommit(ContinuationLaunchProjection{Schema: snapshotSchema, Revision: stagedProjection.Snapshot.Revision}); err != nil {
			return ContinuationLaunch{}, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ContinuationLaunch{}, fmt.Errorf("begin atomic Blackboard v2 Continuation launch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	projection, err := s.board.ProjectRuntimeSnapshotTx(ctx, tx, req.ProjectID)
	if err != nil {
		return ContinuationLaunch{}, fmt.Errorf("project current Runtime Blackboard Snapshot: %w", err)
	}
	if req.Precommit != nil && !bytes.Equal(stagedProjection.Bytes, projection.Bytes) {
		return ContinuationLaunch{}, fmt.Errorf("current Runtime Blackboard Snapshot changed during launch projection")
	}
	digest := sha256.Sum256(projection.Bytes)
	config, continuation, err := s.tasks.CreateContinuationLaunchTx(ctx, tx, task.ContinuationLaunchRequest{
		ProjectID: req.ProjectID, TaskID: req.TaskID, RuntimeProfileID: req.RuntimeProfileID,
		RuntimeProvider: req.RuntimeProvider, Runner: req.Runner, RuntimeConfig: req.RuntimeConfig,
		SteeringEventIDs: req.SteeringEventIDs,
		NativeSessionID:  req.NativeSessionID, NativeSessionPath: req.NativeSessionPath,
	})
	if err != nil {
		return ContinuationLaunch{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_continuation_pins
		(continuation_id,snapshot_schema,snapshot_revision,snapshot_bytes,integrity_sha256,created_at)
		VALUES (?,?,?,?,?,?)`,
		continuation.ID, snapshotSchema, projection.Snapshot.Revision, projection.Bytes, hex.EncodeToString(digest[:]), now,
	); err != nil {
		return ContinuationLaunch{}, fmt.Errorf("store immutable Launch Blackboard Pin: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_continuation_state
		(continuation_id,last_acknowledged_revision,working_snapshot_bytes,updated_at)
		VALUES (?,?,?,?)`, continuation.ID, projection.Snapshot.Revision, projection.Bytes, now,
	); err != nil {
		return ContinuationLaunch{}, fmt.Errorf("store initial Working Blackboard Snapshot: %w", err)
	}
	token := ""
	if s.grants != nil {
		plaintext, _, grantErr := s.grants.IssueInTx(ctx, tx, projectinterface.IssueGrantRequest{
			ProjectID: req.ProjectID, TaskID: req.TaskID, ContinuationID: continuation.ID,
			RuntimeConfigVersionID: config.ID, RuntimeProfileID: req.RuntimeProfileID,
			RuntimePluginID: req.RuntimeProvider, Runner: string(req.Runner), Access: req.GrantAccess,
		})
		if grantErr != nil {
			return ContinuationLaunch{}, fmt.Errorf("issue Continuation Interface capability: %w", grantErr)
		}
		token = plaintext
	}
	if s.fail != nil {
		if err := s.fail(ContinuityFailureBeforeCommit); err != nil {
			return ContinuationLaunch{}, err
		}
		if err := s.fail(ContinuityFailureBeforeWorkingSnapshotPublication); err != nil {
			return ContinuationLaunch{}, err
		}
	}
	workingPath := filepath.Join(s.runtimeRoot, req.TaskID, "workdir", ".pentest", "blackboard.json")
	previousBytes, previousErr := os.ReadFile(workingPath)
	previousExists := previousErr == nil
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return ContinuationLaunch{}, fmt.Errorf("read prior Working Snapshot before resume publication: %w", previousErr)
	}
	restoreWorkingSnapshot := func() {
		restorePriorWorkingSnapshotFile(s.runtimeRoot, req.TaskID, workingPath, previousBytes, previousExists)
	}
	if err := s.publishWorkingSnapshot(s.runtimeRoot, req.TaskID, projection.Bytes); err != nil {
		restoreWorkingSnapshot()
		return ContinuationLaunch{}, fmt.Errorf("publish initial Working Blackboard Snapshot: %w", err)
	}
	grantBound := false
	if req.BindGrant != nil {
		if err := req.BindGrant(token, continuation); err != nil {
			restoreWorkingSnapshot()
			return ContinuationLaunch{}, err
		}
		grantBound = true
	}
	if s.fail != nil {
		if err := s.fail(ContinuityFailureAfterBindGrant); err != nil {
			restoreWorkingSnapshot()
			if grantBound && req.UnbindGrant != nil {
				req.UnbindGrant()
			}
			return ContinuationLaunch{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		restoreWorkingSnapshot()
		if grantBound && req.UnbindGrant != nil {
			req.UnbindGrant()
		}
		return ContinuationLaunch{}, fmt.Errorf("commit atomic Blackboard v2 Continuation launch: %w", err)
	}
	return ContinuationLaunch{
		RuntimeConfig: config, Continuation: continuation, Token: token,
		Snapshot: append([]byte(nil), projection.Bytes...), Schema: snapshotSchema, Revision: projection.Snapshot.Revision,
	}, nil
}

// RebindContinuationForNativeSteer carries the Blackboard Continuation grant
// and Working Snapshot ownership across an interrupt_then_replace native steer.
// The in-container MCP capability token is minted once at launch and is
// immutable, so the replacement Continuation cannot receive a freshly minted
// grant; instead the still-open grant is rebound from the old Continuation to
// the replacement, and the immutable Launch Pin plus last-acknowledged Working
// Snapshot are carried so the replacement satisfies AuthorizeContinuationBinding
// and continuationOwnsCurrentWorkingPath. It runs in one transaction and must be
// invoked before the old Continuation is marked terminal, otherwise
// MarkContinuationTerminal would strand the grant on the completed Continuation.
func (s *ContinuityService) RebindContinuationForNativeSteer(ctx context.Context, oldContinuationID, nextContinuationID string) error {
	if strings.TrimSpace(oldContinuationID) == "" || strings.TrimSpace(nextContinuationID) == "" {
		return fmt.Errorf("native steer Blackboard rebind requires both Continuation identities")
	}
	if oldContinuationID == nextContinuationID {
		return fmt.Errorf("native steer Blackboard rebind requires distinct Continuations")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin native steer Blackboard rebind: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// Carry the immutable Launch Pin so the replacement owns a pinned Snapshot.
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO blackboard_v2_continuation_pins
		(continuation_id,snapshot_schema,snapshot_revision,snapshot_bytes,integrity_sha256,created_at)
		SELECT ?,snapshot_schema,snapshot_revision,snapshot_bytes,integrity_sha256,?
		FROM blackboard_v2_continuation_pins WHERE continuation_id=?`,
		nextContinuationID, now, oldContinuationID,
	); err != nil {
		return fmt.Errorf("carry Launch Blackboard Pin to replacement Continuation: %w", err)
	}
	// Carry the last-acknowledged Working Snapshot so reads and writes resolve.
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO blackboard_v2_continuation_state
		(continuation_id,last_acknowledged_revision,working_snapshot_bytes,updated_at)
		SELECT ?,last_acknowledged_revision,working_snapshot_bytes,?
		FROM blackboard_v2_continuation_state WHERE continuation_id=?`,
		nextContinuationID, now, oldContinuationID,
	); err != nil {
		return fmt.Errorf("carry Working Blackboard Snapshot to replacement Continuation: %w", err)
	}
	// Rebind the still-open grant from the old Continuation to the replacement,
	// clearing any terminal timestamp and re-deriving the bound actor identity.
	if _, err := tx.ExecContext(ctx, `
		UPDATE blackboard_continuation_grants
		   SET continuation_id=?, actor_id='runtime:' || runtime_plugin_id || ':' || ?, terminal_at=''
		 WHERE continuation_id=? AND finished_at='' AND revoked_at=''`,
		nextContinuationID, nextContinuationID, oldContinuationID,
	); err != nil {
		return fmt.Errorf("rebind Continuation Interface grant to replacement: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit native steer Blackboard rebind: %w", err)
	}
	return nil
}

type LaunchHeader struct {
	Runner         string
	ScopePath      string
	BlackboardPath string
	Schema         string
	Revision       int
}

func RenderLaunchHeader(header LaunchHeader) string {
	return "Runner: " + header.Runner +
		"\nScope: " + header.ScopePath +
		"\nBlackboard: " + header.BlackboardPath +
		"\nSchema: " + header.Schema +
		"\nRevision: " + strconv.Itoa(header.Revision)
}

func CodexChecklist() string {
	return strings.Join([]string{
		"1. Reread Scope and the Blackboard snapshot before planning, after context compaction, and after resume.",
		"2. Write semantic milestones only; commands, logs, and raw output stay outside the Blackboard.",
		"3. Write with Blackboard Keys and current versions, and reuse the same idempotency key for an uncertain retry.",
		"4. Exploration flows through an open Attempt, reusable outcome records, and a terminal Attempt.",
		"5. Blackboard scope labels never grant authorization, and Finish occurs only after every Attempt is terminal.",
	}, "\n")
}
