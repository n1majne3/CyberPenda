package blackboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

const provenanceSourceEventLimit = 200

// RecordProvenanceRequest selects the created and/or updated provenance for a
// semantic record version.
type RecordProvenanceRequest struct {
	NodeID     string
	Version    string
	Provenance string
	Literal    bool
}

type ProvenanceTaskV1 struct {
	ID     string `json:"id"`
	Goal   string `json:"goal"`
	Status string `json:"status"`
	Href   string `json:"href"`
}

type ProvenanceContinuationV1 struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Status string `json:"status"`
	Href   string `json:"href"`
}

type ProvenanceRuntimeConfigurationV1 struct {
	VersionID        string `json:"version_id"`
	RuntimePluginID  string `json:"runtime_plugin_id"`
	RuntimeProfileID string `json:"runtime_profile_id"`
	Runner           string `json:"runner"`
	ModelProviderID  string `json:"model_provider_id"`
	Model            string `json:"model"`
	Href             string `json:"href"`
}

type ScopeSnapshotSummaryV1 struct {
	Domains          int  `json:"domains"`
	IPs              int  `json:"ips"`
	CIDRs            int  `json:"cidrs"`
	URLs             int  `json:"urls"`
	Ports            int  `json:"ports"`
	Excluded         int  `json:"excluded"`
	HasTestingLimits bool `json:"has_testing_limits"`
	HasNotes         bool `json:"has_notes"`
}

type ProvenanceScopeSnapshotV1 struct {
	TaskID  string                 `json:"task_id"`
	Summary ScopeSnapshotSummaryV1 `json:"summary"`
	Href    string                 `json:"href"`
}

type ProvenanceSourceEventV1 struct {
	ID        string `json:"id"`
	Sequence  int    `json:"sequence"`
	Kind      string `json:"kind"`
	Phase     string `json:"phase,omitempty"`
	CreatedAt string `json:"created_at"`
}

type RecordProvenanceEntryV1 struct {
	Provenance            ProvenanceSummaryV1               `json:"provenance"`
	JoinStatus            string                            `json:"join_status"`
	Task                  *ProvenanceTaskV1                 `json:"task"`
	Continuation          *ProvenanceContinuationV1         `json:"continuation"`
	RuntimeConfiguration  *ProvenanceRuntimeConfigurationV1 `json:"runtime_configuration"`
	ScopeSnapshot         *ProvenanceScopeSnapshotV1        `json:"scope_snapshot"`
	SourceEvents          []ProvenanceSourceEventV1         `json:"source_events"`
	SourceEventCount      int                               `json:"source_event_count"`
	SourceEventsTruncated bool                              `json:"source_events_truncated"`
	SourceEventsHref      string                            `json:"source_events_href,omitempty"`
}

type RecordProvenanceV1 struct {
	Record  NodeRefV1                 `json:"record"`
	Version int                       `json:"version"`
	Entries []RecordProvenanceEntryV1 `json:"entries"`
}

