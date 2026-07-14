// Package blackboardcompat translates legacy Blackboard calls into the
// canonical graph, project-interface, read, report, and Task modules.
package blackboardcompat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"pentest/internal/blackboard"
	"pentest/internal/projectinterface"
	"pentest/internal/store"
	"pentest/internal/task"
)

// Transport identifies the legacy adapter crossing the compatibility seam.
// It is metadata only and never changes semantic request hashing.
type Transport string

const (
	TransportHTTP Transport = "http"
	TransportMCP  Transport = "mcp"
	TransportCLI  Transport = "cli"
)

const (
	ErrCodeLegacyRelationNotGraphRepresentable = "legacy_relation_not_graph_representable"
	ErrCodeCompatibilityAttemptRequired        = "compatibility_attempt_required"
	ErrCodeCompatibilityRemoved                = "compatibility_removed"
)

// CallKind is the closed legacy_blackboard_v1 operation union.
type CallKind string

const (
	CallUpsertFact      CallKind = "upsert_fact"
	CallDeprecateFact   CallKind = "deprecate_fact"
	CallMergeFacts      CallKind = "merge_facts"
	CallPutFactRelation CallKind = "put_fact_relation"
	CallUpsertFinding   CallKind = "upsert_finding"
	CallMergeFindings   CallKind = "merge_findings"
	CallAttachEvidence  CallKind = "attach_evidence"
	CallGenerateReport  CallKind = "generate_report"
	CallPutTaskSummary  CallKind = "put_task_summary"
	CallReadFact        CallKind = "read_fact"
	CallReadFinding     CallKind = "read_finding"
	CallReadEvidence    CallKind = "read_evidence"
	CallReadTaskSummary CallKind = "read_task_summary"
)

type FactWrite struct {
	FactKey     string `json:"fact_key"`
	Category    string `json:"category"`
	Summary     string `json:"summary"`
	Body        string `json:"body"`
	Confidence  string `json:"confidence"`
	ScopeStatus string `json:"scope_status"`
}

type FactRelationWrite struct {
	SourceFactKey string `json:"source_fact_key"`
	TargetFactKey string `json:"target_fact_key"`
	Relation      string `json:"relation"`
	Summary       string `json:"summary"`
}

