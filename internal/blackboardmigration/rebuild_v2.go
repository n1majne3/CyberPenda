package blackboardmigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"pentest/internal/blackboard"
	"pentest/internal/blackboardv2"
	"pentest/internal/blackboardv2contract"
	"pentest/internal/blackboardv2grammar"
	"pentest/internal/store"
)

const rebuildResultSchema = "blackboard-v2-rebuild-result/v1"

// ErrRebuildBlocked reports that unambiguous head rebuild cannot proceed.
var ErrRebuildBlocked = errors.New("Blackboard v2 unambiguous head rebuild is blocked")

// RebuildResultV1 is the operator-visible outcome of disposable v2 rebuild.
// It never flips the store epoch and never embeds migration source IDs in
// semantic payloads.
type RebuildResultV1 struct {
	Schema      string                   `json:"schema"`
	Status      string                   `json:"status"`
	StoreEpoch  string                   `json:"store_epoch"`
	Projects    []RebuildProjectResultV1 `json:"projects"`
	Mappings    []MigrationMapping       `json:"mappings"`
	Validation  MigrationValidationV1    `json:"validation"`
	Blockers    []MigrationBlocker       `json:"validation_blockers,omitempty"`
	SourceCount int                      `json:"source_record_count"`
}

// RebuildProjectResultV1 reports one Project's rebuilt revision.
type RebuildProjectResultV1 struct {
	Project  string `json:"project"`
	Revision int    `json:"revision"`
}

// MigrationValidationV1 mirrors the closed migration validation shape without
// requiring the full cutover migration-result envelope.
type MigrationValidationV1 struct {
	Status             string `json:"status"`
	SnapshotsValidated int    `json:"snapshots_validated"`
}

type rebuildSourceRecord struct {
	projectID     string
	nodeID        string
	nodeType      blackboard.NodeType
	sourceKey     string
	version       int
	disposition   string
	properties    map[string]any
	updatedAt     string
	priorVersions []rebuildVersion
}

type rebuildVersion struct {
	version    int
	properties map[string]any
	updatedAt  string
}

type rebuildEdge struct {
	projectID string
	edgeID    string
	edgeType  blackboard.EdgeType
	fromID    string
	toID      string
	version   int
	summary   string
	updatedAt string
	prior     []rebuildEdgeVersion
}

type rebuildEdgeVersion struct {
	version   int
	state     string
	summary   string
	updatedAt string
}

type rebuildRelationHistory struct {
	version    int
	reason     string
	recordedAt string
}

type rebuildAlias struct {
	projectID    string
	nodeType     blackboard.NodeType
	aliasKey     string
	canonicalID  string
	sourceNodeID string
}

type assignedKey struct {
	targetKey  string
	sourceType string
	sourceKey  string
	v2Type     string
	action     string // retain | rename
	current    bool   // true when written to blackboard_v2_records
	record     any
	version    int
	history    []rebuildHistoryItem
	nodeID     string
}

type rebuildHistoryItem struct {
	version int
	record  any
	at      string
}

type rebuildWriteState struct {
	assignments map[string]map[string]*assignedKey
	scopeLimits map[string][]string
	projects    []RebuildProjectResultV1
}

