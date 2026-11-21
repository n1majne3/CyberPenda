package blackboardv2

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type WorkingSnapshot struct {
	Path     string `json:"path"`
	Revision int    `json:"revision"`
}

type RuntimeSnapshot struct {
	Schema    string              `json:"schema"`
	Semantics string              `json:"semantics"`
	Revision  int                 `json:"revision"`
	Work      SnapshotWork        `json:"work"`
	Knowledge SnapshotKnowledge   `json:"knowledge"`
	Relations []RelationshipTuple `json:"relations"`
}

type SnapshotWork struct {
	Objectives map[string]SnapshotObjective `json:"objectives,omitempty"`
	Attempts   map[string]SnapshotAttempt   `json:"attempts,omitempty"`
}

type SnapshotObjective struct {
	Version   int    `json:"version"`
	Status    string `json:"status"`
	Objective string `json:"objective"`
}

type SnapshotAttempt struct {
	Version int    `json:"version"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

type SnapshotKnowledge struct {
	Entities  map[string]SnapshotEntity   `json:"entities,omitempty"`
	Facts     map[string]SnapshotFact     `json:"facts,omitempty"`
	Findings  map[string]SnapshotFinding  `json:"findings,omitempty"`
	Solutions map[string]SnapshotSolution `json:"solutions,omitempty"`
	Evidence  map[string]SnapshotEvidence `json:"evidence,omitempty"`
}

type SnapshotSolution struct {
	Version             int    `json:"version"`
	Status              string `json:"status"`
	Kind                string `json:"kind"`
	Summary             string `json:"summary"`
	Value               string `json:"value,omitempty"`
	VerificationSummary string `json:"verification_summary,omitempty"`
}

type SnapshotEntity struct {
	Version       int    `json:"version"`
	Status        string `json:"status"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Locator       string `json:"locator,omitempty"`
	Description   string `json:"description,omitempty"`
	ScopeStatus   string `json:"scope_status,omitempty"`
	CredentialRef string `json:"credential_ref,omitempty"`
}

type SnapshotFact struct {
	Version     int    `json:"version"`
	Category    string `json:"category"`
	Summary     string `json:"summary"`
	Confidence  string `json:"confidence"`
	ScopeStatus string `json:"scope_status,omitempty"`
}

type SnapshotFinding struct {
	Version     int    `json:"version"`
	Status      string `json:"status"`
	Title       string `json:"title"`
	Target      string `json:"target,omitempty"`
	Description string `json:"description,omitempty"`
	Severity    string `json:"severity,omitempty"`
	CVSSPending bool   `json:"cvss_pending"`
}

type SnapshotEvidence struct {
	Version      int    `json:"version"`
	Status       string `json:"status"`
	ArtifactType string `json:"artifact_type"`
	Summary      string `json:"summary"`
	MediaType    string `json:"media_type,omitempty"`
	CapturedAt   string `json:"captured_at,omitempty"`
}