type FindingWrite struct {
	FindingKey     string `json:"finding_key"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	Target         string `json:"target"`
	Proof          string `json:"proof"`
	Impact         string `json:"impact"`
	Recommendation string `json:"recommendation"`
	CVSSVersion    string `json:"cvss_version"`
	CVSSVector     string `json:"cvss_vector"`
}

type EvidenceWrite struct {
	EvidenceKey       string             `json:"evidence_key"`
	AttachToType      string             `json:"attach_to_type"`
	AttachToKey       string             `json:"attach_to_key"`
	ArtifactType      string             `json:"artifact_type"`
	SourcePath        string             `json:"source_path"`
	Summary           string             `json:"summary"`
	ProducedByAttempt blackboard.NodeRef `json:"produced_by_attempt,omitempty"`
}

type TaskSummaryWrite struct {
	TaskID      string `json:"task_id"`
	Summary     string `json:"summary"`
	SubmittedBy string `json:"submitted_by"`
}

type ReportWrite struct {
	TaskID string `json:"task_id,omitempty"`
}

type MergeWrite struct {
	SourceKey    string `json:"source_key"`
	CanonicalKey string `json:"canonical_key"`
}

type MergeResult struct {
	Merged bool `json:"merged"`
}

type LegacyCall struct {
	Kind            CallKind
	Transport       Transport
	ProjectID       string
	Principal       projectinterface.Principal
	IdempotencyKey  string
	ExpectedVersion *int
	Fact            *FactWrite
	Relation        *FactRelationWrite
	FactMerge       *MergeWrite
	Finding         *FindingWrite
	FindingMerge    *MergeWrite
	Evidence        *EvidenceWrite
	TaskSummary     *TaskSummaryWrite
	Report          *ReportWrite
}

type LegacyResult struct {
	Payload  any
	Mutation blackboard.MutationResult
}

type UseMode string

const (
	UseModeRead  UseMode = "read"
	UseModeWrite UseMode = "write"
)

// Use is intentionally payload-free retirement telemetry.
type Use struct {
	ProjectID string
	Transport Transport
	Kind      CallKind
	Mode      UseMode
}

type UseCounter interface {
	Increment(context.Context, Use) error
}

// WriteRetirementPolicy records the release-owned evidence that cannot be
// derived from one local store. Live store gates are evaluated by Call.
type WriteRetirementPolicy struct {
	GraphNativeStableReleases int
	BundledRuntimeV1Only      bool
	ReplacementDocsReady      bool
	ObservationWaiver         *ObservationWaiver
}

// ObservationWaiver is an explicit operator decision to bypass only the
// 30-day local compatibility-use observation period.
type ObservationWaiver struct {
	OperatorID string
	Reason     string
}

// ReleaseCWriteRetirementPolicy is the evidence shipped by the Release C
// binary. Local observation, Continuation, verification, Health, and guard
// gates are still evaluated from the opened store.
func ReleaseCWriteRetirementPolicy() *WriteRetirementPolicy {
	return &WriteRetirementPolicy{
		GraphNativeStableReleases: 2,
		BundledRuntimeV1Only:      true,
		ReplacementDocsReady:      true,
	}
}

type Deps struct {
	DB               *store.DB
	Graph            *blackboard.GraphService
	Reads            *blackboard.BlackboardReadService
	ProjectInterface *projectinterface.Service
	Tasks            *task.Service
	UseCounter       UseCounter
	WriteRetirement  *WriteRetirementPolicy
	Clock            func() time.Time
	// AfterResultMiss is a stable failure-point seam used by concurrency and
	// crash tests. Production leaves it nil.
	AfterResultMiss func()
}

type Service struct {
	deps       Deps
	useCounter UseCounter
	clock      func() time.Time
	missMu     sync.Mutex
}

func NewService(deps Deps) *Service {
	counter := deps.UseCounter
	if counter == nil && deps.DB != nil {
		counter = sqliteUseCounter{db: deps.DB}
	}
	clock := deps.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Service{deps: deps, useCounter: counter, clock: clock}
}

func (s *Service) RecordUse(ctx context.Context, use Use) error {
	if s == nil || s.useCounter == nil {
		return nil
	}
	if err := s.useCounter.Increment(ctx, use); err != nil {
		return projectinterface.InternalError("record compatibility use: " + err.Error())
	}
	return nil
}

func ReadCallKind(kind blackboard.ReadKind) CallKind {
	switch kind {
	case blackboard.ReadKindLegacyFactIndexV1, blackboard.ReadKindLegacyFactDetailV1,
		blackboard.ReadKindLegacyFactVersionsV1, blackboard.ReadKindLegacyFactRelationsV1:
		return CallReadFact
	case blackboard.ReadKindLegacyFindingCollectionV1, blackboard.ReadKindLegacyFindingVersionsV1:
		return CallReadFinding
	case blackboard.ReadKindLegacyEvidenceCollectionV1:
		return CallReadEvidence
	default:
		return ""
	}
}

func (s *Service) Call(ctx context.Context, call LegacyCall) (LegacyResult, error) {
	if s == nil || s.deps.DB == nil || s.deps.Graph == nil || s.deps.Reads == nil || s.deps.ProjectInterface == nil {
		return LegacyResult{}, projectinterface.InternalError("Blackboard compatibility service is unavailable")
	}
	if call.ProjectID == "" || call.ProjectID != call.Principal.ProjectID {
		return LegacyResult{}, projectinterface.ValidationError(projectinterface.ErrCodeProjectMismatch, "legacy call Project does not match trusted principal", "project_id")
	}
	if err := s.rejectRetiredWrite(ctx, call); err != nil {
		return LegacyResult{}, err
	}
	if strings.TrimSpace(call.IdempotencyKey) == "" {
		key, err := newBestEffortKey()
		if err != nil {
			return LegacyResult{}, projectinterface.InternalError("create best-effort compatibility key: " + err.Error())
		}
		call.IdempotencyKey = key
	}
	if err := s.RecordUse(ctx, Use{ProjectID: call.ProjectID, Transport: call.Transport, Kind: call.Kind, Mode: useModeFor(call.Kind)}); err != nil {
		return LegacyResult{}, err
	}
	requestHash, err := legacyRequestHash(call)
	if err != nil {
		return LegacyResult{}, projectinterface.InternalError("encode compatibility request: " + err.Error())
	}
	stored, err := s.loadStoredResult(ctx, call, requestHash)
	if err != nil || stored != nil {
		if stored != nil {
			return *stored, nil
		}
		return LegacyResult{}, err
	}
	if s.deps.AfterResultMiss != nil {
		s.deps.AfterResultMiss()
	}
	s.missMu.Lock()
	defer s.missMu.Unlock()
	stored, err = s.loadStoredResult(ctx, call, requestHash)
	if err != nil || stored != nil {
		if stored != nil {
			return *stored, nil
		}
		return LegacyResult{}, err
	}
	if call.Kind == CallAttachEvidence {
		return s.callAndStore(ctx, call, requestHash, s.callEvidence)
	}
	if call.Kind == CallPutTaskSummary {
		return s.callAndStore(ctx, call, requestHash, s.callTaskSummary)
	}
	if call.Kind == CallGenerateReport {
		return s.callAndStore(ctx, call, requestHash, s.callReport)
	}
	translated, err := s.loadTranslatedRequest(ctx, call, requestHash)
	if err != nil {
		return LegacyResult{}, err
	}
	if translated == nil {
		translated, err = s.translateGraphCall(ctx, call)
		if err != nil {
			return LegacyResult{}, err
		}
		if err := s.storeTranslatedRequest(ctx, call, requestHash, *translated); err != nil {
			return LegacyResult{}, err
		}
		translated, err = s.loadTranslatedRequest(ctx, call, requestHash)
		if err != nil {
			return LegacyResult{}, err
		}
	}

	response, err := s.deps.ProjectInterface.Apply(ctx, call.Principal, *translated)
	if err != nil {
		return LegacyResult{}, err
	}
	atRevision := response.Result.GraphRevision
	payload, err := s.projectPayload(ctx, call, &atRevision)
	if err != nil {
		return LegacyResult{}, err
	}
	result := LegacyResult{Payload: payload, Mutation: response.Result}
	if err := s.storeResult(ctx, call, requestHash, result); err != nil {
		return LegacyResult{}, err
	}
	return result, nil
}

const writeObservationPeriod = 30 * 24 * time.Hour

func (s *Service) rejectRetiredWrite(ctx context.Context, call LegacyCall) error {
	if !isRetirableWrite(call) {
		return nil
	}
	var retired int
	err := s.deps.DB.QueryRowContext(ctx, `SELECT 1 FROM blackboard_compatibility_write_retirement WHERE id=1`).Scan(&retired)
	if err == nil {
		return compatibilityRemovedError(call)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return projectinterface.InternalError("read compatibility-write retirement: " + err.Error())
	}
	policy := s.deps.WriteRetirement
	if policy == nil || policy.GraphNativeStableReleases < 2 || !policy.BundledRuntimeV1Only || !policy.ReplacementDocsReady {
		return nil
	}
	eligible, waived, err := s.localWriteRetirementGatesPass(ctx, *policy)
	if err != nil {
		return projectinterface.InternalError("evaluate compatibility-write retirement: " + err.Error())
	}
	if !eligible {
		return nil
	}
	now := s.clock().UTC().Format(time.RFC3339Nano)
	waiverOperator, waiverReason := "", ""
	if waived {
		waiverOperator = strings.TrimSpace(policy.ObservationWaiver.OperatorID)
		waiverReason = strings.TrimSpace(policy.ObservationWaiver.Reason)
	}
	_, err = s.deps.DB.ExecContext(ctx, `
		INSERT OR IGNORE INTO blackboard_compatibility_write_retirement (
			id,retired_at,graph_native_stable_releases,bundled_runtime_v1_only,replacement_docs_ready,
			observation_waived,waiver_operator_id,waiver_reason
		) VALUES (1,?,?,?,?,?,?,?)`, now, policy.GraphNativeStableReleases, true, true, waived, waiverOperator, waiverReason)
	if err != nil {
		return projectinterface.InternalError("record compatibility-write retirement: " + err.Error())
	}
	return compatibilityRemovedError(call)
}

func (s *Service) localWriteRetirementGatesPass(ctx context.Context, policy WriteRetirementPolicy) (bool, bool, error) {
	var epoch, cutoverState, cutoverCommittedAt, verificationHash string
	if err := s.deps.DB.QueryRowContext(ctx, `
		SELECT canonical_store,cutover_state,cutover_committed_at,latest_verification_result_hash
		FROM blackboard_store_state WHERE id=1`).Scan(&epoch, &cutoverState, &cutoverCommittedAt, &verificationHash); err != nil {
		return false, false, err
	}
	if epoch != store.CanonicalStoreGraphV1 || cutoverState != "graph" || cutoverCommittedAt == "" || verificationHash == "" {
		return false, false, nil
	}
	guardsIntact, err := s.legacyWriteGuardsIntact(ctx)
	if err != nil || !guardsIntact {
		return false, false, err
	}

	var activePreCutover int
	if err := s.deps.DB.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM task_continuations
			WHERE status IN ('pending','running','paused')
			  AND COALESCE(blackboard_renderer_version,'')=''
		)`).Scan(&activePreCutover); err != nil || activePreCutover != 0 {
		return false, false, err
	}

	var unhealthyProjects int
	if err := s.deps.DB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM projects p
		WHERE NOT EXISTS (
			SELECT 1 FROM blackboard_health_runs r
			WHERE r.project_id=p.id AND r.run_status='completed'
			  AND r.run_id=(
				SELECT latest.run_id FROM blackboard_health_runs latest
				WHERE latest.project_id=p.id
				ORDER BY latest.started_at DESC,latest.rowid DESC LIMIT 1
			  )
			  AND NOT EXISTS (
				SELECT 1 FROM blackboard_health_results result
				WHERE result.project_id=r.project_id AND result.run_id=r.run_id AND result.severity='critical'
			  )
		)`).Scan(&unhealthyProjects); err != nil || unhealthyProjects != 0 {
		return false, false, err
	}

	waived := validObservationWaiver(policy.ObservationWaiver)
	if waived {
		return true, true, nil
	}
	cutoverTime, err := time.Parse(time.RFC3339Nano, cutoverCommittedAt)
	if err != nil {
		return false, false, err
	}
	observationStart := s.clock().UTC().Add(-writeObservationPeriod)
	if cutoverTime.After(observationStart) {
		return false, false, nil
	}
	var recentWrites int
	if err := s.deps.DB.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM blackboard_compatibility_use
			WHERE use_mode='write' AND last_used_at>?
		)`, observationStart.Format(time.RFC3339Nano)).Scan(&recentWrites); err != nil {
		return false, false, err
	}
	return recentWrites == 0, false, nil
}