func buildRecordProvenance(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, request RecordProvenanceRequest) (RecordProvenanceV1, error) {
	if request.NodeID == "" {
		return RecordProvenanceV1{}, readValidationError(ErrCodeInvalidQuery, "node_id is required", "node_id")
	}
	mode := request.Provenance
	if mode == "" {
		mode = "all"
	}
	if mode != "created" && mode != "updated" && mode != "all" {
		return RecordProvenanceV1{}, readValidationError(ErrCodeInvalidQuery, "provenance must be created, updated, or all", "provenance")
	}
	byID := make(map[string]NodeRecord, len(snapshot.Nodes))
	for _, candidate := range snapshot.Nodes {
		byID[candidate.ID] = candidate
	}
	node, ok := byID[request.NodeID]
	if !ok {
		return RecordProvenanceV1{}, readValidationError(ErrCodeRecordNotFound, "record does not exist in this Project", "node_id")
	}
	if node.MergeTargetID != "" && !request.Literal {
		resolved, ok := byID[node.MergeTargetID]
		if !ok {
			return RecordProvenanceV1{}, readValidationError(ErrCodeSnapshotUnavailable, "merged target cannot be reconstructed", "node_id")
		}
		node = resolved
	}
	version := node.Version
	var err error
	if request.Version != "" && request.Version != "current" {
		version, err = strconv.Atoi(request.Version)
		if err != nil || version < 1 || version > node.Version {
			return RecordProvenanceV1{}, readValidationError(ErrCodeInvalidQuery, "version must be current or an available positive version", "version")
		}
	}
	versionNode, err := loadNodeVersionAt(ctx, tx, snapshot.ProjectID, node.ID, version)
	if err != nil {
		return RecordProvenanceV1{}, err
	}
	out := RecordProvenanceV1{Record: nodeRefForNode(versionNode), Version: version, Entries: []RecordProvenanceEntryV1{}}
	versions := []int{}
	if mode == "created" || mode == "all" {
		versions = append(versions, 1)
	}
	if mode == "updated" || mode == "all" {
		if version != 1 || mode != "all" {
			versions = append(versions, version)
		}
	}
	for _, provenanceVersion := range versions {
		entry, err := loadNodeVersionProvenance(ctx, tx, snapshot.ProjectID, node.ID, provenanceVersion)
		if err != nil {
			return RecordProvenanceV1{}, err
		}
		out.Entries = append(out.Entries, entry)
	}
	return out, nil
}

func loadNodeVersionAt(ctx context.Context, tx *sql.Tx, projectID, nodeID string, version int) (NodeRecord, error) {
	var node NodeRecord
	var propertiesJSON, disposition string
	err := tx.QueryRowContext(ctx, `SELECT n.node_type, COALESCE((SELECT key FROM blackboard_key_events k WHERE k.project_id=n.project_id AND k.source_node_id=n.id AND k.role='stable' AND k.result_graph_revision<=v.result_graph_revision ORDER BY k.key_version DESC LIMIT 1),n.original_stable_key), v.version,v.disposition,v.properties_json,v.updated_at,v.semantic_hash FROM blackboard_nodes n JOIN blackboard_node_versions v ON v.project_id=n.project_id AND v.node_id=n.id WHERE n.project_id=? AND n.id=? AND v.version=?`, projectID, nodeID, version).Scan(&node.NodeType, &node.StableKey, &node.Version, &disposition, &propertiesJSON, &node.UpdatedAt, &node.SemanticHash)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeRecord{}, readValidationError(ErrCodeRecordNotFound, "record version does not exist", "version")
	}
	if err != nil {
		return NodeRecord{}, fmt.Errorf("read provenance record version: %w", err)
	}
	node.ID, node.ProjectID, node.Disposition = nodeID, projectID, Disposition(disposition)
	if err := json.Unmarshal([]byte(propertiesJSON), &node.PropertyMap); err != nil {
		return NodeRecord{}, fmt.Errorf("decode provenance record version: %w", err)
	}
	return node, nil
}

