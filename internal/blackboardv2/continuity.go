package blackboardv2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"pentest/internal/projectinterface"
	"pentest/internal/store"
	"pentest/internal/task"
)

const snapshotEstimatorVersion = "utf8-bytes-div-4/v1"

var ErrLaunchPinIntegrity = errors.New("Launch Blackboard Pin integrity check failed")

type ContinuityFailurePoint string

const (
	ContinuityFailureBeforeCommit                     ContinuityFailurePoint = "before_commit"
	ContinuityFailureBeforeWorkingSnapshotPublication ContinuityFailurePoint = "before_working_snapshot_publication"
	// ContinuityFailureAfterBindGrant runs after BindGrant succeeds and before
	// the launch transaction commits. Injectors use it to prove grant-bearing
	// projection rolls back atomically with durable Continuation state.
	ContinuityFailureAfterBindGrant ContinuityFailurePoint = "after_bind_grant"
)

type ContinuityFailureInjector func(ContinuityFailurePoint) error

type ContinuationLaunchRequest struct {
	ProjectID        string
	TaskID           string
	RuntimeProfileID string
	RuntimeProvider  string
	Runner           task.Runner
	GrantAccess      projectinterface.GrantAccess
	RuntimeConfig    map[string]any
	SteeringEventIDs []string
	// Native session metadata is durable provider identity for Resume. A stale
	// container identity is deliberately not part of an atomic replacement launch.
	NativeSessionID   string
	NativeSessionPath string
	// Precommit runs before the launch transaction with staged Snapshot
	// metadata only (no Continuation grant). Codex uses this for full
	// projection; Claude/Pi use it for grant-less layout projection.
	Precommit func(ContinuationLaunchProjection) error
	// BindGrant runs after the Continuation grant plaintext is available
	// (empty when grants are disabled), after the Working Snapshot is
	// published, and still before the launch transaction commits. Failure
	// aborts the launch: durable rows roll back and the prior Working
	// Snapshot is restored. Callers that write grant-bearing config must
	// scrub it before returning an error from BindGrant, or via UnbindGrant
	// when the service aborts after a successful BindGrant.
	BindGrant func(plaintextGrant string, continuation task.TaskContinuation) error
	// UnbindGrant is invoked when BindGrant returned nil but the launch still
	// aborts before success (failure injection or commit failure). Optional.
	UnbindGrant func()
}

type ContinuationLaunchProjection struct {
	Schema   string
	Revision int
}

type ContinuationLaunch struct {
	RuntimeConfig task.RuntimeConfigVersion
	Continuation  task.TaskContinuation
	// Token is the one-time opaque Continuation Interface capability bound to
	// this Project/Task/Continuation. Callers must treat it as a secret.
	Token    string
	Snapshot []byte
	Schema   string
	Revision int
}

type LaunchPin struct {
	Schema   string
	Revision int
	Bytes    []byte
}

type WorkingSnapshotState struct {
	LastAcknowledgedRevision int
	Bytes                    []byte
}

// ContinuationSynchronizationState is the authenticated revision state an
// adapter observes before serving one trusted Continuation response.
type ContinuationSynchronizationState struct {
	FromRevision int
	Revision     int
	Pending      bool
}

// SynchronizationAttachment is the common optional trusted-response sibling.
type SynchronizationAttachment struct {
	Reason       string          `json:"reason"`
	FromRevision int             `json:"from_revision"`
	Revision     int             `json:"revision"`
	Snapshot     RuntimeSnapshot `json:"snapshot"`
}

type ActiveSnapshot struct {
	ContinuationID  string
	TaskID          string
	RuntimeProvider string
	Runner          task.Runner
	Schema          string
	Revision        int
}

type ContinuityService struct {
	db          *store.DB
	board       *Service
	tasks       *task.Service
	grants      *projectinterface.GrantStore
	runtimeRoot string
	fail        ContinuityFailureInjector
}

func NewContinuityService(db *store.DB, board *Service, tasks *task.Service, runtimeRoot string) *ContinuityService {
	board.runtimeRoot = runtimeRoot
	return &ContinuityService{
		db: db, board: board, tasks: tasks, runtimeRoot: runtimeRoot,
		grants: projectinterface.NewGrantStore(db, projectinterface.SystemClock{}, projectinterface.RandomIDSource{}, projectinterface.RandomTokenSource{}),
	}
}

func (s *ContinuityService) SetFailureInjector(injector ContinuityFailureInjector) {
	s.fail = injector
}

// ContinuationAuthority is the race-safe Project/Task/Continuation binding an
// offline or transport adapter must establish before service access.
type ContinuationAuthority struct {
	ProjectID      string
	TaskID         string
	ContinuationID string
	Status         string
	// Live is true when the Continuation is open and still owns the Task's
	// current Working Snapshot (active/latest). Closed or superseded
	// Continuations lose offline read, history, and synchronization authority.
	Live bool
	Sync ContinuationSynchronizationState
}

// AuthorizeContinuationBinding validates Project, Task, and Continuation
// identity before any offline Runtime mutation or read. When requireLive is
// true, closed or superseded Continuations are rejected before service access.
func (s *Service) AuthorizeContinuationBinding(ctx context.Context, projectID, taskID, continuationID string, requireLive bool) (ContinuationAuthority, error) {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(continuationID) == "" {
		return ContinuationAuthority{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	var boundTaskID, status string
	var number, acknowledged, revision, newer int
	err := s.db.QueryRowContext(ctx, `
		SELECT continuation.task_id,continuation.status,continuation.number,
		       state.last_acknowledged_revision,COALESCE(project_state.revision,0),
		       (SELECT COUNT(*) FROM task_continuations AS newer
		         WHERE newer.task_id=continuation.task_id AND newer.number>continuation.number)
		FROM task_continuations AS continuation
		JOIN tasks AS task ON task.id=continuation.task_id
		JOIN blackboard_v2_continuation_pins AS pin ON pin.continuation_id=continuation.id
		JOIN blackboard_v2_continuation_state AS state ON state.continuation_id=continuation.id
		LEFT JOIN blackboard_v2_project_state AS project_state ON project_state.project_id=task.project_id
		WHERE continuation.id=? AND task.project_id=?`, continuationID, projectID,
	).Scan(&boundTaskID, &status, &number, &acknowledged, &revision, &newer)
	if errors.Is(err, sql.ErrNoRows) {
		return ContinuationAuthority{}, semanticError("authority_denied", "trusted Continuation does not own this Project interface", "", nil)
	}
	if err != nil {
		return ContinuationAuthority{}, fmt.Errorf("authorize trusted Continuation binding: %w", err)
	}
	if taskID != "" && taskID != boundTaskID {
		return ContinuationAuthority{}, semanticError("authority_denied", "trusted Continuation does not own this Task interface", "", nil)
	}
	live := continuationCanWrite(status) && newer == 0
	if requireLive && !live {
		return ContinuationAuthority{}, semanticError("closed_continuation", "trusted Continuation is closed for offline Blackboard access", "", nil)
	}
	return ContinuationAuthority{
		ProjectID: projectID, TaskID: boundTaskID, ContinuationID: continuationID, Status: status, Live: live,
		Sync: ContinuationSynchronizationState{FromRevision: acknowledged, Revision: revision, Pending: live && revision > acknowledged},
	}, nil
}