func (s *Service) legacyWriteGuardsIntact(ctx context.Context) (bool, error) {
	tables := []string{
		"project_facts", "project_fact_versions", "project_fact_relations", "fact_key_aliases",
		"findings", "finding_versions", "finding_key_aliases", "evidence_artifacts",
	}
	for _, table := range tables {
		for _, operation := range []string{"insert", "update", "delete"} {
			trigger := "blackboard_legacy_" + table + "_" + operation + "_guard"
			var sqlText string
			err := s.deps.DB.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&sqlText)
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if !strings.Contains(sqlText, table+" is frozen after graph_v1 cutover") {
				return false, nil
			}
		}
	}
	return true, nil
}

func validObservationWaiver(waiver *ObservationWaiver) bool {
	return waiver != nil && strings.TrimSpace(waiver.OperatorID) != "" && strings.TrimSpace(waiver.Reason) != ""
}

func isRetirableWrite(call LegacyCall) bool {
	switch call.Kind {
	case CallUpsertFact, CallDeprecateFact, CallMergeFacts, CallPutFactRelation,
		CallUpsertFinding, CallMergeFindings, CallAttachEvidence:
		return true
	case CallPutTaskSummary:
		return call.Principal.ActorType == blackboard.ActorTypeRuntime
	default:
		return false
	}
}

func compatibilityRemovedError(call LegacyCall) *projectinterface.Error {
	replacement := "blackboard apply"
	if call.Kind == CallAttachEvidence {
		replacement = "blackboard evidence retain"
	} else if call.Kind == CallPutTaskSummary {
		replacement = "blackboard continuation finish"
	}
	err := projectinterface.ValidationError(ErrCodeCompatibilityRemoved,
		"Legacy Blackboard compatibility writes were removed; use "+replacement, "kind")
	err.Details = map[string]any{"replacement_operation": replacement}
	return err
}

type compatibilityCall func(context.Context, LegacyCall) (LegacyResult, error)

func (s *Service) callAndStore(ctx context.Context, call LegacyCall, requestHash string, fn compatibilityCall) (LegacyResult, error) {
	result, err := fn(ctx, call)
	if err != nil {
		return LegacyResult{}, err
	}
	if err := s.storeResult(ctx, call, requestHash, result); err != nil {
		return LegacyResult{}, err
	}
	return result, nil
}

type sqliteUseCounter struct {
	db *store.DB
}

func (counter sqliteUseCounter) Increment(ctx context.Context, use Use) error {
	_, err := counter.db.ExecContext(ctx, `
		INSERT INTO blackboard_compatibility_use (
			project_id,transport,call_kind,use_mode,use_count,last_used_at
		) VALUES (?,?,?,?,1,?)
		ON CONFLICT(project_id,transport,call_kind,use_mode) DO UPDATE SET
			use_count=use_count+1,last_used_at=excluded.last_used_at`,
		use.ProjectID, string(use.Transport), string(use.Kind), string(use.Mode), time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func useModeFor(kind CallKind) UseMode {
	if kind == CallGenerateReport {
		return UseModeRead
	}
	return UseModeWrite
}

func newBestEffortKey() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "best-effort:" + hex.EncodeToString(value[:]), nil
}

func newCompatibilityID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (s *Service) callReport(ctx context.Context, call LegacyCall) (LegacyResult, error) {
	if call.Report == nil {
		return LegacyResult{}, projectinterface.ValidationError(projectinterface.ErrCodeInvalidRequest, "Report request is required", "report")
	}
	scopeContext := "current"
	if strings.TrimSpace(call.Report.TaskID) != "" {
		scopeContext = "task:" + call.Report.TaskID
	}
	include := true
	envelope, err := s.deps.Reads.Read(ctx, blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       call.ProjectID,
		Kind:            blackboard.ReadKindPentestReportV1,
		PentestReport: &blackboard.PentestReportRequest{
			Format: "markdown", ScopeContext: scopeContext,
			IncludeUnconfirmed: &include, IncludeTentativeFacts: &include,
		},
	})
	if err != nil {
		return LegacyResult{}, err
	}
	markdown, ok := envelope.Result.(blackboard.ReportMarkdownV1)
	if !ok {
		return LegacyResult{}, projectinterface.InternalError("Pentest report projection returned an unexpected shape")
	}
	return LegacyResult{Payload: blackboard.LegacyReportEnvelopeV1{
		Status: "generated", Format: "markdown", Markdown: markdown.Markdown,
	}}, nil
}