func loadNodeVersionProvenance(ctx context.Context, tx *sql.Tx, projectID, nodeID string, version int) (RecordProvenanceEntryV1, error) {
	var p ProvenanceSummaryV1
	var actorType string
	var taskID, continuationID, profileID, runner, migration sql.NullString
	var provenanceID string
	err := tx.QueryRowContext(ctx, `SELECT p.id,p.actor_type,p.actor_id,p.task_id,p.continuation_id,p.runtime_profile_id,p.runner,p.migration_source_json,p.recorded_at FROM blackboard_node_versions v JOIN blackboard_graph_operations o ON o.project_id=v.project_id AND o.mutation_seq=v.mutation_seq AND o.operation_index=v.operation_index JOIN blackboard_graph_provenance p ON p.project_id=o.project_id AND p.id=o.provenance_id WHERE v.project_id=? AND v.node_id=? AND v.version=?`, projectID, nodeID, version).Scan(&provenanceID, &actorType, &p.ActorID, &taskID, &continuationID, &profileID, &runner, &migration, &p.RecordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RecordProvenanceEntryV1{}, readValidationError(ErrCodeSnapshotUnavailable, "required record provenance is missing", "provenance")
	}
	if err != nil {
		return RecordProvenanceEntryV1{}, fmt.Errorf("read record provenance: %w", err)
	}
	p.ActorType = ActorType(actorType)
	p.TaskID, p.ContinuationID, p.RuntimeProfileID, p.Runner = nullStringPointer(taskID), nullStringPointer(continuationID), nullStringPointer(profileID), nullStringPointer(runner)
	if migration.Valid && migration.String != "" {
		_ = json.Unmarshal([]byte(migration.String), &p.MigrationSource)
	}
	entry := RecordProvenanceEntryV1{Provenance: p, JoinStatus: "complete", SourceEvents: []ProvenanceSourceEventV1{}}
	if taskID.Valid {
		entry.Task, entry.ScopeSnapshot, err = loadProvenanceTask(ctx, tx, projectID, taskID.String)
		if errors.Is(err, sql.ErrNoRows) {
			entry.JoinStatus = "missing"
		} else if err != nil {
			return RecordProvenanceEntryV1{}, fmt.Errorf("join provenance Task: %w", err)
		}
	}
	if continuationID.Valid {
		entry.Continuation, entry.RuntimeConfiguration, err = loadProvenanceContinuation(ctx, tx, projectID, continuationID.String, profileID.String, runner.String)
		if errors.Is(err, sql.ErrNoRows) {
			entry.JoinStatus = "missing"
		} else if err != nil {
			return RecordProvenanceEntryV1{}, fmt.Errorf("join provenance Continuation: %w", err)
		}
	}
	entry.SourceEvents, entry.SourceEventCount, err = loadProvenanceEvents(ctx, tx, projectID, provenanceID)
	if err != nil {
		return RecordProvenanceEntryV1{}, err
	}
	p.SourceEventCount = entry.SourceEventCount
	entry.Provenance = p
	entry.SourceEventsTruncated = entry.SourceEventCount > len(entry.SourceEvents)
	if entry.SourceEventCount > len(entry.SourceEvents) && entry.SourceEventCount <= provenanceSourceEventLimit {
		entry.JoinStatus = "missing"
	}
	if entry.SourceEventsTruncated && len(entry.SourceEvents) > 0 && taskID.Valid {
		entry.SourceEventsHref = "/api/projects/" + projectID + "/tasks/" + taskID.String + "/events?after_sequence=" + strconv.Itoa(entry.SourceEvents[len(entry.SourceEvents)-1].Sequence)
	}
	return entry, nil
}

func loadProvenanceTask(ctx context.Context, tx *sql.Tx, projectID, taskID string) (*ProvenanceTaskV1, *ProvenanceScopeSnapshotV1, error) {
	var task ProvenanceTaskV1
	var scopeJSON string
	if err := tx.QueryRowContext(ctx, `SELECT id,goal,status,scope_snapshot_json FROM tasks WHERE id=?`, taskID).Scan(&task.ID, &task.Goal, &task.Status, &scopeJSON); err != nil {
		return nil, nil, err
	}
	var scope struct {
		Domains       []string `json:"domains"`
		IPs           []string `json:"ips"`
		CIDRs         []string `json:"cidrs"`
		URLs          []string `json:"urls"`
		Ports         []string `json:"ports"`
		Excluded      []string `json:"excluded"`
		TestingLimits []string `json:"testing_limits"`
		Notes         string   `json:"notes"`
	}
	if err := json.Unmarshal([]byte(scopeJSON), &scope); err != nil {
		return &task, nil, err
	}
	task.Href = "/projects/" + projectID + "/tasks/" + taskID
	return &task, &ProvenanceScopeSnapshotV1{TaskID: taskID, Href: task.Href + "#scope-snapshot", Summary: ScopeSnapshotSummaryV1{Domains: len(scope.Domains), IPs: len(scope.IPs), CIDRs: len(scope.CIDRs), URLs: len(scope.URLs), Ports: len(scope.Ports), Excluded: len(scope.Excluded), HasTestingLimits: len(scope.TestingLimits) > 0, HasNotes: scope.Notes != ""}}, nil
}