func (s *Service) rebuildUnambiguousHeads(ctx context.Context, request MigrationRequest) (RebuildResultV1, error) {
	epoch, err := s.db.CanonicalStore()
	if err != nil {
		return RebuildResultV1{}, err
	}
	if epoch != store.CanonicalStoreGraphV1 && epoch != store.CanonicalStoreGraphV1Finalized && epoch != store.CanonicalStoreLegacyV1 {
		return RebuildResultV1{}, fmt.Errorf("unambiguous rebuild requires a v1 store epoch, got %q", epoch)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RebuildResultV1{}, fmt.Errorf("begin disposable v2 rebuild: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, state, err := s.rebuildUnambiguousHeadsWithTx(ctx, tx, request, epoch)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return RebuildResultV1{}, fmt.Errorf("commit disposable v2 rebuild: %w", err)
	}
	return s.finalizeRebuildValidation(ctx, result, state)
}

// rebuildUnambiguousHeadsWithTx rebuilds disposable v2 state inside a caller-owned
// transaction and never commits. Callers that need atomic cutover validate and
// commit themselves.
func (s *Service) rebuildUnambiguousHeadsWithTx(ctx context.Context, tx *sql.Tx, request MigrationRequest, epoch string) (RebuildResultV1, rebuildWriteState, error) {
	if err := ensureDisposableV2Tables(ctx, tx); err != nil {
		return RebuildResultV1{}, rebuildWriteState{}, err
	}

	records, err := loadRebuildSourceRecords(ctx, tx)
	if err != nil {
		return RebuildResultV1{}, rebuildWriteState{}, err
	}
	edges, edgeBlockers, err := loadRebuildSourceEdges(ctx, tx)
	if err != nil {
		return RebuildResultV1{}, rebuildWriteState{}, err
	}
	aliases, err := loadRebuildSourceAliases(ctx, tx)
	if err != nil {
		return RebuildResultV1{}, rebuildWriteState{}, err
	}

	projects, err := listProjectIDs(ctx, tx)
	if err != nil {
		return RebuildResultV1{}, rebuildWriteState{}, err
	}

	// Clear disposable v2 state only (never touch v1 graph/legacy tables).
	if err := clearDisposableV2State(ctx, tx, projects); err != nil {
		return RebuildResultV1{}, rebuildWriteState{}, err
	}

	requiredDecisions, err := collectRequiredRebuildDecisions(records)
	if err != nil {
		return RebuildResultV1{}, rebuildWriteState{}, err
	}
	decisionIndex, decisionBlockers := indexRebuildDecisions(request.Decisions, requiredDecisions)
	blockers := append([]MigrationBlocker{}, edgeBlockers...)
	blockers = append(blockers, decisionBlockers...)

	if len(requiredDecisions) > 0 || len(request.Decisions) > 0 || strings.TrimSpace(request.SourceDigest) != "" {
		currentDigest, err := sourceDigestInTransaction(ctx, tx)
		if err != nil {
			return RebuildResultV1{}, rebuildWriteState{}, err
		}
		if strings.TrimSpace(request.SourceDigest) == "" {
			blockers = append(blockers, MigrationBlocker{
				Code:    "missing_source_digest",
				Message: "Source-digest-bound operator decisions require the inspect plan source_digest.",
				Path:    "source_digest",
			})
		} else if request.SourceDigest != currentDigest {
			blockers = append(blockers, MigrationBlocker{
				Code:    "stale_source_digest",
				Message: "The supplied source_digest does not match the current v1 source.",
				Path:    "source_digest",
			})
		}
	}

	assignments := make(map[string]map[string]*assignedKey) // project -> nodeID -> assignment
	mappings := make([]MigrationMapping, 0)
	projectMappings := make(map[string][]MigrationMapping)
	scopeLimits := make(map[string][]string)        // project -> testing limit texts
	goalNodeIDs := make(map[string]map[string]bool) // project -> nodeID

	for _, projectID := range projects {
		kind, err := loadProjectKind(ctx, tx, projectID)
		if err != nil {
			return RebuildResultV1{}, rebuildWriteState{}, err
		}

		projectRecords := filterRecordsForProject(records, projectID)
		projectEdges := filterEdgesForProject(edges, projectID)
		projectAssignments, mapped, projectScopeLimits, goals, projectBlockers := assignProjectKeys(
			projectID, kind, projectRecords, projectEdges, decisionIndex,
		)
		blockers = append(blockers, projectBlockers...)
		assignments[projectID] = projectAssignments
		projectMappings[projectID] = mapped
		mappings = append(mappings, mapped...)
		if len(projectScopeLimits) > 0 {
			scopeLimits[projectID] = append(scopeLimits[projectID], projectScopeLimits...)
		}
		goalNodeIDs[projectID] = goals
	}

	// Map relationships after keys are assigned so endpoints resolve.
	now := s.clock().UTC().Format(time.RFC3339Nano)
	relations := make([]rebuildPendingRelation, 0)
	redirects := make([]struct {
		projectID, source, canonical string
	}, 0)

	for _, edge := range edges {
		// Goal endpoints never contribute edges to v2.
		if goalNodeIDs[edge.projectID][edge.fromID] || goalNodeIDs[edge.projectID][edge.toID] {
			continue
		}
		fromAssign, fromOK := assignments[edge.projectID][edge.fromID]
		toAssign, toOK := assignments[edge.projectID][edge.toID]
		if !fromOK || !toOK {
			// Non-imported / discarded endpoints are skipped without guessing.
			continue
		}
		relation := string(edge.edgeType)
		fromKey, toKey := fromAssign.targetKey, toAssign.targetKey
		fromType, toType := fromAssign.v2Type, toAssign.v2Type
		switch relation {
		case "leads_to":
			// Vague attack-chain edges are retired; narrative lives outside v2.
			continue
		case "blocks":
			// A blocks B => B depends_on A (dependent points to prerequisite).
			relation = "depends_on"
			fromKey, toKey = toAssign.targetKey, fromAssign.targetKey
			fromType, toType = toAssign.v2Type, fromAssign.v2Type
			fromAssign, toAssign = toAssign, fromAssign
		}
		if !isV2Relation(relation) {
			blockers = append(blockers, MigrationBlocker{
				Code:    "invalid_relationship_type",
				Message: "Relationship type is not part of the closed v2 vocabulary.",
				Path:    edge.projectID + "/relationship/" + relation,
			})
			continue
		}
		rule, ok := relationRule(relation)
		if !ok || !rule.Allows(fromType, toType) {
			blockers = append(blockers, MigrationBlocker{
				Code:    "invalid_relationship_endpoints",
				Message: "Relationship endpoints are not valid under the v2 grammar.",
				Path:    edge.projectID + "/" + fromKey + "/" + relation + "/" + toKey,
			})
			continue
		}
		if fromKey == toKey {
			blockers = append(blockers, MigrationBlocker{
				Code:    "invalid_relationship_self_link",
				Message: "Self-linked relationships are rejected.",
				Path:    edge.projectID + "/" + fromKey + "/" + relation,
			})
			continue
		}
		// Supersedes: replaced side should not remain current.
		if relation == "supersedes" {
			toAssign.current = false
			toAssign.record = terminalSemanticRecord(toAssign.v2Type, toAssign.record)
		}
		// Relationships require both endpoints current in v2 public model,
		// except supersedes which is historical once applied. Keep supersedes
		// out of current relations when the replaced record is historical.
		if relation == "supersedes" {
			relations = append(relations, rebuildPendingRelation{
				projectID: edge.projectID,
				fromKey:   fromKey,
				relation:  relation,
				toKey:     toKey,
				version:   max(1, edge.version),
				history: []rebuildRelationHistory{{
					version:    max(1, edge.version),
					reason:     "",
					recordedAt: coalesceString(edge.updatedAt, now),
				}},
			})
			continue
		}
		if !fromAssign.current || !toAssign.current {
			continue
		}
		reason := ""
		if rule.ReasonPolicy == blackboardv2grammar.ReasonOptional {
			reason = strings.TrimSpace(edge.summary)
			if reason != "" {
				if errCode := validateReason(reason, fromKey, toKey); errCode != "" {
					blockers = append(blockers, MigrationBlocker{
						Code:    errCode,
						Message: "Relationship reason is invalid under the v2 contract.",
						Path:    edge.projectID + "/" + fromKey + "/" + relation + "/" + toKey,
					})
					continue
				}
			}
		}
		relations = append(relations, rebuildPendingRelation{
			projectID: edge.projectID,
			fromKey:   fromKey,
			relation:  relation,
			toKey:     toKey,
			version:   max(1, edge.version),
			reason:    reason,
			history:   rebuildRelationHistoryFor(edge),
		})
	}

	// Validate acyclic depends_on (and other acyclic) subgraphs before commit.
	blockers = append(blockers, detectRebuildRelationCycles(relations)...)

	// Key redirects from v1 aliases and merges (project-local only).
	for _, alias := range aliases {
		canonicalAssign, ok := assignments[alias.projectID][alias.canonicalID]
		if !ok {
			continue
		}
		sourceKey := alias.aliasKey
		// Prefer the mapped source-node key when the alias source node was imported.
		if sourceAssign, ok := assignments[alias.projectID][alias.sourceNodeID]; ok {
			sourceKey = sourceAssign.targetKey
			if sourceKey == canonicalAssign.targetKey {
				continue
			}
		} else {
			// Alias key may itself need project-wide normalization.
			sourceKey = normalizeAliasKey(alias.projectID, string(alias.nodeType), alias.aliasKey, assignments[alias.projectID])
		}
		if sourceKey == "" || sourceKey == canonicalAssign.targetKey {
			continue
		}
		redirects = append(redirects, struct {
			projectID, source, canonical string
		}{projectID: alias.projectID, source: sourceKey, canonical: canonicalAssign.targetKey})
	}

	if len(blockers) > 0 {
		sort.Slice(blockers, func(i, j int) bool {
			return blockers[i].Code+blockers[i].Path < blockers[j].Code+blockers[j].Path
		})
		return RebuildResultV1{
			Schema:     rebuildResultSchema,
			Status:     "blocked",
			StoreEpoch: epoch,
			Blockers:   blockers,
			Mappings:   mappings,
			Validation: MigrationValidationV1{Status: "passed", SnapshotsValidated: 0},
		}, rebuildWriteState{}, ErrRebuildBlocked
	}

	projectResults := make([]RebuildProjectResultV1, 0, len(projects))
	for _, projectID := range projects {
		projectAssignments := assignments[projectID]
		// Deterministic write order.
		nodeIDs := make([]string, 0, len(projectAssignments))
		for nodeID := range projectAssignments {
			nodeIDs = append(nodeIDs, nodeID)
		}
		sort.Slice(nodeIDs, func(i, j int) bool {
			left := projectAssignments[nodeIDs[i]]
			right := projectAssignments[nodeIDs[j]]
			if left.v2Type != right.v2Type {
				return left.v2Type < right.v2Type
			}
			return left.targetKey < right.targetKey
		})

		revision := 0
		for _, nodeID := range nodeIDs {
			assign := projectAssignments[nodeID]
			for _, item := range assign.history {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO blackboard_v2_record_history (project_id, key, version, type, record_json, recorded_at)
					VALUES (?, ?, ?, ?, ?, ?)`,
					projectID, assign.targetKey, item.version, assign.v2Type, mustJSON(item.record), item.at,
				); err != nil {
					return RebuildResultV1{}, rebuildWriteState{}, fmt.Errorf("store rebuilt history for %s/%s@%d: %w", projectID, assign.targetKey, item.version, err)
				}
			}
			if !assign.current {
				// Terminal heads: final semantic version lives only in history.
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO blackboard_v2_record_history (project_id, key, version, type, record_json, recorded_at)
					VALUES (?, ?, ?, ?, ?, ?)
					ON CONFLICT(project_id, key, version) DO NOTHING`,
					projectID, assign.targetKey, assign.version, assign.v2Type, mustJSON(assign.record), coalesceTime(assign.history, now),
				); err != nil {
					return RebuildResultV1{}, rebuildWriteState{}, fmt.Errorf("store terminal rebuilt history for %s/%s: %w", projectID, assign.targetKey, err)
				}
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO blackboard_v2_records (project_id, key, type, version, record_json, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				projectID, assign.targetKey, assign.v2Type, assign.version, mustJSON(assign.record), now, now,
			); err != nil {
				return RebuildResultV1{}, rebuildWriteState{}, fmt.Errorf("store rebuilt record %s/%s: %w", projectID, assign.targetKey, err)
			}
			revision++
		}

		// Relationships for this project.
		for _, rel := range relations {
			if rel.projectID != projectID {
				continue
			}
			if rel.relation != "supersedes" {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO blackboard_v2_relationships (project_id, from_key, relation, to_key, version, reason, created_at, updated_at)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					projectID, rel.fromKey, rel.relation, rel.toKey, rel.version, rel.reason, now, now,
				); err != nil {
					return RebuildResultV1{}, rebuildWriteState{}, fmt.Errorf("store rebuilt relationship %s %s %s: %w", rel.fromKey, rel.relation, rel.toKey, err)
				}
			}
			for _, historical := range rel.history {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO blackboard_v2_relationship_history (project_id, from_key, relation, to_key, version, reason, recorded_at)
					VALUES (?, ?, ?, ?, ?, ?, ?)
					ON CONFLICT(project_id, from_key, relation, to_key, version) DO NOTHING`,
					projectID, rel.fromKey, rel.relation, rel.toKey, historical.version, historical.reason, historical.recordedAt,
				); err != nil {
					return RebuildResultV1{}, rebuildWriteState{}, fmt.Errorf("store rebuilt relationship history %s %s %s@%d: %w", rel.fromKey, rel.relation, rel.toKey, historical.version, err)
				}
			}
			revision++
		}

		for _, redirect := range redirects {
			if redirect.projectID != projectID {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO blackboard_v2_key_redirects (project_id, source_key, canonical_key, created_at)
				VALUES (?, ?, ?, ?)
				ON CONFLICT(project_id, source_key) DO UPDATE SET canonical_key=excluded.canonical_key`,
				projectID, redirect.source, redirect.canonical, now,
			); err != nil {
				return RebuildResultV1{}, rebuildWriteState{}, fmt.Errorf("store rebuilt key redirect %s -> %s: %w", redirect.source, redirect.canonical, err)
			}
		}

		if limits := scopeLimits[projectID]; len(limits) > 0 {
			if err := persistRebuildScopeLimits(ctx, tx, projectID, limits, now); err != nil {
				return RebuildResultV1{}, rebuildWriteState{}, err
			}
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO blackboard_v2_project_state (project_id, revision)
			VALUES (?, ?)
			ON CONFLICT(project_id) DO UPDATE SET revision=excluded.revision`,
			projectID, revision,
		); err != nil {
			return RebuildResultV1{}, rebuildWriteState{}, fmt.Errorf("store rebuilt project revision for %s: %w", projectID, err)
		}
		projectResults = append(projectResults, RebuildProjectResultV1{Project: projectID, Revision: revision})
	}

	// Persist mapping index for endpoint resolution without putting source IDs
	// into semantic payloads.
	for _, projectID := range projects {
		if err := persistRebuildMappings(ctx, tx, projectID, projectMappings[projectID], now); err != nil {
			return RebuildResultV1{}, rebuildWriteState{}, err
		}
	}

	// Epoch must remain v1 until the later atomic cutover.
	var committedEpoch string
	if err := tx.QueryRowContext(ctx, `SELECT canonical_store FROM blackboard_store_state WHERE id=1`).Scan(&committedEpoch); err != nil {
		return RebuildResultV1{}, rebuildWriteState{}, err
	}
	if committedEpoch != epoch {
		return RebuildResultV1{}, rebuildWriteState{}, fmt.Errorf("rebuild mutated store epoch from %s to %s", epoch, committedEpoch)
	}

	sort.Slice(mappings, func(i, j int) bool {
		left := mappings[i].SourceType + "\x00" + mappings[i].SourceKey + "\x00" + mappings[i].TargetKey
		right := mappings[j].SourceType + "\x00" + mappings[j].SourceKey + "\x00" + mappings[j].TargetKey
		return left < right
	})
	sort.Slice(projectResults, func(i, j int) bool { return projectResults[i].Project < projectResults[j].Project })

	state := rebuildWriteState{
		assignments: assignments,
		scopeLimits: scopeLimits,
		projects:    projectResults,
	}
	return RebuildResultV1{
		Schema:      rebuildResultSchema,
		Status:      "rebuilt",
		StoreEpoch:  epoch,
		Projects:    projectResults,
		Mappings:    mappings,
		Validation:  MigrationValidationV1{Status: "passed", SnapshotsValidated: 0},
		SourceCount: len(records),
	}, state, nil
}

func (s *Service) finalizeRebuildValidation(ctx context.Context, result RebuildResultV1, state rebuildWriteState) (RebuildResultV1, error) {
	v2 := blackboardv2.NewServiceWithEvidence(s.db, blackboardv2.EvidenceConfig{ArtifactRoot: s.artifactRoot})
	validated := 0
	for _, project := range state.projects {
		projection, err := v2.ProjectRuntimeSnapshot(ctx, project.Project)
		if err != nil {
			return RebuildResultV1{}, fmt.Errorf("project Runtime Snapshot for %s: %w", project.Project, err)
		}
		if projection.Snapshot.Schema != "runtime-blackboard/v2" {
			return RebuildResultV1{}, fmt.Errorf("snapshot schema for %s = %q", project.Project, projection.Snapshot.Schema)
		}
		if err := validateSnapshotContract(projection.Bytes); err != nil {
			return RebuildResultV1{}, fmt.Errorf("snapshot contract for %s: %w", project.Project, err)
		}
		again, err := v2.ProjectRuntimeSnapshot(ctx, project.Project)
		if err != nil {
			return RebuildResultV1{}, err
		}
		if string(again.Bytes) != string(projection.Bytes) {
			return RebuildResultV1{}, fmt.Errorf("snapshot bytes for %s are not deterministic", project.Project)
		}
		if err := validateDetailAndHistoryReads(ctx, v2, project.Project, state.assignments[project.Project]); err != nil {
			return RebuildResultV1{}, err
		}
		validated++
	}
	result.Validation = MigrationValidationV1{Status: "passed", SnapshotsValidated: validated}
	return result, nil
}

func validateDetailAndHistoryReads(ctx context.Context, v2 *blackboardv2.Service, projectID string, assigns map[string]*assignedKey) error {
	for _, assign := range assigns {
		if !assign.current {
			// History-only keys must still resolve Semantic History.
			history, err := v2.ReadHistory(ctx, projectID, assign.targetKey, blackboardv2.HistoryOptions{Limit: 20})
			if err != nil {
				return fmt.Errorf("history read for terminal %s/%s: %w", projectID, assign.targetKey, err)
			}
			if history.Schema != "semantic-history/v2" || history.Key != assign.targetKey {
				return fmt.Errorf("history identity for %s/%s = %#v", projectID, assign.targetKey, history)
			}
			continue
		}
		detail, err := v2.ReadCurrent(ctx, projectID, assign.targetKey)
		if err != nil {
			return fmt.Errorf("detail read for %s/%s: %w", projectID, assign.targetKey, err)
		}
		if detail.Schema != "blackboard-record/v2" || detail.Key != assign.targetKey || detail.Type != assign.v2Type {
			return fmt.Errorf("detail identity for %s/%s = %#v", projectID, assign.targetKey, detail)
		}
		// Reject audit/storage envelope leakage.
		raw, err := json.Marshal(detail.Record)
		if err != nil {
			return err
		}
		for _, banned := range []string{
			`"node_id"`, `"mutation_seq"`, `"semantic_hash"`, `"graph_revision"`,
			`"provenance"`, `"actor_type"`, `"migration_source"`, `"recorded_at"`,
			`"source_table"`, `"legacy_primary_id"`, assign.nodeID,
		} {
			if banned != "" && strings.Contains(string(raw), banned) {
				return fmt.Errorf("detail for %s/%s leaked storage envelope field %s: %s", projectID, assign.targetKey, banned, raw)
			}
		}
		if len(assign.history) > 0 {
			history, err := v2.ReadHistory(ctx, projectID, assign.targetKey, blackboardv2.HistoryOptions{Limit: 100})
			if err != nil {
				return fmt.Errorf("history read for %s/%s: %w", projectID, assign.targetKey, err)
			}
			if history.Schema != "semantic-history/v2" {
				return fmt.Errorf("history schema for %s/%s = %q", projectID, assign.targetKey, history.Schema)
			}
		}
	}
	return nil
}

func validateSnapshotContract(raw []byte) error {
	harness, err := blackboardv2contract.NewHarness()
	if err != nil {
		return err
	}
	return harness.Validate("runtimeSnapshot", raw)
}

func assignProjectKeys(
	projectID, projectKind string,
	records []rebuildSourceRecord,
	edges []rebuildEdge,
	decisions map[string]MigrationDecision,
) (map[string]*assignedKey, []MigrationMapping, []string, map[string]bool, []MigrationBlocker) {
	blockers := make([]MigrationBlocker, 0)
	scopeLimits := make([]string, 0)
	goalIDs := make(map[string]bool)
	// Stable ordering for collision ownership.
	sort.Slice(records, func(i, j int) bool {
		if records[i].nodeType != records[j].nodeType {
			return v2TypeOrder(records[i].nodeType) < v2TypeOrder(records[j].nodeType)
		}
		if records[i].sourceKey != records[j].sourceKey {
			return records[i].sourceKey < records[j].sourceKey
		}
		return records[i].nodeID < records[j].nodeID
	})

	// Index node types for observation support detection.
	nodeTypeByID := make(map[string]blackboard.NodeType, len(records))
	propsByID := make(map[string]map[string]any, len(records))
	for _, record := range records {
		nodeTypeByID[record.nodeID] = record.nodeType
		propsByID[record.nodeID] = record.properties
	}
	supportedObservations := observationSupportSet(edges, nodeTypeByID, propsByID)
	producedTargets := producedTargetSet(edges)

	usedKeys := make(map[string]string) // targetKey -> nodeID
	assignments := make(map[string]*assignedKey, len(records))
	mappings := make([]MigrationMapping, 0, len(records))

	for _, record := range records {
		if record.nodeType == blackboard.NodeTypeGoal {
			goalIDs[record.nodeID] = true
			continue // Goals and Goal-only edges are omitted; Task Goal stays on Task.
		}

		// Removed workflow types with explicit mapping rules.
		switch record.nodeType {
		case blackboard.NodeTypeObservation:
			assign, mapping, blocker := mapObservationRecord(projectID, record, supportedObservations[record.nodeID], decisions, usedKeys)
			if blocker != nil {
				blockers = append(blockers, *blocker)
				continue
			}
			if assign == nil {
				continue
			}
			assignments[record.nodeID] = assign
			mappings = append(mappings, mapping)
			continue
		case blackboard.NodeTypeHypothesis:
			assign, mapping, blocker := mapHypothesisRecord(projectID, record, decisions, usedKeys)
			if blocker != nil {
				blockers = append(blockers, *blocker)
				continue
			}
			if assign == nil {
				continue
			}
			assignments[record.nodeID] = assign
			mappings = append(mappings, mapping)
			continue
		case blackboard.NodeTypeProjectDirective:
			assign, mapping, limits, blocker := mapDirectiveRecord(projectID, record, decisions, usedKeys)
			if blocker != nil {
				blockers = append(blockers, *blocker)
				continue
			}
			scopeLimits = append(scopeLimits, limits...)
			if assign == nil {
				if mapping.Action != "" {
					mappings = append(mappings, mapping)
				}
				continue
			}
			assignments[record.nodeID] = assign
			mappings = append(mappings, mapping)
			continue
		}

		v2Type, ok := mapNodeType(record.nodeType)
		if !ok {
			continue
		}
		if projectKind != "ctf_challenge" && v2Type == "solution" {
			blockers = append(blockers, MigrationBlocker{
				Code:    "solution_project_kind_mismatch",
				Message: "Solution heads are valid only for CTF Challenge Projects.",
				Path:    projectID + "/solution/" + record.sourceKey,
			})
			continue
		}
		semantic, current, err := mapSemanticRecord(v2Type, record.properties)
		if err != nil {
			blockers = append(blockers, MigrationBlocker{
				Code:    "malformed_source_identity",
				Message: err.Error(),
				Path:    projectID + "/" + v2Type + "/" + record.sourceKey,
			})
			continue
		}
		// disposition merged/archived is never current.
		if record.disposition != string(blackboard.DispositionMain) {
			current = false
		}

		targetKey, action := chooseTargetKey(projectID, v2Type, record.sourceKey, usedKeys)
		usedKeys[targetKey] = record.nodeID

		history := rebuildHistoryForType(v2Type, record)

		assignments[record.nodeID] = &assignedKey{
			targetKey:  targetKey,
			sourceType: string(record.nodeType),
			sourceKey:  record.sourceKey,
			v2Type:     v2Type,
			action:     action,
			current:    current,
			record:     semantic,
			version:    max(1, record.version),
			history:    history,
			nodeID:     record.nodeID,
		}
		mappings = append(mappings, MigrationMapping{
			Project:    projectID,
			SourceType: string(record.nodeType),
			SourceKey:  record.sourceKey,
			Action:     action,
			TargetKey:  targetKey,
		})

		// Reusable terminal summaries lacking outcomes become conservative Facts.
		if !current && record.disposition == string(blackboard.DispositionMain) {
			if factAssign, factMapping, ok := mapTerminalSummaryFact(projectID, record, v2Type, semantic, producedTargets, usedKeys); ok {
				// Synthetic key under a non-colliding assignment id.
				syntheticID := "terminal-summary:" + record.nodeID
				assignments[syntheticID] = factAssign
				// Mapping uses a distinct source_key so the rebuild mapping index
				// can retain both the terminal Attempt history and the Fact.
				factMapping.SourceKey = "terminal-summary:" + record.sourceKey
				mappings = append(mappings, factMapping)
			}
		}
	}
	return assignments, mappings, scopeLimits, goalIDs, blockers
}

func rebuildHistoryForType(v2Type string, record rebuildSourceRecord) []rebuildHistoryItem {
	history := make([]rebuildHistoryItem, 0, len(record.priorVersions))
	for _, prior := range record.priorVersions {
		if prior.version >= record.version {
			continue
		}
		priorSemantic, _, priorErr := mapSemanticRecord(v2Type, prior.properties)
		if priorErr != nil {
			continue
		}
		history = append(history, rebuildHistoryItem{
			version: prior.version,
			record:  priorSemantic,
			at:      coalesceString(prior.updatedAt, record.updatedAt),
		})
	}
	sort.Slice(history, func(i, j int) bool { return history[i].version < history[j].version })
	return history
}

func mapObservationRecord(
	projectID string,
	record rebuildSourceRecord,
	hasSupport bool,
	decisions map[string]MigrationDecision,
	usedKeys map[string]string,
) (*assignedKey, MigrationMapping, *MigrationBlocker) {
	if record.disposition != string(blackboard.DispositionMain) {
		return nil, MigrationMapping{}, nil
	}
	status := stringProp(record.properties, "status")
	if status == "" {
		status = "recorded"
	}
	if status == "superseded" {
		return nil, MigrationMapping{}, nil
	}
	summary := stringProp(record.properties, "summary")
	if strings.TrimSpace(summary) == "" {
		return nil, MigrationMapping{}, nil // meaningless without summary
	}
	scope := stringProp(record.properties, "scope_status")
	if scope == "" {
		scope = "unknown"
	}
	rawConfidence := strings.TrimSpace(stringProp(record.properties, "confidence"))
	confidence := ""
	preferredKey := record.sourceKey
	switch rawConfidence {
	case "tentative":
		confidence = "tentative"
	case "confirmed":
		confidence = "confirmed"
	case "":
		// Graph observations without confidence map by support state.
		if hasSupport {
			confidence = "confirmed"
		} else {
			confidence = "tentative"
		}
	default:
		// Ambiguous confidence requires an accepted operator decision.
		decision, ok := decisions[decisionLookupKey(projectID, "observation", record.sourceKey)]
		if !ok || strings.TrimSpace(decision.Decision) == "" {
			return nil, MigrationMapping{}, &MigrationBlocker{
				Code:    "missing_decision",
				Message: "Ambiguous Observation confidence requires a source-digest-bound operator decision.",
				Path:    projectID + "/observation/" + record.sourceKey,
			}
		}
		switch decision.Decision {
		case "tentative_fact":
			confidence = "tentative"
		case "confirmed_fact":
			confidence = "confirmed"
		default:
			return nil, MigrationMapping{}, &MigrationBlocker{
				Code:    "disallowed_decision",
				Message: "Observation decision must be tentative_fact or confirmed_fact.",
				Path:    projectID + "/observation/" + record.sourceKey,
			}
		}
		if decision.TargetKey != "" {
			preferredKey = decision.TargetKey
		}
	}

	targetKey, action := chooseTargetKey(projectID, "fact", preferredKey, usedKeys)
	if preferredKey != record.sourceKey && isConformingV2Key(preferredKey) {
		if _, taken := usedKeys[preferredKey]; !taken {
			targetKey = preferredKey
			action = "retain"
		}
	}
	usedKeys[targetKey] = record.nodeID
	fact := blackboardv2.FactRecord{
		Category:    "observation",
		Summary:     summary,
		Body:        stringProp(record.properties, "detail"),
		Confidence:  confidence,
		ScopeStatus: scope,
	}
	return &assignedKey{
		targetKey: targetKey, sourceType: "observation", sourceKey: record.sourceKey,
		v2Type: "fact", action: action, current: true, record: fact,
		version: max(1, record.version), nodeID: record.nodeID,
	}, MigrationMapping{Project: projectID, SourceType: "observation", SourceKey: record.sourceKey, Action: action, TargetKey: targetKey}, nil
}

func mapHypothesisRecord(
	projectID string,
	record rebuildSourceRecord,
	decisions map[string]MigrationDecision,
	usedKeys map[string]string,
) (*assignedKey, MigrationMapping, *MigrationBlocker) {
	if record.disposition != string(blackboard.DispositionMain) {
		return nil, MigrationMapping{}, nil
	}
	status := stringProp(record.properties, "status")
	if status == "" {
		status = "open"
	}
	if status == "superseded" {
		return nil, MigrationMapping{}, nil
	}
	// Active hypotheses require decisions.
	if status != "open" && status != "supported" && status != "contradicted" && status != "inconclusive" {
		return nil, MigrationMapping{}, nil
	}
	statement := stringProp(record.properties, "statement")
	if strings.TrimSpace(statement) == "" {
		return nil, MigrationMapping{}, nil
	}
	decision, ok := decisions[decisionLookupKey(projectID, "hypothesis", record.sourceKey)]
	if !ok || strings.TrimSpace(decision.Decision) == "" {
		return nil, MigrationMapping{}, &MigrationBlocker{
			Code:    "missing_decision",
			Message: "Active Hypothesis requires a source-digest-bound operator decision.",
			Path:    projectID + "/hypothesis/" + record.sourceKey,
		}
	}
	switch decision.Decision {
	case "discard":
		return nil, MigrationMapping{Project: projectID, SourceType: "hypothesis", SourceKey: record.sourceKey, Action: "discard"}, nil
	case "objective":
		targetKey, action := chooseTargetKey(projectID, "objective", firstNonEmpty(decision.TargetKey, record.sourceKey), usedKeys)
		if decision.TargetKey != "" && isConformingV2Key(decision.TargetKey) {
			if _, taken := usedKeys[decision.TargetKey]; !taken {
				targetKey = decision.TargetKey
				action = "retain"
			}
		}
		usedKeys[targetKey] = record.nodeID
		obj := blackboardv2.ObjectiveRecord{Status: "open", Objective: statement}
		return &assignedKey{
			targetKey: targetKey, sourceType: "hypothesis", sourceKey: record.sourceKey,
			v2Type: "objective", action: action, current: true, record: obj,
			version: max(1, record.version), nodeID: record.nodeID,
		}, MigrationMapping{Project: projectID, SourceType: "hypothesis", SourceKey: record.sourceKey, Action: action, TargetKey: targetKey}, nil
	case "tentative_fact":
		targetKey, action := chooseTargetKey(projectID, "fact", firstNonEmpty(decision.TargetKey, record.sourceKey), usedKeys)
		if decision.TargetKey != "" && isConformingV2Key(decision.TargetKey) {
			if _, taken := usedKeys[decision.TargetKey]; !taken {
				targetKey = decision.TargetKey
				action = "retain"
			}
		}
		usedKeys[targetKey] = record.nodeID
		fact := blackboardv2.FactRecord{
			Category: "hypothesis", Summary: statement, Body: stringProp(record.properties, "rationale"),
			Confidence: "tentative", ScopeStatus: "unknown",
		}
		return &assignedKey{
			targetKey: targetKey, sourceType: "hypothesis", sourceKey: record.sourceKey,
			v2Type: "fact", action: action, current: true, record: fact,
			version: max(1, record.version), nodeID: record.nodeID,
		}, MigrationMapping{Project: projectID, SourceType: "hypothesis", SourceKey: record.sourceKey, Action: action, TargetKey: targetKey}, nil
	default:
		return nil, MigrationMapping{}, &MigrationBlocker{
			Code:    "disallowed_decision",
			Message: "Hypothesis decision must be objective, tentative_fact, or discard.",
			Path:    projectID + "/hypothesis/" + record.sourceKey,
		}
	}
}

func mapDirectiveRecord(
	projectID string,
	record rebuildSourceRecord,
	decisions map[string]MigrationDecision,
	usedKeys map[string]string,
) (*assignedKey, MigrationMapping, []string, *MigrationBlocker) {
	if record.disposition != string(blackboard.DispositionMain) {
		return nil, MigrationMapping{}, nil, nil
	}
	status := stringProp(record.properties, "status")
	// proposed, retired, superseded, and other non-active states are discarded.
	if status != "active" {
		return nil, MigrationMapping{}, nil, nil
	}
	directive := stringProp(record.properties, "directive")
	if strings.TrimSpace(directive) == "" {
		return nil, MigrationMapping{}, nil, nil
	}
	decision, ok := decisions[decisionLookupKey(projectID, "project_directive", record.sourceKey)]
	if !ok || strings.TrimSpace(decision.Decision) == "" {
		return nil, MigrationMapping{}, nil, &MigrationBlocker{
			Code:    "missing_decision",
			Message: "Active Project Directive requires a source-digest-bound operator decision.",
			Path:    projectID + "/project_directive/" + record.sourceKey,
		}
	}
	switch decision.Decision {
	case "scope_limit":
		return nil, MigrationMapping{
			Project: projectID, SourceType: "project_directive", SourceKey: record.sourceKey,
			Action: "scope_limit",
		}, []string{directive}, nil
	case "objective":
		targetKey, action := chooseTargetKey(projectID, "objective", firstNonEmpty(decision.TargetKey, record.sourceKey), usedKeys)
		if decision.TargetKey != "" && isConformingV2Key(decision.TargetKey) {
			if _, taken := usedKeys[decision.TargetKey]; !taken {
				targetKey = decision.TargetKey
				action = "retain"
			}
		}
		usedKeys[targetKey] = record.nodeID
		obj := blackboardv2.ObjectiveRecord{Status: "open", Objective: directive}
		return &assignedKey{
			targetKey: targetKey, sourceType: "project_directive", sourceKey: record.sourceKey,
			v2Type: "objective", action: action, current: true, record: obj,
			version: max(1, record.version), nodeID: record.nodeID,
		}, MigrationMapping{Project: projectID, SourceType: "project_directive", SourceKey: record.sourceKey, Action: action, TargetKey: targetKey}, nil, nil
	default:
		return nil, MigrationMapping{}, nil, &MigrationBlocker{
			Code:    "disallowed_decision",
			Message: "Project Directive decision must be scope_limit or objective.",
			Path:    projectID + "/project_directive/" + record.sourceKey,
		}
	}
}

func mapTerminalSummaryFact(
	projectID string,
	record rebuildSourceRecord,
	v2Type string,
	semantic any,
	producedTargets map[string]bool,
	usedKeys map[string]string,
) (*assignedKey, MigrationMapping, bool) {
	if v2Type != "attempt" {
		return nil, MigrationMapping{}, false
	}
	if producedTargets[record.nodeID] {
		return nil, MigrationMapping{}, false
	}
	summary := ""
	if attempt, ok := semantic.(blackboardv2.AttemptRecord); ok {
		summary = strings.TrimSpace(attempt.Summary)
	}
	if summary == "" {
		return nil, MigrationMapping{}, false
	}
	sourceKey := "fact:from-" + record.sourceKey
	targetKey, _ := chooseTargetKey(projectID, "fact", sourceKey, usedKeys)
	usedKeys[targetKey] = "terminal-summary:" + record.nodeID
	fact := blackboardv2.FactRecord{
		Category:    "progress",
		Summary:     summary,
		Confidence:  "tentative",
		ScopeStatus: "unknown",
	}
	return &assignedKey{
			targetKey: targetKey, sourceType: "attempt", sourceKey: record.sourceKey,
			v2Type: "fact", action: "terminal_summary_fact", current: true, record: fact,
			version: 1, nodeID: record.nodeID,
		}, MigrationMapping{
			Project: projectID, SourceType: "attempt", SourceKey: record.sourceKey,
			Action: "terminal_summary_fact", TargetKey: targetKey,
		}, true
}

func observationSupportSet(edges []rebuildEdge, nodeTypeByID map[string]blackboard.NodeType, propsByID map[string]map[string]any) map[string]bool {
	supported := make(map[string]bool)
	// First pass: identify confirmed facts and succeeded attempts.
	confirmedFacts := make(map[string]bool)
	succeededAttempts := make(map[string]bool)
	for id, nodeType := range nodeTypeByID {
		props := propsByID[id]
		switch nodeType {
		case blackboard.NodeTypeProjectFact:
			if stringProp(props, "confidence") == "confirmed" {
				confirmedFacts[id] = true
			}
		case blackboard.NodeTypeAttempt:
			if stringProp(props, "status") == "succeeded" {
				succeededAttempts[id] = true
			}
		}
	}
	for _, edge := range edges {
		switch edge.edgeType {
		case blackboard.EdgeTypeEvidences:
			if nodeTypeByID[edge.toID] == blackboard.NodeTypeObservation && nodeTypeByID[edge.fromID] == blackboard.NodeTypeEvidenceArtifact {
				supported[edge.toID] = true
			}
		case blackboard.EdgeTypeSupports:
			if nodeTypeByID[edge.toID] == blackboard.NodeTypeObservation && confirmedFacts[edge.fromID] {
				supported[edge.toID] = true
			}
		case blackboard.EdgeTypeProduced:
			// Production alone is not confirmation support; only succeeded Attempt production counts
			// when paired with other proof, but ADR says "supported state" — evidences is primary.
			if nodeTypeByID[edge.toID] == blackboard.NodeTypeObservation && succeededAttempts[edge.fromID] {
				// Succeeded production is weak support; keep as non-confirming unless evidences present.
				_ = edge
			}
		}
	}
	return supported
}

func producedTargetSet(edges []rebuildEdge) map[string]bool {
	// Map attempt nodeID -> true when it produced any semantic outcome.
	produced := make(map[string]bool)
	for _, edge := range edges {
		if edge.edgeType == blackboard.EdgeTypeProduced {
			produced[edge.fromID] = true
		}
	}
	return produced
}

func collectRequiredRebuildDecisions(records []rebuildSourceRecord) ([]MigrationDecision, error) {
	decisions := make([]MigrationDecision, 0)
	for _, record := range records {
		if record.disposition != string(blackboard.DispositionMain) {
			continue
		}
		switch record.nodeType {
		case blackboard.NodeTypeObservation:
			status := stringProp(record.properties, "status")
			if status == "superseded" {
				continue
			}
			confidence := strings.TrimSpace(stringProp(record.properties, "confidence"))
			if confidence != "" && confidence != "tentative" && confidence != "confirmed" {
				decisions = append(decisions, MigrationDecision{
					Source:         MigrationSourceRef{Project: record.projectID, Type: "observation", Key: record.sourceKey},
					AllowedActions: []string{"tentative_fact", "confirmed_fact"},
				})
			}
		case blackboard.NodeTypeHypothesis:
			status := stringProp(record.properties, "status")
			if status == "" {
				status = "open"
			}
			if status == "open" || status == "supported" || status == "contradicted" || status == "inconclusive" {
				decisions = append(decisions, MigrationDecision{
					Source:         MigrationSourceRef{Project: record.projectID, Type: "hypothesis", Key: record.sourceKey},
					AllowedActions: []string{"objective", "tentative_fact", "discard"},
				})
			}
		case blackboard.NodeTypeProjectDirective:
			if stringProp(record.properties, "status") == "active" {
				decisions = append(decisions, MigrationDecision{
					Source:         MigrationSourceRef{Project: record.projectID, Type: "project_directive", Key: record.sourceKey},
					AllowedActions: []string{"scope_limit", "objective"},
				})
			}
		}
	}
	sort.Slice(decisions, func(i, j int) bool {
		left := decisions[i].Source.Project + "\x00" + decisions[i].Source.Type + "\x00" + decisions[i].Source.Key
		right := decisions[j].Source.Project + "\x00" + decisions[j].Source.Type + "\x00" + decisions[j].Source.Key
		return left < right
	})
	return decisions, nil
}

func indexRebuildDecisions(supplied []MigrationDecision, required []MigrationDecision) (map[string]MigrationDecision, []MigrationBlocker) {
	blockers := make([]MigrationBlocker, 0)
	requiredByKey := make(map[string]MigrationDecision, len(required))
	for _, item := range required {
		requiredByKey[decisionLookupKey(item.Source.Project, item.Source.Type, item.Source.Key)] = item
	}
	index := make(map[string]MigrationDecision)
	seen := make(map[string]bool)
	for _, decision := range supplied {
		key := decisionLookupKey(decision.Source.Project, decision.Source.Type, decision.Source.Key)
		if seen[key] {
			blockers = append(blockers, MigrationBlocker{
				Code:    "duplicate_decision",
				Message: "Duplicate operator decision for the same migration source.",
				Path:    decision.Source.Project + "/" + decision.Source.Type + "/" + decision.Source.Key,
			})
			continue
		}
		seen[key] = true
		req, ok := requiredByKey[key]
		if !ok {
			// Distinguish unknown source key vs wrong project/type.
			code := "unknown_decision"
			if decision.Source.Project != "" && decision.Source.Type != "" {
				// wrong_source when type is known but key/project is not required.
				code = "wrong_source"
				if decision.Source.Type != "observation" && decision.Source.Type != "hypothesis" && decision.Source.Type != "project_directive" {
					code = "unknown_decision"
				}
			}
			blockers = append(blockers, MigrationBlocker{
				Code:    code,
				Message: "Operator decision does not match a required migration source.",
				Path:    decision.Source.Project + "/" + decision.Source.Type + "/" + decision.Source.Key,
			})
			continue
		}
		if !stringListContains(req.AllowedActions, decision.Decision) {
			blockers = append(blockers, MigrationBlocker{
				Code:    "disallowed_decision",
				Message: "Operator decision is not in the closed allowed action set.",
				Path:    decision.Source.Project + "/" + decision.Source.Type + "/" + decision.Source.Key,
			})
			continue
		}
		// Preserve allowed actions from required set for diagnostics.
		decision.AllowedActions = append([]string(nil), req.AllowedActions...)
		index[key] = decision
	}
	for key, req := range requiredByKey {
		if _, ok := index[key]; !ok {
			// Missing is also reported when mapping tries to use the decision;
			// still surface a stable missing_decision here for complete diagnostics.
			if _, supplied := seen[key]; !supplied {
				blockers = append(blockers, MigrationBlocker{
					Code:    "missing_decision",
					Message: "Required operator decision is missing.",
					Path:    req.Source.Project + "/" + req.Source.Type + "/" + req.Source.Key,
				})
			}
		}
	}
	return index, blockers
}

func decisionLookupKey(project, sourceType, key string) string {
	return project + "\x00" + sourceType + "\x00" + key
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringListContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func filterEdgesForProject(edges []rebuildEdge, projectID string) []rebuildEdge {
	out := make([]rebuildEdge, 0)
	for _, edge := range edges {
		if edge.projectID == projectID {
			out = append(out, edge)
		}
	}
	return out
}

type rebuildPendingRelation struct {
	projectID string
	fromKey   string
	relation  string
	toKey     string
	version   int
	reason    string
	history   []rebuildRelationHistory
}

type rebuildCycleEdge struct{ from, to string }

func detectRebuildRelationCycles(relations []rebuildPendingRelation) []MigrationBlocker {
	// Acyclic policies match v2 grammar: part_of, derived_from, depends_on, supersedes,
	// and Project-Fact-to-Project-Fact supports. Rebuild only needs cycle detection for
	// relations it writes; endpoint types already passed grammar checks.
	blockers := make([]MigrationBlocker, 0)
	byProjectRelation := make(map[string][]rebuildCycleEdge)
	for _, rel := range relations {
		switch rel.relation {
		case "part_of", "derived_from", "depends_on", "supersedes", "supports":
			key := rel.projectID + "\x00" + rel.relation
			byProjectRelation[key] = append(byProjectRelation[key], rebuildCycleEdge{from: rel.fromKey, to: rel.toKey})
		}
	}
	for key, edges := range byProjectRelation {
		parts := strings.SplitN(key, "\x00", 2)
		projectID, relation := parts[0], parts[1]
		if relationWouldCycle(edges) {
			blockers = append(blockers, MigrationBlocker{
				Code:    "relationship_cycle",
				Message: "Rebuilt " + relation + " relationships form a cycle under the v2 grammar.",
				Path:    projectID + "/relationship/" + relation,
			})
		}
	}
	return blockers
}

func relationWouldCycle(edges []rebuildCycleEdge) bool {
	adj := make(map[string][]string)
	nodes := make(map[string]bool)
	for _, edge := range edges {
		adj[edge.from] = append(adj[edge.from], edge.to)
		nodes[edge.from] = true
		nodes[edge.to] = true
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(nodes))
	var visit func(string) bool
	visit = func(node string) bool {
		color[node] = gray
		for _, next := range adj[node] {
			switch color[next] {
			case gray:
				return true
			case white:
				if visit(next) {
					return true
				}
			}
		}
		color[node] = black
		return false
	}
	for node := range nodes {
		if color[node] == white && visit(node) {
			return true
		}
	}
	return false
}

func persistRebuildScopeLimits(ctx context.Context, tx *sql.Tx, projectID string, limits []string, now string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_rebuild_scope_limits WHERE project_id=?`, projectID); err != nil {
		return fmt.Errorf("clear staged scope limits for %s: %w", projectID, err)
	}
	seen := make(map[string]bool)
	for _, limit := range limits {
		limit = strings.TrimSpace(limit)
		if limit == "" || seen[limit] {
			continue
		}
		seen[limit] = true
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO blackboard_v2_rebuild_scope_limits(project_id, limit_text, created_at)
			VALUES (?, ?, ?)`, projectID, limit, now); err != nil {
			return fmt.Errorf("stage scope limit for %s: %w", projectID, err)
		}
	}
	return nil
}

func chooseTargetKey(projectID, v2Type, sourceKey string, usedKeys map[string]string) (string, string) {
	if isConformingV2Key(sourceKey) {
		if _, taken := usedKeys[sourceKey]; !taken {
			return sourceKey, "retain"
		}
	}
	return deterministicMigratedKey(projectID, v2Type, sourceKey), "rename"
}

func isConformingV2Key(key string) bool {
	if key == "" || len(key) > 96 {
		return false
	}
	for _, r := range key {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	// Opaque shapes that embed internal identity/hash material are renamed
	// even when they fit the length/ASCII rules.
	if strings.HasPrefix(key, "mig_") {
		return false
	}
	if strings.Contains(key, "\x00") {
		return false
	}
	// Pure 32+ hex blobs (common opaque/hash keys) are not human-readable.
	if len(key) >= 32 && isHex(key) {
		return false
	}
	return true
}

func isHex(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func deterministicMigratedKey(projectID, v2Type, sourceKey string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + v2Type + "\x00" + sourceKey))
	return "migrated:" + v2Type + ":" + hex.EncodeToString(sum[:16])
}

func normalizeAliasKey(projectID, sourceType, aliasKey string, assigns map[string]*assignedKey) string {
	// If an imported assignment already retained/renamed this alias source key,
	// reuse that mapping.
	for _, assign := range assigns {
		if assign.sourceKey == aliasKey {
			return assign.targetKey
		}
	}
	if isConformingV2Key(aliasKey) {
		// Avoid colliding with an existing target key in this Project.
		for _, assign := range assigns {
			if assign.targetKey == aliasKey {
				return deterministicMigratedKey(projectID, mapSourceTypeName(sourceType), aliasKey)
			}
		}
		return aliasKey
	}
	return deterministicMigratedKey(projectID, mapSourceTypeName(sourceType), aliasKey)
}

func mapSourceTypeName(sourceType string) string {
	switch sourceType {
	case string(blackboard.NodeTypeExplorationObjective):
		return "objective"
	case string(blackboard.NodeTypeProjectFact):
		return "fact"
	case string(blackboard.NodeTypeEvidenceArtifact):
		return "evidence"
	default:
		return sourceType
	}
}

func mapNodeType(nodeType blackboard.NodeType) (string, bool) {
	switch nodeType {
	case blackboard.NodeTypeEntity:
		return "entity", true
	case blackboard.NodeTypeExplorationObjective:
		return "objective", true
	case blackboard.NodeTypeAttempt:
		return "attempt", true
	case blackboard.NodeTypeProjectFact:
		return "fact", true
	case blackboard.NodeTypeFinding:
		return "finding", true
	case blackboard.NodeTypeSolution:
		return "solution", true
	case blackboard.NodeTypeEvidenceArtifact:
		return "evidence", true
	default:
		return "", false
	}
}

func v2TypeOrder(nodeType blackboard.NodeType) int {
	switch nodeType {
	case blackboard.NodeTypeEntity:
		return 0
	case blackboard.NodeTypeExplorationObjective:
		return 1
	case blackboard.NodeTypeAttempt:
		return 2
	case blackboard.NodeTypeProjectFact:
		return 3
	case blackboard.NodeTypeFinding:
		return 4
	case blackboard.NodeTypeSolution:
		return 5
	case blackboard.NodeTypeEvidenceArtifact:
		return 6
	case blackboard.NodeTypeObservation:
		return 7
	case blackboard.NodeTypeHypothesis:
		return 8
	case blackboard.NodeTypeProjectDirective:
		return 9
	case blackboard.NodeTypeGoal:
		return 10
	default:
		return 100
	}
}

func mapSemanticRecord(v2Type string, props map[string]any) (any, bool, error) {
	switch v2Type {
	case "entity":
		status := stringProp(props, "status")
		if status == "" {
			status = "active"
		}
		record := blackboardv2.EntityRecord{
			Status:        status,
			Kind:          stringProp(props, "kind"),
			Name:          stringProp(props, "name"),
			Locator:       stringProp(props, "locator"),
			Description:   stringProp(props, "description"),
			ScopeStatus:   stringProp(props, "scope_status"),
			CredentialRef: stringProp(props, "credential_ref"),
		}
		if record.Kind == "" || record.Name == "" || record.ScopeStatus == "" {
			return nil, false, fmt.Errorf("entity is missing required closed fields")
		}
		current := status == "active"
		return record, current, nil
	case "objective":
		status := stringProp(props, "status")
		if status == "" {
			status = "open"
		}
		record := blackboardv2.ObjectiveRecord{
			Status:            status,
			Objective:         stringProp(props, "objective"),
			ResolutionSummary: stringProp(props, "resolution_summary"),
		}
		if strings.TrimSpace(record.Objective) == "" {
			return nil, false, fmt.Errorf("objective text is required")
		}
		return record, status == "open", nil
	case "attempt":
		status := stringProp(props, "status")
		if status == "" {
			status = "open"
		}
		summary := stringProp(props, "summary")
		if status == "open" && summary == "" {
			summary = "Open Attempt"
		}
		record := blackboardv2.AttemptRecord{Status: status, Summary: summary}
		if status != "open" && strings.TrimSpace(summary) == "" {
			return nil, false, fmt.Errorf("terminal attempt requires summary")
		}
		return record, status == "open", nil
	case "fact":
		confidence := stringProp(props, "confidence")
		if confidence == "" {
			confidence = "tentative"
		}
		category := stringProp(props, "category")
		if category == "" {
			category = "uncategorized"
		}
		scope := stringProp(props, "scope_status")
		if scope == "" {
			scope = "unknown"
		}
		record := blackboardv2.FactRecord{
			Category:    category,
			Summary:     stringProp(props, "summary"),
			Body:        stringProp(props, "body"),
			Confidence:  confidence,
			ScopeStatus: scope,
		}
		if strings.TrimSpace(record.Summary) == "" {
			return nil, false, fmt.Errorf("fact summary is required")
		}
		if confidence != "tentative" && confidence != "confirmed" && confidence != "deprecated" {
			return nil, false, fmt.Errorf("unknown fact confidence %q", confidence)
		}
		return record, confidence == "tentative" || confidence == "confirmed", nil
	case "finding":
		status := stringProp(props, "status")
		if status == "" {
			status = "unconfirmed"
		}
		record := blackboardv2.FindingRecord{
			Status:         status,
			Title:          stringProp(props, "title"),
			Target:         stringProp(props, "target"),
			Description:    stringProp(props, "description"),
			Proof:          stringProp(props, "proof"),
			Impact:         stringProp(props, "impact"),
			Recommendation: stringProp(props, "recommendation"),
			CVSSVersion:    stringProp(props, "cvss_version"),
			CVSSVector:     stringProp(props, "cvss_vector"),
		}
		if strings.TrimSpace(record.Title) == "" {
			return nil, false, fmt.Errorf("finding title is required")
		}
		// Do not copy derived severity/cvss_pending.
		return record, status == "unconfirmed" || status == "confirmed", nil
	case "solution":
		status := stringProp(props, "status")
		if status == "" {
			status = "candidate"
		}
		record := blackboardv2.SolutionRecord{
			Status:              status,
			Kind:                stringProp(props, "kind"),
			Summary:             stringProp(props, "summary"),
			Value:               stringProp(props, "value"),
			VerificationSummary: stringProp(props, "verification_summary"),
		}
		if record.Kind == "" || strings.TrimSpace(record.Summary) == "" {
			return nil, false, fmt.Errorf("solution kind and summary are required")
		}
		return record, status == "candidate" || status == "verified", nil
	case "evidence":
		status := stringProp(props, "status")
		if status == "" {
			status = "available"
		}
		size := int64Prop(props, "size_bytes")
		record := blackboardv2.EvidenceRecord{
			Status:       status,
			ArtifactType: stringProp(props, "artifact_type"),
			Summary:      stringProp(props, "summary"),
			MediaType:    stringProp(props, "media_type"),
			SourcePath:   stringProp(props, "source_path"),
			ManagedPath:  stringProp(props, "managed_path"),
			SHA256:       strings.ToLower(stringProp(props, "sha256")),
			Size:         size,
			CapturedAt:   normalizeCapturedAt(stringProp(props, "captured_at")),
		}
		if record.ArtifactType == "" || record.ManagedPath == "" || strings.TrimSpace(record.Summary) == "" {
			return nil, false, fmt.Errorf("evidence requires artifact_type, managed_path, and summary")
		}
		if status != "missing" && status != "superseded" {
			if len(record.SHA256) != 64 || !isHex(record.SHA256) {
				return nil, false, fmt.Errorf("available evidence requires sha256")
			}
		}
		if record.SHA256 == "" {
			// missing evidence may lack a digest; store empty digest only when missing.
			if status == "missing" {
				record.SHA256 = strings.Repeat("0", 64)
			}
		}
		return record, status == "available" || status == "missing", nil
	default:
		return nil, false, fmt.Errorf("unsupported v2 type %q", v2Type)
	}
}

func terminalSemanticRecord(v2Type string, record any) any {
	switch v2Type {
	case "entity":
		value, ok := record.(blackboardv2.EntityRecord)
		if ok {
			value.Status = "superseded"
			return value
		}
	case "objective":
		value, ok := record.(blackboardv2.ObjectiveRecord)
		if ok {
			value.Status = "superseded"
			return value
		}
	case "attempt":
		value, ok := record.(blackboardv2.AttemptRecord)
		if ok {
			value.Status = "blocked"
			return value
		}
	case "fact":
		value, ok := record.(blackboardv2.FactRecord)
		if ok {
			value.Confidence = "deprecated"
			return value
		}
	case "finding":
		value, ok := record.(blackboardv2.FindingRecord)
		if ok {
			value.Status = "superseded"
			return value
		}
	case "solution":
		value, ok := record.(blackboardv2.SolutionRecord)
		if ok {
			value.Status = "superseded"
			return value
		}
	case "evidence":
		value, ok := record.(blackboardv2.EvidenceRecord)
		if ok {
			value.Status = "superseded"
			return value
		}
	}
	return record
}

func rebuildRelationHistoryFor(edge rebuildEdge) []rebuildRelationHistory {
	history := make([]rebuildRelationHistory, 0, len(edge.prior))
	for _, prior := range edge.prior {
		if prior.state != "active" || prior.version >= edge.version {
			continue
		}
		history = append(history, rebuildRelationHistory{
			version:    prior.version,
			reason:     prior.summary,
			recordedAt: coalesceString(prior.updatedAt, edge.updatedAt),
		})
	}
	return history
}

func normalizeCapturedAt(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if _, err := time.Parse(time.RFC3339, value); err == nil {
		return value
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC().Format(time.RFC3339)
	}
	return ""
}

func stringProp(props map[string]any, key string) string {
	if props == nil {
		return ""
	}
	value, ok := props[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

func int64Prop(props map[string]any, key string) int64 {
	if props == nil {
		return 0
	}
	value, ok := props[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	case json.Number:
		n, _ := typed.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(typed, 10, 64)
		return n
	default:
		return 0
	}
}

func loadRebuildSourceRecords(ctx context.Context, tx *sql.Tx) ([]rebuildSourceRecord, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT h.project_id, h.node_id, h.node_type, n.original_stable_key, h.version, h.disposition, v.properties_json, v.updated_at
		FROM blackboard_node_heads h
		JOIN blackboard_nodes n ON n.project_id=h.project_id AND n.id=h.node_id
		JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version
		WHERE h.node_type IN (
			'entity','exploration_objective','attempt','project_fact','finding','solution','evidence_artifact',
			'goal','observation','hypothesis','project_directive'
		)
		ORDER BY h.project_id, h.node_type, n.original_stable_key, h.node_id`)
	if err != nil {
		return nil, fmt.Errorf("load graph_v1 heads for rebuild: %w", err)
	}
	defer rows.Close()
	records := make([]rebuildSourceRecord, 0)
	for rows.Next() {
		var record rebuildSourceRecord
		var propsRaw string
		var nodeType string
		if err := rows.Scan(&record.projectID, &record.nodeID, &nodeType, &record.sourceKey, &record.version, &record.disposition, &propsRaw, &record.updatedAt); err != nil {
			return nil, err
		}
		record.nodeType = blackboard.NodeType(nodeType)
		if err := json.Unmarshal([]byte(propsRaw), &record.properties); err != nil {
			return nil, fmt.Errorf("decode node properties for %s: %w", record.nodeID, err)
		}
		priors, err := loadPriorVersions(ctx, tx, record.projectID, record.nodeID, record.version)
		if err != nil {
			return nil, err
		}
		record.priorVersions = priors
		records = append(records, record)
	}
	return records, rows.Err()
}

func loadPriorVersions(ctx context.Context, tx *sql.Tx, projectID, nodeID string, currentVersion int) ([]rebuildVersion, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT version, properties_json, updated_at
		FROM blackboard_node_versions
		WHERE project_id=? AND node_id=? AND version<?
		ORDER BY version`, projectID, nodeID, currentVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := make([]rebuildVersion, 0)
	for rows.Next() {
		var item rebuildVersion
		var propsRaw string
		if err := rows.Scan(&item.version, &propsRaw, &item.updatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(propsRaw), &item.properties); err != nil {
			return nil, err
		}
		versions = append(versions, item)
	}
	return versions, rows.Err()
}

func loadRebuildSourceEdges(ctx context.Context, tx *sql.Tx) ([]rebuildEdge, []MigrationBlocker, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT h.project_id, h.edge_id, h.edge_type, h.from_node_id, h.to_node_id, h.version, COALESCE(v.summary,''), COALESCE(v.updated_at,'')
		FROM blackboard_edge_heads h
		LEFT JOIN blackboard_edge_versions v
			ON v.project_id=h.project_id AND v.edge_id=h.edge_id AND v.version=h.version
		WHERE h.state='active'
		ORDER BY h.project_id, h.edge_type, h.from_node_id, h.to_node_id`)
	if err != nil {
		return nil, nil, fmt.Errorf("load graph_v1 edges for rebuild: %w", err)
	}
	defer rows.Close()
	edges := make([]rebuildEdge, 0)
	blockers := make([]MigrationBlocker, 0)
	for rows.Next() {
		var edge rebuildEdge
		var edgeType string
		if err := rows.Scan(&edge.projectID, &edge.edgeID, &edgeType, &edge.fromID, &edge.toID, &edge.version, &edge.summary, &edge.updatedAt); err != nil {
			return nil, nil, err
		}
		edge.edgeType = blackboard.EdgeType(edgeType)
		prior, err := loadPriorEdgeVersions(ctx, tx, edge.projectID, edge.edgeID, edge.version)
		if err != nil {
			return nil, nil, err
		}
		edge.prior = prior
		if blocker, err := crossProjectEndpointBlocker(ctx, tx, edge.projectID, edge.fromID, "from"); err != nil {
			return nil, nil, err
		} else if blocker != nil {
			blockers = append(blockers, *blocker)
			continue
		}
		if blocker, err := crossProjectEndpointBlocker(ctx, tx, edge.projectID, edge.toID, "to"); err != nil {
			return nil, nil, err
		} else if blocker != nil {
			blockers = append(blockers, *blocker)
			continue
		}
		edges = append(edges, edge)
	}
	return edges, blockers, rows.Err()
}