func (s *Service) callTaskSummary(ctx context.Context, call LegacyCall) (LegacyResult, error) {
	if call.TaskSummary == nil || strings.TrimSpace(call.TaskSummary.TaskID) == "" {
		return LegacyResult{}, projectinterface.ValidationError(projectinterface.ErrCodeInvalidRequest, "Task Summary request and task_id are required", "task_summary")
	}
	if s.deps.Tasks == nil {
		return LegacyResult{}, projectinterface.InternalError("Task service is unavailable")
	}
	storedTask, err := s.deps.Tasks.Get(call.TaskSummary.TaskID)
	if err != nil || storedTask.ProjectID != call.ProjectID {
		return LegacyResult{}, projectinterface.ValidationError(projectinterface.ErrCodeProjectNotFound, "Task does not belong to the trusted Project", "task_summary.task_id")
	}
	if call.Principal.ActorType == blackboard.ActorTypeRuntime {
		if call.Principal.Grant.TaskID != call.TaskSummary.TaskID {
			return LegacyResult{}, projectinterface.ValidationError(projectinterface.ErrCodeProjectMismatch, "Task does not match the Continuation Interface Grant", "task_summary.task_id")
		}
		finished, err := s.deps.ProjectInterface.FinishContinuation(ctx, call.Principal, projectinterface.FinishContinuationRequest{
			ProtocolVersion: projectinterface.RuntimeProtocolVersion,
			IdempotencyKey:  graphIdempotencyKey(call),
			Summary:         call.TaskSummary.Summary,
		})
		if err != nil {
			return LegacyResult{}, err
		}
		return LegacyResult{Payload: finished.Result.SummaryVersion}, nil
	}
	submittedBy := strings.TrimSpace(call.TaskSummary.SubmittedBy)
	if submittedBy == "" {
		submittedBy = call.Principal.ActorID
	}
	version, err := s.putOperatorTaskSummary(ctx, call, submittedBy)
	if err != nil {
		return LegacyResult{}, err
	}
	return LegacyResult{Payload: version}, nil
}

