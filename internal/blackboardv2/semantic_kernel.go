package blackboardv2

import (
	"context"
	"database/sql"
	"fmt"
)

// semanticChangeBackend isolates owner-specific persistence while the shared
// kernel owns operation dispatch, revision advancement, and changed-version
// accounting for both Project and Session Blackboards.
type semanticChangeBackend struct {
	prepare     func(context.Context, *sql.Tx, Change, int) (Change, error)
	create      func(context.Context, *sql.Tx, int, int, Change, string) (int, string, int, bool, error)
	update      func(context.Context, *sql.Tx, int, int, Change, string) (int, string, int, bool, error)
	relate      func(context.Context, *sql.Tx, int, int, Change, string) (int, RelationVersionTuple, bool, error)
	unrelate    func(context.Context, *sql.Tx, int, int, Change, string) (int, RelationVersionTuple, error)
	transition  func(context.Context, *sql.Tx, int, int, Change, string) (int, string, int, bool, error)
	supersede   func(context.Context, *sql.Tx, int, int, Change, map[string]bool, string) (int, string, int, RelationVersionTuple, bool, error)
	merge       func(context.Context, *sql.Tx, int, int, Change, string) (int, string, int, []RelationVersionTuple, error)
	after       func(context.Context, *sql.Tx, int, Change, semanticChangeOutcome) error
	unsupported string
}

type semanticChangeOutcome struct {
	Changed bool
	Key     string
	Version int
	Tuples  []RelationVersionTuple
}

type semanticChangeSetResult struct {
	Revision         int
	ChangedRecords   map[string]int
	ChangedRelations map[string]RelationVersionTuple
	Created          map[string]bool
	TerminalAttempts map[string]terminalAttemptValidation
}

func applySemanticChangeSet(ctx context.Context, tx *sql.Tx, revision int, changes []Change, now string, backend semanticChangeBackend) (semanticChangeSetResult, error) {
	result := semanticChangeSetResult{
		Revision: revision, ChangedRecords: map[string]int{}, ChangedRelations: map[string]RelationVersionTuple{},
		Created: map[string]bool{}, TerminalAttempts: map[string]terminalAttemptValidation{},
	}
	for index, original := range changes {
		change := original
		var err error
		if backend.prepare != nil {
			change, err = backend.prepare(ctx, tx, change, index)
			if err != nil {
				return semanticChangeSetResult{}, err
			}
		}
		outcome := semanticChangeOutcome{}
		switch change.Op {
		case "create":
			if backend.create == nil {
				err = missingSemanticOperationBackend(change.Op, index, backend.unsupported)
				break
			}
			result.Revision, outcome.Key, outcome.Version, outcome.Changed, err = backend.create(ctx, tx, result.Revision, index, change, now)
			if outcome.Changed {
				result.ChangedRecords[outcome.Key], result.Created[outcome.Key] = outcome.Version, true
			}
		case "update":
			if backend.update == nil {
				err = missingSemanticOperationBackend(change.Op, index, backend.unsupported)
				break
			}
			result.Revision, outcome.Key, outcome.Version, outcome.Changed, err = backend.update(ctx, tx, result.Revision, index, change, now)
			if outcome.Changed {
				result.ChangedRecords[outcome.Key] = outcome.Version
			}
		case "relate":
			if backend.relate == nil {
				err = missingSemanticOperationBackend(change.Op, index, backend.unsupported)
				break
			}
			var tuple RelationVersionTuple
			result.Revision, tuple, outcome.Changed, err = backend.relate(ctx, tx, result.Revision, index, change, now)
			outcome.Tuples = []RelationVersionTuple{tuple}
			if outcome.Changed {
				result.ChangedRelations[relationKey(tuple)] = tuple
			}
		case "unrelate":
			if backend.unrelate == nil {
				err = missingSemanticOperationBackend(change.Op, index, backend.unsupported)
				break
			}
			var tuple RelationVersionTuple
			result.Revision, tuple, err = backend.unrelate(ctx, tx, result.Revision, index, change, now)
			outcome.Changed, outcome.Tuples = err == nil, []RelationVersionTuple{tuple}
			if err == nil {
				result.ChangedRelations[relationKey(tuple)] = tuple
			}
		case "transition":
			if backend.transition == nil {
				err = missingSemanticOperationBackend(change.Op, index, backend.unsupported)
				break
			}
			result.Revision, outcome.Key, outcome.Version, outcome.Changed, err = backend.transition(ctx, tx, result.Revision, index, change, now)
			if outcome.Changed {
				result.ChangedRecords[outcome.Key] = outcome.Version
				if isOneOf(change.Status, "succeeded", "failed", "blocked", "inconclusive") {
					result.TerminalAttempts[outcome.Key] = terminalAttemptValidation{status: change.Status, path: fmt.Sprintf("changes[%d].status", index)}
				}
			}
		case "supersede":
			if backend.supersede == nil {
				err = missingSemanticOperationBackend(change.Op, index, backend.unsupported)
				break
			}
			var tuple RelationVersionTuple
			result.Revision, outcome.Key, outcome.Version, tuple, outcome.Changed, err = backend.supersede(ctx, tx, result.Revision, index, change, result.Created, now)
			outcome.Tuples = []RelationVersionTuple{tuple}
			if outcome.Changed {
				result.ChangedRecords[outcome.Key], result.ChangedRelations[relationKey(tuple)] = outcome.Version, tuple
			}
		case "merge":
			if backend.merge == nil {
				err = missingSemanticOperationBackend(change.Op, index, backend.unsupported)
				break
			}
			result.Revision, outcome.Key, outcome.Version, outcome.Tuples, err = backend.merge(ctx, tx, result.Revision, index, change, now)
			outcome.Changed = err == nil
			if err == nil {
				result.ChangedRecords[outcome.Key] = outcome.Version
				for _, tuple := range outcome.Tuples {
					result.ChangedRelations[relationKey(tuple)] = tuple
				}
			}
		default:
			err = semanticError("semantic_validation", backend.unsupported, fmt.Sprintf("changes[%d].op", index), nil)
		}
		if err != nil {
			return semanticChangeSetResult{}, err
		}
		if backend.after != nil {
			if err := backend.after(ctx, tx, index, change, outcome); err != nil {
				return semanticChangeSetResult{}, err
			}
		}
	}
	return result, nil
}

func missingSemanticOperationBackend(_ string, index int, message string) error {
	return semanticError("semantic_validation", message, fmt.Sprintf("changes[%d].op", index), nil)
}