func loadPriorEdgeVersions(ctx context.Context, tx *sql.Tx, projectID, edgeID string, currentVersion int) ([]rebuildEdgeVersion, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT version, state, COALESCE(summary,''), COALESCE(updated_at,'')
		FROM blackboard_edge_versions
		WHERE project_id=? AND edge_id=? AND version<?
		ORDER BY version`, projectID, edgeID, currentVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := make([]rebuildEdgeVersion, 0)
	for rows.Next() {
		var item rebuildEdgeVersion
		if err := rows.Scan(&item.version, &item.state, &item.summary, &item.updatedAt); err != nil {
			return nil, err
		}
		history = append(history, item)
	}
	return history, rows.Err()
}

func crossProjectEndpointBlocker(ctx context.Context, tx *sql.Tx, projectID, nodeID, side string) (*MigrationBlocker, error) {
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT project_id FROM blackboard_nodes WHERE id=?`, nodeID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return &MigrationBlocker{
			Code:    "invalid_relationship_endpoint",
			Message: "Relationship endpoint does not resolve inside the Project.",
			Path:    projectID + "/relationship/" + side,
		}, nil
	}
	if err != nil {
		return nil, err
	}
	if owner != projectID {
		return &MigrationBlocker{
			Code:    "cross_project_reference",
			Message: "Cross-Project relationship endpoints are migration blockers.",
			Path:    projectID + "/relationship/" + side,
		}, nil
	}
	return nil, nil
}