func (s *Service) putOperatorTaskSummary(ctx context.Context, call LegacyCall, submittedBy string) (task.SummaryVersion, error) {
	if call.TaskSummary.Summary == "" {
		return task.SummaryVersion{}, projectinterface.ValidationError(projectinterface.ErrCodeInvalidRequest, "Task Summary is required", "task_summary.summary")
	}
	requestHash, err := legacyRequestHash(call)
	if err != nil {
		return task.SummaryVersion{}, projectinterface.InternalError("hash operator Task Summary: " + err.Error())
	}
	tx, err := s.deps.DB.BeginTx(ctx, nil)
	if err != nil {
		return task.SummaryVersion{}, projectinterface.InternalError("begin operator Task Summary: " + err.Error())
	}
	defer func() { _ = tx.Rollback() }()
	var storedHash, storedJSON string
	err = tx.QueryRowContext(ctx, `SELECT request_hash,result_json
		FROM blackboard_compatibility_task_summaries
		WHERE project_id=? AND idempotency_scope=? AND idempotency_key=?`,
		call.ProjectID, idempotencyScope(call.Principal), call.IdempotencyKey).Scan(&storedHash, &storedJSON)
	if err == nil {
		if storedHash != requestHash {
			return task.SummaryVersion{}, projectinterface.ValidationError(blackboard.ErrCodeIdempotencyConflict, "compatibility idempotency key was reused with a different payload", "idempotency_key")
		}
		var stored task.SummaryVersion
		if err := json.Unmarshal([]byte(storedJSON), &stored); err != nil {
			return task.SummaryVersion{}, projectinterface.InternalError("decode operator Task Summary replay: " + err.Error())
		}
		return stored, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return task.SummaryVersion{}, projectinterface.InternalError("load operator Task Summary replay: " + err.Error())
	}
	var maxVersion sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(version) FROM task_summary_versions WHERE task_id=?`, call.TaskSummary.TaskID).Scan(&maxVersion); err != nil {
		return task.SummaryVersion{}, projectinterface.InternalError("read operator Task Summary version: " + err.Error())
	}
	id, err := newCompatibilityID()
	if err != nil {
		return task.SummaryVersion{}, projectinterface.InternalError("create operator Task Summary id: " + err.Error())
	}
	now := time.Now().UTC()
	version := task.SummaryVersion{
		ID: id, TaskID: call.TaskSummary.TaskID, Version: int(maxVersion.Int64) + 1,
		Summary: call.TaskSummary.Summary, SubmittedBy: submittedBy, CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_summary_versions
		(id,task_id,version,summary,submitted_by,created_at) VALUES (?,?,?,?,?,?)`,
		version.ID, version.TaskID, version.Version, version.Summary, version.SubmittedBy, now.Format(time.RFC3339Nano)); err != nil {
		return task.SummaryVersion{}, projectinterface.InternalError("store operator Task Summary: " + err.Error())
	}
	resultJSON, err := json.Marshal(version)
	if err != nil {
		return task.SummaryVersion{}, projectinterface.InternalError("encode operator Task Summary: " + err.Error())
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO blackboard_compatibility_task_summaries
		(project_id,idempotency_scope,idempotency_key,request_hash,task_id,result_json,created_at)
		VALUES (?,?,?,?,?,?,?)`, call.ProjectID, idempotencyScope(call.Principal), call.IdempotencyKey,
		requestHash, call.TaskSummary.TaskID, string(resultJSON), now.Format(time.RFC3339Nano)); err != nil {
		return task.SummaryVersion{}, projectinterface.InternalError("store operator Task Summary replay: " + err.Error())
	}
	if err := tx.Commit(); err != nil {
		return task.SummaryVersion{}, projectinterface.InternalError("commit operator Task Summary: " + err.Error())
	}
	return version, nil
}

func (s *Service) callEvidence(ctx context.Context, call LegacyCall) (LegacyResult, error) {
	if call.Evidence == nil {
		return LegacyResult{}, projectinterface.ValidationError(projectinterface.ErrCodeInvalidRequest, "Evidence request is required", "evidence")
	}
	evidence := call.Evidence
	if call.Principal.ActorType == blackboard.ActorTypeRuntime && evidence.ProducedByAttempt.ID == "" &&
		(evidence.ProducedByAttempt.NodeType != blackboard.NodeTypeAttempt || strings.TrimSpace(evidence.ProducedByAttempt.StableKey) == "") {
		return LegacyResult{}, projectinterface.ValidationError(
			ErrCodeCompatibilityAttemptRequired,
			"Runtime Evidence compatibility requires a matching produced_by_attempt",
			"evidence.produced_by_attempt",
		)
	}
	var targetType blackboard.NodeType
	switch evidence.AttachToType {
	case "fact":
		targetType = blackboard.NodeTypeProjectFact
	case "finding":
		targetType = blackboard.NodeTypeFinding
	default:
		return LegacyResult{}, projectinterface.ValidationError(projectinterface.ErrCodeInvalidRequest, "Evidence target must be fact or finding", "evidence.attach_to_type")
	}
	retained, err := s.deps.ProjectInterface.RetainEvidence(ctx, call.Principal, projectinterface.RetainEvidenceRequest{
		ProtocolVersion:   projectinterface.RuntimeProtocolVersion,
		IdempotencyKey:    graphIdempotencyKey(call),
		StableKey:         evidence.EvidenceKey,
		ExpectedVersion:   call.ExpectedVersion,
		ArtifactType:      evidence.ArtifactType,
		SourcePath:        evidence.SourcePath,
		Summary:           evidence.Summary,
		ProducedByAttempt: evidence.ProducedByAttempt,
		Links: []projectinterface.EvidenceLink{{
			EdgeType: blackboard.EdgeTypeEvidences,
			To:       blackboard.NodeRef{NodeType: targetType, StableKey: evidence.AttachToKey},
		}},
	})
	if err != nil {
		return LegacyResult{}, err
	}
	envelope, err := s.deps.Reads.Read(ctx, blackboard.ReadRequest{
		ProtocolVersion:          blackboard.BlackboardReadProtocolVersion,
		ProjectID:                call.ProjectID,
		AtRevision:               &retained.Result.Mutation.GraphRevision,
		Kind:                     blackboard.ReadKindLegacyEvidenceCollectionV1,
		LegacyEvidenceCollection: &blackboard.LegacyEvidenceCollectionRequest{},
	})
	if err != nil {
		return LegacyResult{}, err
	}
	artifacts, ok := envelope.Result.(blackboard.LegacyEvidenceCollectionV1)
	if !ok {
		return LegacyResult{}, projectinterface.InternalError("legacy Evidence projection returned an unexpected shape")
	}
	for _, artifact := range artifacts.Evidence {
		if artifact.EvidenceKey == evidence.EvidenceKey {
			return LegacyResult{Payload: artifact, Mutation: retained.Result.Mutation}, nil
		}
	}
	return LegacyResult{}, projectinterface.InternalError("retained legacy Evidence was not readable")
}

func (s *Service) translateGraphCall(ctx context.Context, call LegacyCall) (*projectinterface.ApplyMutationRequest, error) {
	switch call.Kind {
	case CallUpsertFact:
		if call.Fact == nil {
			break
		}
		return s.translateFact(ctx, call)
	case CallDeprecateFact:
		if call.Fact == nil {
			break
		}
		return s.translateFactDeprecation(ctx, call)
	case CallPutFactRelation:
		if call.Relation == nil {
			break
		}
		return s.translateFactRelation(ctx, call)
	case CallMergeFacts:
		if call.FactMerge == nil {
			break
		}
		return s.translateMerge(ctx, call, blackboard.NodeTypeProjectFact, call.FactMerge)
	case CallUpsertFinding:
		if call.Finding == nil {
			break
		}
		return s.translateFinding(ctx, call)
	case CallMergeFindings:
		if call.FindingMerge == nil {
			break
		}
		return s.translateMerge(ctx, call, blackboard.NodeTypeFinding, call.FindingMerge)
	}
	return nil, projectinterface.ValidationError(projectinterface.ErrCodeInvalidRequest, "unsupported or incomplete legacy compatibility call", "kind")
}

func (s *Service) projectPayload(ctx context.Context, call LegacyCall, atRevision *int) (any, error) {
	switch call.Kind {
	case CallUpsertFact, CallDeprecateFact:
		envelope, err := s.deps.Reads.Read(ctx, blackboard.ReadRequest{
			ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
			ProjectID:       call.ProjectID,
			AtRevision:      atRevision,
			Kind:            blackboard.ReadKindLegacyFactDetailV1,
			LegacyFactDetail: &blackboard.LegacyFactDetailRequest{
				FactKey: call.Fact.FactKey,
			},
		})
		if err != nil {
			return nil, err
		}
		return envelope.Result, nil
	case CallPutFactRelation:
		envelope, err := s.deps.Reads.Read(ctx, blackboard.ReadRequest{
			ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
			ProjectID:       call.ProjectID,
			AtRevision:      atRevision,
			Kind:            blackboard.ReadKindLegacyFactRelationsV1,
			LegacyFactRelations: &blackboard.LegacyFactRelationsRequest{
				FactKey: call.Relation.SourceFactKey,
			},
		})
		if err != nil {
			return nil, err
		}
		relations, ok := envelope.Result.(blackboard.LegacyFactRelationsV1)
		if !ok {
			return nil, projectinterface.InternalError("legacy relation projection returned an unexpected shape")
		}
		wanted := normalizedLegacyRelation(call.Relation.Relation)
		for _, relation := range relations.Relations {
			if relation.TargetFactKey == call.Relation.TargetFactKey && relation.Relation == wanted {
				return relation, nil
			}
		}
		return nil, projectinterface.InternalError("translated legacy relation was not readable")
	case CallMergeFacts:
		return MergeResult{Merged: true}, nil
	case CallUpsertFinding:
		resolved, err := s.deps.Graph.ReadNode(ctx, blackboard.ReadNodeRequest{ProjectID: call.ProjectID, NodeType: blackboard.NodeTypeFinding, Key: call.Finding.FindingKey})
		if err != nil {
			return nil, err
		}
		envelope, err := s.deps.Reads.Read(ctx, blackboard.ReadRequest{
			ProtocolVersion:         blackboard.BlackboardReadProtocolVersion,
			ProjectID:               call.ProjectID,
			AtRevision:              atRevision,
			Kind:                    blackboard.ReadKindLegacyFindingCollectionV1,
			LegacyFindingCollection: &blackboard.LegacyFindingCollectionRequest{},
		})
		if err != nil {
			return nil, err
		}
		findings, ok := envelope.Result.(blackboard.LegacyFindingCollectionV1)
		if !ok {
			return nil, projectinterface.InternalError("legacy Finding projection returned an unexpected shape")
		}
		for _, finding := range findings.Findings {
			if finding.FindingKey == resolved.Node.StableKey {
				return finding, nil
			}
		}
		return nil, projectinterface.InternalError("translated legacy Finding was not readable")
	case CallMergeFindings:
		return MergeResult{Merged: true}, nil
	}
	return nil, projectinterface.InternalError("legacy result projection is unavailable")
}

func (s *Service) translateFact(ctx context.Context, call LegacyCall) (*projectinterface.ApplyMutationRequest, error) {
	fact := call.Fact
	if strings.TrimSpace(fact.FactKey) == "" {
		return nil, projectinterface.ValidationError(projectinterface.ErrCodeInvalidRequest, "fact_key is required", "fact.fact_key")
	}
	current, err := s.deps.Graph.ReadNode(ctx, blackboard.ReadNodeRequest{ProjectID: call.ProjectID, NodeType: blackboard.NodeTypeProjectFact, Key: fact.FactKey})
	if err != nil && !isNodeNotFound(err) {
		return nil, err
	}
	if err == nil {
		if strings.TrimSpace(fact.Summary) == "" {
			return nil, projectinterface.ValidationError(projectinterface.ErrCodeInvalidRequest, "summary is required", "fact.summary")
		}
		return translateFactUpdate(call, current.Node), nil
	}
	if strings.TrimSpace(fact.Summary) == "" {
		return nil, projectinterface.ValidationError(projectinterface.ErrCodeInvalidRequest, "summary is required", "fact.summary")
	}
	confidence := fact.Confidence
	if confidence == "" {
		confidence = string(blackboard.ConfidenceTentative)
	}
	scopeStatus := normalizedFactScope(fact.ScopeStatus)
	return &projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion:  blackboard.GraphMutationSchemaVersion,
			IdempotencyKey: graphIdempotencyKey(call),
			Operations: []blackboard.Operation{{
				OpID: "fact", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: fact.FactKey},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"category": normalizedFactCategory(fact.Category), "summary": fact.Summary, "body": fact.Body,
					"confidence": confidence, "scope_status": scopeStatus,
				}},
			}},
		},
	}, nil
}

func translateFactUpdate(call LegacyCall, current blackboard.NodeRecord) *projectinterface.ApplyMutationRequest {
	expectedVersion := current.Version
	if call.ExpectedVersion != nil {
		expectedVersion = *call.ExpectedVersion
	}
	desired := cloneProperties(current.PropertyMap)
	desired["summary"] = strings.TrimSpace(call.Fact.Summary)
	desired["category"] = normalizedFactCategory(call.Fact.Category)
	if call.Fact.Body != "" {
		desired["body"] = call.Fact.Body
	}
	desired["scope_status"] = normalizedFactScope(call.Fact.ScopeStatus)
	desiredConfidence := call.Fact.Confidence
	if desiredConfidence == "" {
		desiredConfidence = string(blackboard.ConfidenceTentative)
	}

	patch := map[string]any{}
	for _, key := range []string{"category", "summary", "body", "scope_status"} {
		if desired[key] != current.PropertyMap[key] {
			patch[key] = desired[key]
		}
	}
	operations := []blackboard.Operation{}
	nextVersion := expectedVersion
	if len(patch) > 0 {
		operations = append(operations, blackboard.Operation{
			OpID: "fact-patch", Kind: blackboard.OpPatchNode,
			Node:  blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: call.Fact.FactKey},
			Patch: blackboard.PatchNodeInput{ExpectedVersion: expectedVersion, Properties: patch},
		})
		nextVersion++
	}
	if desiredConfidence != propertyString(current.PropertyMap, "confidence") {
		operations = append(operations, blackboard.Operation{
			OpID: "fact-transition", Kind: blackboard.OpTransitionNode,
			Node:       blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: call.Fact.FactKey},
			Transition: blackboard.TransitionNodeInput{ExpectedVersion: nextVersion, Status: desiredConfidence},
		})
	}
	if len(operations) == 0 {
		operations = append(operations, blackboard.Operation{
			OpID: "fact-patch", Kind: blackboard.OpPatchNode,
			Node:  blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: call.Fact.FactKey},
			Patch: blackboard.PatchNodeInput{ExpectedVersion: expectedVersion, Properties: map[string]any{}},
		})
	}
	return &projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: graphIdempotencyKey(call), Operations: operations,
		},
	}
}

func (s *Service) translateFactDeprecation(ctx context.Context, call LegacyCall) (*projectinterface.ApplyMutationRequest, error) {
	current, err := s.deps.Graph.ReadNode(ctx, blackboard.ReadNodeRequest{ProjectID: call.ProjectID, NodeType: blackboard.NodeTypeProjectFact, Key: call.Fact.FactKey})
	if err != nil {
		return nil, err
	}
	call.Fact = &FactWrite{
		FactKey: call.Fact.FactKey, Category: propertyString(current.Node.PropertyMap, "category"),
		Summary: propertyString(current.Node.PropertyMap, "summary"), Body: "",
		Confidence: string(blackboard.ConfidenceDeprecated), ScopeStatus: propertyString(current.Node.PropertyMap, "scope_status"),
	}
	return translateFactUpdate(call, current.Node), nil
}

func normalizedFactCategory(value string) string {
	if strings.TrimSpace(value) == "" {
		return "uncategorized"
	}
	return value
}

func normalizedFactScope(value string) string {
	switch value {
	case string(blackboard.ScopeStatusInScope), string(blackboard.ScopeStatusOutOfScope), string(blackboard.ScopeStatusUnknown):
		return value
	default:
		return string(blackboard.ScopeStatusUnknown)
	}
}

func (s *Service) translateFinding(ctx context.Context, call LegacyCall) (*projectinterface.ApplyMutationRequest, error) {
	finding := call.Finding
	if strings.TrimSpace(finding.FindingKey) == "" {
		return nil, projectinterface.ValidationError(projectinterface.ErrCodeInvalidRequest, "finding_key is required", "finding.finding_key")
	}
	current, err := s.deps.Graph.ReadNode(ctx, blackboard.ReadNodeRequest{ProjectID: call.ProjectID, NodeType: blackboard.NodeTypeFinding, Key: finding.FindingKey})
	if err != nil && !isNodeNotFound(err) {
		return nil, err
	}
	if err == nil {
		return translateFindingUpdate(call, current.Node), nil
	}
	if strings.TrimSpace(finding.Title) == "" {
		return nil, projectinterface.ValidationError(projectinterface.ErrCodeInvalidRequest, "title is required", "finding.title")
	}
	status := finding.Status
	if status == "" {
		status = "unconfirmed"
	}
	return &projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: graphIdempotencyKey(call),
			Operations: []blackboard.Operation{{
				OpID: "finding", Kind: blackboard.OpCreateNode,
				Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: finding.FindingKey},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
					"title": finding.Title, "description": finding.Description, "status": status,
					"target": finding.Target, "proof": finding.Proof, "impact": finding.Impact,
					"recommendation": finding.Recommendation, "cvss_version": finding.CVSSVersion,
					"cvss_vector": finding.CVSSVector,
				}},
			}},
		},
	}, nil
}

func translateFindingUpdate(call LegacyCall, current blackboard.NodeRecord) *projectinterface.ApplyMutationRequest {
	expectedVersion := current.Version
	if call.ExpectedVersion != nil {
		expectedVersion = *call.ExpectedVersion
	}
	desired := cloneProperties(current.PropertyMap)
	updates := map[string]string{
		"title": call.Finding.Title, "description": call.Finding.Description,
		"target": call.Finding.Target, "proof": call.Finding.Proof,
		"impact": call.Finding.Impact, "recommendation": call.Finding.Recommendation,
		"cvss_version": call.Finding.CVSSVersion, "cvss_vector": call.Finding.CVSSVector,
	}
	for key, value := range updates {
		if strings.TrimSpace(value) != "" {
			desired[key] = value
		}
	}
	desiredStatus := propertyString(desired, "status")
	if call.Finding.Status != "" {
		desiredStatus = call.Finding.Status
	}
	patch := map[string]any{}
	for key := range updates {
		if desired[key] != current.PropertyMap[key] {
			patch[key] = desired[key]
		}
	}
	operations := []blackboard.Operation{}
	nextVersion := expectedVersion
	if len(patch) > 0 {
		operations = append(operations, blackboard.Operation{
			OpID: "finding-patch", Kind: blackboard.OpPatchNode,
			Node:  blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: call.Finding.FindingKey},
			Patch: blackboard.PatchNodeInput{ExpectedVersion: expectedVersion, Properties: patch},
		})
		nextVersion++
	}
	if desiredStatus != propertyString(current.PropertyMap, "status") {
		operations = append(operations, blackboard.Operation{
			OpID: "finding-transition", Kind: blackboard.OpTransitionNode,
			Node:       blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: call.Finding.FindingKey},
			Transition: blackboard.TransitionNodeInput{ExpectedVersion: nextVersion, Status: desiredStatus},
		})
	}
	if len(operations) == 0 {
		operations = append(operations, blackboard.Operation{
			OpID: "finding-patch", Kind: blackboard.OpPatchNode,
			Node:  blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: call.Finding.FindingKey},
			Patch: blackboard.PatchNodeInput{ExpectedVersion: expectedVersion, Properties: map[string]any{}},
		})
	}
	return &projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: graphIdempotencyKey(call), Operations: operations,
		},
	}
}

func (s *Service) translateFactRelation(ctx context.Context, call LegacyCall) (*projectinterface.ApplyMutationRequest, error) {
	relation := normalizedLegacyRelation(call.Relation.Relation)
	var edgeType blackboard.EdgeType
	switch relation {
	case "supports":
		edgeType = blackboard.EdgeTypeSupports
	case "contradicts":
		edgeType = blackboard.EdgeTypeContradicts
	case "leads_to":
		edgeType = blackboard.EdgeTypeLeadsTo
	default:
		return nil, projectinterface.ValidationError(
			ErrCodeLegacyRelationNotGraphRepresentable,
			"legacy relation is not representable in the graph; use an Exploration Objective dependency or explicit Fact Merge",
			"relation.relation",
		)
	}
	if strings.TrimSpace(call.Relation.SourceFactKey) == "" || strings.TrimSpace(call.Relation.TargetFactKey) == "" {
		return nil, projectinterface.ValidationError(projectinterface.ErrCodeInvalidRequest, "source and target Fact keys are required", "relation")
	}
	from, err := s.deps.Graph.ReadNode(ctx, blackboard.ReadNodeRequest{ProjectID: call.ProjectID, NodeType: blackboard.NodeTypeProjectFact, Key: call.Relation.SourceFactKey})
	if err != nil {
		return nil, err
	}
	to, err := s.deps.Graph.ReadNode(ctx, blackboard.ReadNodeRequest{ProjectID: call.ProjectID, NodeType: blackboard.NodeTypeProjectFact, Key: call.Relation.TargetFactKey})
	if err != nil {
		return nil, err
	}
	expectedVersion := 0
	if call.ExpectedVersion != nil {
		expectedVersion = *call.ExpectedVersion
	} else if current, err := s.deps.Graph.ReadActiveEdge(ctx, blackboard.ReadActiveEdgeRequest{
		ProjectID: call.ProjectID, EdgeType: edgeType, FromNodeID: from.Node.ID, ToNodeID: to.Node.ID,
	}); err == nil {
		expectedVersion = current.Version
	} else if !isEdgeNotFound(err) {
		return nil, err
	}
	return &projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: graphIdempotencyKey(call),
			Operations: []blackboard.Operation{{
				OpID: "relation", Kind: blackboard.OpPutEdge,
				PutEdge: blackboard.PutEdgeInput{
					EdgeType: edgeType,
					From:     blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: call.Relation.SourceFactKey},
					To:       blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: call.Relation.TargetFactKey},
					Summary:  call.Relation.Summary, ExpectedVersion: expectedVersion,
				},
			}},
		},
	}, nil
}

func (s *Service) translateMerge(ctx context.Context, call LegacyCall, nodeType blackboard.NodeType, merge *MergeWrite) (*projectinterface.ApplyMutationRequest, error) {
	if strings.TrimSpace(merge.SourceKey) == "" || strings.TrimSpace(merge.CanonicalKey) == "" {
		return nil, projectinterface.ValidationError(projectinterface.ErrCodeInvalidRequest, "source and canonical keys are required", "merge")
	}
	source, err := s.deps.Graph.ReadNode(ctx, blackboard.ReadNodeRequest{ProjectID: call.ProjectID, NodeType: nodeType, Key: merge.SourceKey})
	if err != nil {
		return nil, err
	}
	canonical, err := s.deps.Graph.ReadNode(ctx, blackboard.ReadNodeRequest{ProjectID: call.ProjectID, NodeType: nodeType, Key: merge.CanonicalKey})
	if err != nil {
		return nil, err
	}
	return &projectinterface.ApplyMutationRequest{
		ProtocolVersion: projectinterface.RuntimeProtocolVersion,
		Batch: projectinterface.RequestBatch{
			SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: graphIdempotencyKey(call),
			Operations: []blackboard.Operation{{
				OpID: "merge", Kind: blackboard.OpMergeNodes,
				Merge: blackboard.MergeNodesInput{
					Source: blackboard.NodeRef{ID: source.Node.ID}, Canonical: blackboard.NodeRef{ID: canonical.Node.ID},
					SourceExpectedVersion: source.Node.Version, CanonicalExpectedVersion: canonical.Node.Version,
				},
			}},
		},
	}, nil
}

func normalizedLegacyRelation(relation string) string {
	if relation == "leads-to" {
		return "leads_to"
	}
	return relation
}

func cloneProperties(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func propertyString(properties map[string]any, key string) string {
	value, _ := properties[key].(string)
	return value
}

func (s *Service) loadTranslatedRequest(ctx context.Context, call LegacyCall, requestHash string) (*projectinterface.ApplyMutationRequest, error) {
	var storedHash, raw string
	err := s.deps.DB.QueryRowContext(ctx, `
		SELECT request_hash,translated_request_json
		FROM blackboard_compatibility_requests
		WHERE project_id=? AND idempotency_scope=? AND idempotency_key=?`,
		call.ProjectID, idempotencyScope(call.Principal), call.IdempotencyKey,
	).Scan(&storedHash, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, projectinterface.InternalError("load compatibility request: " + err.Error())
	}
	if storedHash != requestHash {
		return nil, projectinterface.ValidationError(blackboard.ErrCodeIdempotencyConflict, "compatibility idempotency key was reused with a different payload", "idempotency_key")
	}
	var request projectinterface.ApplyMutationRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		return nil, projectinterface.InternalError("decode compatibility request: " + err.Error())
	}
	return &request, nil
}

func (s *Service) storeTranslatedRequest(ctx context.Context, call LegacyCall, requestHash string, request projectinterface.ApplyMutationRequest) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return projectinterface.InternalError("encode translated compatibility request: " + err.Error())
	}
	_, err = s.deps.DB.ExecContext(ctx, `
		INSERT OR IGNORE INTO blackboard_compatibility_requests (
			project_id,idempotency_scope,idempotency_key,call_kind,request_hash,translated_request_json,created_at
		) VALUES (?,?,?,?,?,?,?)`, call.ProjectID, idempotencyScope(call.Principal), call.IdempotencyKey,
		string(call.Kind), requestHash, string(raw), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return projectinterface.InternalError("store compatibility request: " + err.Error())
	}
	return nil
}

func (s *Service) loadStoredResult(ctx context.Context, call LegacyCall, requestHash string) (*LegacyResult, error) {
	var storedHash, payloadJSON, mutationJSON string
	err := s.deps.DB.QueryRowContext(ctx, `
		SELECT request_hash,payload_json,mutation_json
		FROM blackboard_compatibility_results
		WHERE project_id=? AND idempotency_scope=? AND idempotency_key=?`,
		call.ProjectID, idempotencyScope(call.Principal), call.IdempotencyKey,
	).Scan(&storedHash, &payloadJSON, &mutationJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, projectinterface.InternalError("load compatibility result: " + err.Error())
	}
	if storedHash != requestHash {
		return nil, projectinterface.ValidationError(blackboard.ErrCodeIdempotencyConflict, "compatibility idempotency key was reused with a different payload", "idempotency_key")
	}
	payload, err := decodeLegacyPayload(call.Kind, []byte(payloadJSON))
	if err != nil {
		return nil, projectinterface.InternalError("decode compatibility payload: " + err.Error())
	}
	var mutation blackboard.MutationResult
	if err := json.Unmarshal([]byte(mutationJSON), &mutation); err != nil {
		return nil, projectinterface.InternalError("decode compatibility mutation: " + err.Error())
	}
	return &LegacyResult{Payload: payload, Mutation: mutation}, nil
}

func (s *Service) storeResult(ctx context.Context, call LegacyCall, requestHash string, result LegacyResult) error {
	payloadJSON, err := json.Marshal(result.Payload)
	if err != nil {
		return projectinterface.InternalError("encode compatibility payload: " + err.Error())
	}
	mutationJSON, err := json.Marshal(result.Mutation)
	if err != nil {
		return projectinterface.InternalError("encode compatibility mutation: " + err.Error())
	}
	_, err = s.deps.DB.ExecContext(ctx, `
		INSERT OR IGNORE INTO blackboard_compatibility_results (
			project_id,idempotency_scope,idempotency_key,call_kind,request_hash,payload_json,mutation_json,created_at
		) VALUES (?,?,?,?,?,?,?,?)`, call.ProjectID, idempotencyScope(call.Principal), call.IdempotencyKey,
		string(call.Kind), requestHash, string(payloadJSON), string(mutationJSON), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return projectinterface.InternalError("store compatibility result: " + err.Error())
	}
	return nil
}

func decodeLegacyPayload(kind CallKind, raw []byte) (any, error) {
	var target any
	switch kind {
	case CallUpsertFact, CallDeprecateFact:
		target = &blackboard.LegacyFactDetailV1{}
	case CallPutFactRelation:
		target = &blackboard.LegacyFactRelationRow{}
	case CallMergeFacts, CallMergeFindings:
		target = &MergeResult{}
	case CallUpsertFinding:
		target = &blackboard.LegacyFindingV1{}
	case CallAttachEvidence:
		target = &blackboard.LegacyEvidenceArtifactV1{}
	case CallPutTaskSummary:
		target = &task.SummaryVersion{}
	case CallGenerateReport:
		target = &blackboard.LegacyReportEnvelopeV1{}
	default:
		return nil, fmt.Errorf("unsupported compatibility call kind %q", kind)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return nil, err
	}
	switch value := target.(type) {
	case *blackboard.LegacyFactDetailV1:
		return *value, nil
	case *blackboard.LegacyFactRelationRow:
		return *value, nil
	case *MergeResult:
		return *value, nil
	case *blackboard.LegacyFindingV1:
		return *value, nil
	case *blackboard.LegacyEvidenceArtifactV1:
		return *value, nil
	case *task.SummaryVersion:
		return *value, nil
	case *blackboard.LegacyReportEnvelopeV1:
		return *value, nil
	}
	return nil, fmt.Errorf("unsupported compatibility payload type %T", target)
}

func legacyRequestHash(call LegacyCall) (string, error) {
	canonical := struct {
		Kind            CallKind           `json:"kind"`
		ProjectID       string             `json:"project_id"`
		IdempotencyKey  string             `json:"idempotency_key"`
		ExpectedVersion *int               `json:"expected_version,omitempty"`
		Fact            *FactWrite         `json:"fact,omitempty"`
		Relation        *FactRelationWrite `json:"relation,omitempty"`
		FactMerge       *MergeWrite        `json:"fact_merge,omitempty"`
		Finding         *FindingWrite      `json:"finding,omitempty"`
		FindingMerge    *MergeWrite        `json:"finding_merge,omitempty"`
		Evidence        *EvidenceWrite     `json:"evidence,omitempty"`
		TaskSummary     *TaskSummaryWrite  `json:"task_summary,omitempty"`
		Report          *ReportWrite       `json:"report,omitempty"`
	}{call.Kind, call.ProjectID, call.IdempotencyKey, call.ExpectedVersion, call.Fact, call.Relation, call.FactMerge, call.Finding, call.FindingMerge, call.Evidence, call.TaskSummary, call.Report}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func graphIdempotencyKey(call LegacyCall) string {
	sum := sha256.Sum256([]byte(idempotencyScope(call.Principal) + "\x00" + call.IdempotencyKey))
	return "compat:" + hex.EncodeToString(sum[:])
}

func idempotencyScope(principal projectinterface.Principal) string {
	if principal.ActorType == blackboard.ActorTypeRuntime {
		return "continuation:" + principal.Grant.ContinuationID
	}
	return fmt.Sprintf("%s:%s", principal.ActorType, principal.ActorID)
}

func isNodeNotFound(err error) bool {
	var validation *blackboard.ValidationError
	return errors.As(err, &validation) && validation.Code == blackboard.ErrCodeNodeNotFound
}

func isEdgeNotFound(err error) bool {
	var validation *blackboard.ValidationError
	return errors.As(err, &validation) && validation.Code == blackboard.ErrCodeEdgeEndpointNotFound
}