func (s *Service) RuntimeSnapshot(ctx context.Context, projectID string) (RuntimeSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RuntimeSnapshot{}, fmt.Errorf("begin Blackboard v2 snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return s.runtimeSnapshotTx(ctx, tx, projectID)
}

func (s *Service) runtimeSnapshotTx(ctx context.Context, tx *sql.Tx, projectID string) (RuntimeSnapshot, error) {
	return s.runtimeSnapshotForKeysTx(ctx, tx, projectID, nil)
}

// A nil key set reads the full graph; a non-nil set reads only changed records.
// Relationships still come from the same transaction and retain grammar checks.
func (s *Service) runtimeSnapshotForKeysTx(ctx context.Context, tx *sql.Tx, projectID string, keys []string) (RuntimeSnapshot, error) {
	if err := ensureProjectExists(ctx, tx, projectID); err != nil {
		return RuntimeSnapshot{}, err
	}
	revision, err := currentRevisionOrZero(ctx, tx, projectID)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	query := `SELECT key, type, version, record_json FROM blackboard_v2_records
 WHERE project_id=? AND type IN ('entity','objective','attempt','fact','finding','solution','evidence')`
	args := []any{projectID}
	if keys != nil {
		if len(keys) == 0 {
			query += ` AND 0`
		} else {
			query += ` AND key IN (` + strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",") + `)`
			for _, key := range keys {
				args = append(args, key)
			}
		}
	}
	rows, err := tx.QueryContext(ctx, query+` ORDER BY key ASC`, args...)
	if err != nil {
		return RuntimeSnapshot{}, fmt.Errorf("read Blackboard v2 snapshot records: %w", err)
	}
	defer rows.Close()

	entities := make(map[string]SnapshotEntity)
	objectives := make(map[string]SnapshotObjective)
	attempts := make(map[string]SnapshotAttempt)
	facts := make(map[string]SnapshotFact)
	findings := make(map[string]SnapshotFinding)
	solutions := make(map[string]SnapshotSolution)
	evidence := make(map[string]SnapshotEvidence)
	for rows.Next() {
		var key, typ, raw string
		var version int
		if err := rows.Scan(&key, &typ, &version, &raw); err != nil {
			return RuntimeSnapshot{}, fmt.Errorf("scan Blackboard v2 snapshot record: %w", err)
		}
		record, err := decodeStoredRecord(typ, raw)
		if err != nil {
			return RuntimeSnapshot{}, fmt.Errorf("decode Blackboard v2 snapshot record: %w", err)
		}
		switch typ {
		case "entity":
			entity := record.entityRecord()
			entities[key] = SnapshotEntity{
				Version:       version,
				Status:        entity.Status,
				Kind:          entity.Kind,
				Name:          entity.Name,
				Locator:       entity.Locator,
				Description:   entity.Description,
				ScopeStatus:   entity.ScopeStatus,
				CredentialRef: entity.CredentialRef,
			}
		case "objective":
			objective := record.objectiveRecord()
			objectives[key] = SnapshotObjective{Version: version, Status: objective.Status, Objective: objective.Objective}
		case "attempt":
			attempt := record.attemptRecord()
			attempts[key] = SnapshotAttempt{Version: version, Status: attempt.Status, Summary: attempt.Summary}
		case "fact":
			fact := record.factRecord()
			facts[key] = SnapshotFact{
				Version:     version,
				Category:    fact.Category,
				Summary:     fact.Summary,
				Confidence:  fact.Confidence,
				ScopeStatus: fact.ScopeStatus,
			}
		case "finding":
			finding := record.findingOutputRecord()
			findings[key] = SnapshotFinding{
				Version: version, Status: finding.Status, Title: finding.Title,
				Target: finding.Target, Description: finding.Description,
				Severity: finding.Severity, CVSSPending: finding.CVSSPending,
			}
		case "solution":
			solution := record.solutionRecord()
			solutions[key] = SnapshotSolution{Version: version, Status: solution.Status, Kind: solution.Kind, Summary: solution.Summary, Value: solution.Value, VerificationSummary: solution.VerificationSummary}
		case "evidence":
			item := record.evidenceRecord()
			evidence[key] = SnapshotEvidence{
				Version:      version,
				Status:       item.Status,
				ArtifactType: item.ArtifactType,
				Summary:      item.Summary,
				MediaType:    item.MediaType,
				CapturedAt:   item.CapturedAt,
			}
		}
	}
	if err := rows.Err(); err != nil {
		return RuntimeSnapshot{}, fmt.Errorf("iterate Blackboard v2 snapshot records: %w", err)
	}
	work := SnapshotWork{}
	if len(objectives) != 0 {
		work.Objectives = objectives
	}
	if len(attempts) != 0 {
		work.Attempts = attempts
	}
	knowledge := SnapshotKnowledge{}
	if len(entities) != 0 {
		knowledge.Entities = entities
	}
	if len(facts) != 0 {
		knowledge.Facts = facts
	}
	if len(findings) != 0 {
		knowledge.Findings = findings
	}
	if len(solutions) != 0 {
		knowledge.Solutions = solutions
	}
	if len(evidence) != 0 {
		knowledge.Evidence = evidence
	}
	relationships, err := loadAllCurrentRelationships(ctx, tx, projectID)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	return RuntimeSnapshot{
		Schema:    snapshotSchema,
		Semantics: snapshotSemantics,
		Revision:  revision,
		Work:      work,
		Knowledge: knowledge,
		Relations: relationships,
	}, nil
}