func loadRebuildSourceAliases(ctx context.Context, tx *sql.Tx) ([]rebuildAlias, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT project_id, node_type, key, canonical_node_id, source_node_id
		FROM blackboard_key_registry
		WHERE role IN ('alias','merged_alias') OR (source_node_id <> '' AND source_node_id <> canonical_node_id)
		ORDER BY project_id, node_type, key`)
	if err != nil {
		// Role vocabulary may vary; treat missing table content as no aliases.
		if strings.Contains(err.Error(), "no such column") || strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("load graph_v1 key aliases for rebuild: %w", err)
	}
	defer rows.Close()
	aliases := make([]rebuildAlias, 0)
	for rows.Next() {
		var alias rebuildAlias
		var nodeType string
		if err := rows.Scan(&alias.projectID, &nodeType, &alias.aliasKey, &alias.canonicalID, &alias.sourceNodeID); err != nil {
			return nil, err
		}
		alias.nodeType = blackboard.NodeType(nodeType)
		aliases = append(aliases, alias)
	}
	return aliases, rows.Err()
}

func listProjectIDs(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func loadProjectKind(ctx context.Context, tx *sql.Tx, projectID string) (string, error) {
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM projects WHERE id=?`, projectID).Scan(&kind); err != nil {
		return "", err
	}
	if kind == "" {
		kind = "pentest"
	}
	return kind, nil
}