func loadProvenanceContinuation(ctx context.Context, tx *sql.Tx, projectID, continuationID, capturedProfileID, capturedRunner string) (*ProvenanceContinuationV1, *ProvenanceRuntimeConfigurationV1, error) {
	var continuation ProvenanceContinuationV1
	var configVersionID sql.NullString
	var taskID, runtimeProvider string
	if err := tx.QueryRowContext(ctx, `SELECT id,number,status,runtime_config_version_id,task_id,runtime_provider FROM task_continuations WHERE id=?`, continuationID).Scan(&continuation.ID, &continuation.Number, &continuation.Status, &configVersionID, &taskID, &runtimeProvider); err != nil {
		return nil, nil, err
	}
	var configJSON string
	var storedProfileID string
	if !configVersionID.Valid || configVersionID.String == "" {
		return &continuation, nil, sql.ErrNoRows
	}
	if err := tx.QueryRowContext(ctx, `SELECT runtime_profile_id,config_json FROM task_runtime_config_versions WHERE id=?`, configVersionID.String).Scan(&storedProfileID, &configJSON); err != nil {
		return &continuation, nil, err
	}
	continuation.Href = "/projects/" + projectID + "/tasks/" + taskID + "#continuation-" + continuationID
	var config map[string]any
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return &continuation, nil, err
	}
	profileID := capturedProfileID
	if profileID == "" {
		profileID = storedProfileID
	}
	runtimePluginID := safeConfigString(config, "runtime_plugin_id")
	if runtimePluginID == "" {
		runtimePluginID = safeConfigString(config, "provider")
	}
	if runtimePluginID == "" {
		runtimePluginID = runtimeProvider
	}
	modelProviderID := safeConfigString(config, "model_provider_id")
	model := safeConfigString(config, "model")
	if snapshot, ok := config["model_provider_snapshot"].(map[string]any); ok {
		if modelProviderID == "" {
			modelProviderID = safeConfigString(snapshot, "model_provider_id")
		}
		if model == "" {
			model = safeConfigString(snapshot, "model")
		}
	}
	if model == "" {
		model = safeConfigString(config, "model_override")
	}
	return &continuation, &ProvenanceRuntimeConfigurationV1{VersionID: configVersionID.String, RuntimePluginID: runtimePluginID, RuntimeProfileID: profileID, Runner: capturedRunner, ModelProviderID: modelProviderID, Model: model, Href: continuation.Href + "-runtime-configuration"}, nil
}

func loadProvenanceEvents(ctx context.Context, tx *sql.Tx, projectID, provenanceID string) ([]ProvenanceSourceEventV1, int, error) {
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_graph_provenance_events WHERE project_id=? AND provenance_id=?`, projectID, provenanceID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.id,e.seq,e.kind,e.payload_json,e.created_at FROM blackboard_graph_provenance_events pe JOIN task_events e ON e.id=pe.event_id WHERE pe.project_id=? AND pe.provenance_id=? ORDER BY pe.ordinal LIMIT ?`, projectID, provenanceID, provenanceSourceEventLimit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	events := []ProvenanceSourceEventV1{}
	for rows.Next() {
		var event ProvenanceSourceEventV1
		var payloadJSON string
		if err := rows.Scan(&event.ID, &event.Sequence, &event.Kind, &payloadJSON, &event.CreatedAt); err != nil {
			return nil, 0, err
		}
		var payload map[string]any
		if json.Unmarshal([]byte(payloadJSON), &payload) == nil {
			event.Phase = safeConfigString(payload, "phase")
		}
		events = append(events, event)
	}
	return events, total, rows.Err()
}

func safeConfigString(config map[string]any, key string) string {
	value, _ := config[key].(string)
	return value
}