func filterRecordsForProject(records []rebuildSourceRecord, projectID string) []rebuildSourceRecord {
	out := make([]rebuildSourceRecord, 0)
	for _, record := range records {
		if record.projectID == projectID {
			out = append(out, record)
		}
	}
	return out
}

func clearDisposableV2State(ctx context.Context, tx *sql.Tx, projects []string) error {
	// Project-scoped wipe of disposable v2 semantic state only.
	tables := []string{
		"blackboard_v2_relationships",
		"blackboard_v2_relationship_history",
		"blackboard_v2_records",
		"blackboard_v2_record_history",
		"blackboard_v2_key_redirects",
		"blackboard_v2_project_state",
		"blackboard_v2_idempotency_receipts",
		"blackboard_v2_rebuild_scope_limits",
	}
	for _, projectID := range projects {
		for _, table := range tables {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE project_id=?`, projectID); err != nil {
				// Table may not exist on pure legacy fixtures until ensureDisposableV2Tables.
				if strings.Contains(err.Error(), "no such table") {
					continue
				}
				return fmt.Errorf("clear disposable v2 table %s for %s: %w", table, projectID, err)
			}
		}
	}
	return nil
}

func ensureDisposableV2Tables(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS blackboard_v2_project_state (
			project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
			revision INTEGER NOT NULL CHECK (revision >= 0)
		)`,
		`CREATE TABLE IF NOT EXISTS blackboard_v2_records (
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			key TEXT NOT NULL,
			type TEXT NOT NULL CHECK (type IN ('entity','objective','attempt','fact','finding','solution','evidence')),
			version INTEGER NOT NULL CHECK (version >= 1),
			record_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, key)
		)`,
		`CREATE TABLE IF NOT EXISTS blackboard_v2_record_history (
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			key TEXT NOT NULL,
			version INTEGER NOT NULL CHECK (version >= 1),
			type TEXT NOT NULL CHECK (type IN ('entity','objective','attempt','fact','finding','solution','evidence')),
			record_json TEXT NOT NULL,
			recorded_at TEXT NOT NULL,
			PRIMARY KEY (project_id, key, version)
		)`,
		`CREATE TABLE IF NOT EXISTS blackboard_v2_relationships (
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			from_key TEXT NOT NULL,
			relation TEXT NOT NULL CHECK (relation IN ('about','part_of','tests','produced','evidences','supports','contradicts','derived_from','depends_on','satisfies','supersedes')),
			to_key TEXT NOT NULL,
			version INTEGER NOT NULL CHECK (version >= 1),
			reason TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, from_key, relation, to_key)
		)`,
		`CREATE TABLE IF NOT EXISTS blackboard_v2_relationship_history (
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			from_key TEXT NOT NULL,
			relation TEXT NOT NULL CHECK (relation IN ('about','part_of','tests','produced','evidences','supports','contradicts','derived_from','depends_on','satisfies','supersedes')),
			to_key TEXT NOT NULL,
			version INTEGER NOT NULL CHECK (version >= 1),
			reason TEXT NOT NULL DEFAULT '',
			recorded_at TEXT NOT NULL,
			PRIMARY KEY (project_id, from_key, relation, to_key, version)
		)`,
		`CREATE TABLE IF NOT EXISTS blackboard_v2_key_redirects (
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			source_key TEXT NOT NULL,
			canonical_key TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (project_id, source_key)
		)`,
		`CREATE TABLE IF NOT EXISTS blackboard_v2_idempotency_receipts (
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			idempotency_key TEXT NOT NULL,
			request_hash TEXT NOT NULL,
			result_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (project_id, idempotency_key)
		)`,
		`CREATE TABLE IF NOT EXISTS blackboard_v2_rebuild_mappings (
			project_id TEXT NOT NULL,
			source_type TEXT NOT NULL,
			source_key TEXT NOT NULL,
			action TEXT NOT NULL,
			target_key TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (project_id, source_type, source_key)
		)`,
		`CREATE TABLE IF NOT EXISTS blackboard_v2_rebuild_scope_limits (
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			limit_text TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (project_id, limit_text)
		)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("ensure disposable v2 schema: %w", err)
		}
	}
	return nil
}

func persistRebuildMappings(ctx context.Context, tx *sql.Tx, projectID string, mappings []MigrationMapping, now string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_rebuild_mappings WHERE project_id=?`, projectID); err != nil {
		return fmt.Errorf("clear rebuild mappings for %s: %w", projectID, err)
	}
	for _, mapping := range mappings {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO blackboard_v2_rebuild_mappings (project_id, source_type, source_key, action, target_key, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			projectID, mapping.SourceType, mapping.SourceKey, mapping.Action, mapping.TargetKey, now,
		); err != nil {
			return fmt.Errorf("store rebuild mapping %s/%s: %w", mapping.SourceType, mapping.SourceKey, err)
		}
	}
	return nil
}

func isV2Relation(relation string) bool {
	for _, rule := range blackboardv2grammar.Rules() {
		if rule.Relation == relation {
			return true
		}
	}
	return false
}

func relationRule(relation string) (blackboardv2grammar.Rule, bool) {
	for _, rule := range blackboardv2grammar.Rules() {
		if rule.Relation == relation {
			return rule, true
		}
	}
	return blackboardv2grammar.Rule{}, false
}

func validateReason(reason, from, to string) string {
	if !utf8Valid(reason) {
		return "invalid_relationship_reason"
	}
	if len([]byte(reason)) > blackboardv2grammar.MaxReasonBytes {
		return "invalid_relationship_reason"
	}
	if reason == from || reason == to {
		return "invalid_relationship_reason"
	}
	return ""
}

func utf8Valid(value string) bool {
	return strings.ToValidUTF8(value, "") == value
}

func mustJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func coalesceString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func coalesceTime(history []rebuildHistoryItem, fallback string) string {
	if len(history) == 0 {
		return fallback
	}
	return coalesceString(history[len(history)-1].at, fallback)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
