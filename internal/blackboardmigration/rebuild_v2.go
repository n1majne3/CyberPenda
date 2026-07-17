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

func (s *Service) rebuildUnambiguousHeads(ctx context.Context) (RebuildResultV1, error) {
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

	if err := ensureDisposableV2Tables(ctx, tx); err != nil {
		return RebuildResultV1{}, err
	}

	records, err := loadRebuildSourceRecords(ctx, tx)
	if err != nil {
		return RebuildResultV1{}, err
	}
	edges, edgeBlockers, err := loadRebuildSourceEdges(ctx, tx)
	if err != nil {
		return RebuildResultV1{}, err
	}
	aliases, err := loadRebuildSourceAliases(ctx, tx)
	if err != nil {
		return RebuildResultV1{}, err
	}

	projects, err := listProjectIDs(ctx, tx)
	if err != nil {
		return RebuildResultV1{}, err
	}

	// Clear disposable v2 state only (never touch v1 graph/legacy tables).
	if err := clearDisposableV2State(ctx, tx, projects); err != nil {
		return RebuildResultV1{}, err
	}

	blockers := append([]MigrationBlocker{}, edgeBlockers...)
	assignments := make(map[string]map[string]*assignedKey) // project -> nodeID -> assignment
	mappings := make([]MigrationMapping, 0)
	projectMappings := make(map[string][]MigrationMapping)

	for _, projectID := range projects {
		kind, err := loadProjectKind(ctx, tx, projectID)
		if err != nil {
			return RebuildResultV1{}, err
		}

		projectRecords := filterRecordsForProject(records, projectID)
		projectAssignments, mapped, projectBlockers := assignProjectKeys(projectID, kind, projectRecords)
		blockers = append(blockers, projectBlockers...)
		assignments[projectID] = projectAssignments
		projectMappings[projectID] = mapped
		mappings = append(mappings, mapped...)
	}

	// Map relationships after keys are assigned so endpoints resolve.
	now := s.clock().UTC().Format(time.RFC3339Nano)
	type pendingRelation struct {
		projectID string
		fromKey   string
		relation  string
		toKey     string
		version   int
		reason    string
		history   []rebuildRelationHistory
	}
	relations := make([]pendingRelation, 0)
	redirects := make([]struct {
		projectID, source, canonical string
	}, 0)

	for _, edge := range edges {
		fromAssign, fromOK := assignments[edge.projectID][edge.fromID]
		toAssign, toOK := assignments[edge.projectID][edge.toID]
		if !fromOK || !toOK {
			// Endpoints that are removed types or non-imported heads are #122 /
			// non-reusable and are skipped without guessing.
			continue
		}
		relation := string(edge.edgeType)
		switch relation {
		case "blocks", "leads_to":
			// Explicitly owned by #122; do not map or invent replacements here.
			continue
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
		if !ok || !rule.Allows(fromAssign.v2Type, toAssign.v2Type) {
			blockers = append(blockers, MigrationBlocker{
				Code:    "invalid_relationship_endpoints",
				Message: "Relationship endpoints are not valid under the v2 grammar.",
				Path:    edge.projectID + "/" + fromAssign.targetKey + "/" + relation + "/" + toAssign.targetKey,
			})
			continue
		}
		if fromAssign.targetKey == toAssign.targetKey {
			blockers = append(blockers, MigrationBlocker{
				Code:    "invalid_relationship_self_link",
				Message: "Self-linked relationships are rejected.",
				Path:    edge.projectID + "/" + fromAssign.targetKey + "/" + relation,
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
			relations = append(relations, pendingRelation{
				projectID: edge.projectID,
				fromKey:   fromAssign.targetKey,
				relation:  relation,
				toKey:     toAssign.targetKey,
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
				if errCode := validateReason(reason, fromAssign.targetKey, toAssign.targetKey); errCode != "" {
					blockers = append(blockers, MigrationBlocker{
						Code:    errCode,
						Message: "Relationship reason is invalid under the v2 contract.",
						Path:    edge.projectID + "/" + fromAssign.targetKey + "/" + relation + "/" + toAssign.targetKey,
					})
					continue
				}
			}
		}
		relations = append(relations, pendingRelation{
			projectID: edge.projectID,
			fromKey:   fromAssign.targetKey,
			relation:  relation,
			toKey:     toAssign.targetKey,
			version:   max(1, edge.version),
			reason:    reason,
			history:   rebuildRelationHistoryFor(edge),
		})
	}

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
		}, ErrRebuildBlocked
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
					return RebuildResultV1{}, fmt.Errorf("store rebuilt history for %s/%s@%d: %w", projectID, assign.targetKey, item.version, err)
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
					return RebuildResultV1{}, fmt.Errorf("store terminal rebuilt history for %s/%s: %w", projectID, assign.targetKey, err)
				}
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO blackboard_v2_records (project_id, key, type, version, record_json, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				projectID, assign.targetKey, assign.v2Type, assign.version, mustJSON(assign.record), now, now,
			); err != nil {
				return RebuildResultV1{}, fmt.Errorf("store rebuilt record %s/%s: %w", projectID, assign.targetKey, err)
			}
			revision++
		}

		// Relationships for this project.
		for _, rel := range relations {
			if rel.projectID != projectID {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO blackboard_v2_relationships (project_id, from_key, relation, to_key, version, reason, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				projectID, rel.fromKey, rel.relation, rel.toKey, rel.version, rel.reason, now, now,
			); err != nil {
				return RebuildResultV1{}, fmt.Errorf("store rebuilt relationship %s %s %s: %w", rel.fromKey, rel.relation, rel.toKey, err)
			}
			for _, historical := range rel.history {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO blackboard_v2_relationship_history (project_id, from_key, relation, to_key, version, reason, recorded_at)
					VALUES (?, ?, ?, ?, ?, ?, ?)
					ON CONFLICT(project_id, from_key, relation, to_key, version) DO NOTHING`,
					projectID, rel.fromKey, rel.relation, rel.toKey, historical.version, historical.reason, historical.recordedAt,
				); err != nil {
					return RebuildResultV1{}, fmt.Errorf("store rebuilt relationship history %s %s %s@%d: %w", rel.fromKey, rel.relation, rel.toKey, historical.version, err)
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
				return RebuildResultV1{}, fmt.Errorf("store rebuilt key redirect %s -> %s: %w", redirect.source, redirect.canonical, err)
			}
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO blackboard_v2_project_state (project_id, revision)
			VALUES (?, ?)
			ON CONFLICT(project_id) DO UPDATE SET revision=excluded.revision`,
			projectID, revision,
		); err != nil {
			return RebuildResultV1{}, fmt.Errorf("store rebuilt project revision for %s: %w", projectID, err)
		}
		projectResults = append(projectResults, RebuildProjectResultV1{Project: projectID, Revision: revision})
	}

	// Persist mapping index for endpoint resolution without putting source IDs
	// into semantic payloads.
	for _, projectID := range projects {
		if err := persistRebuildMappings(ctx, tx, projectID, projectMappings[projectID], now); err != nil {
			return RebuildResultV1{}, err
		}
	}

	// Epoch must remain v1.
	var committedEpoch string
	if err := tx.QueryRowContext(ctx, `SELECT canonical_store FROM blackboard_store_state WHERE id=1`).Scan(&committedEpoch); err != nil {
		return RebuildResultV1{}, err
	}
	if committedEpoch != epoch {
		return RebuildResultV1{}, fmt.Errorf("rebuild mutated store epoch from %s to %s", epoch, committedEpoch)
	}

	if err := tx.Commit(); err != nil {
		return RebuildResultV1{}, fmt.Errorf("commit disposable v2 rebuild: %w", err)
	}

	// Validate exact conforming snapshots and detail/history through the public
	// v2 service contracts after commit.
	v2 := blackboardv2.NewServiceWithEvidence(s.db, blackboardv2.EvidenceConfig{ArtifactRoot: s.artifactRoot})
	validated := 0
	for _, project := range projectResults {
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
		// Re-encode and require exact stable bytes.
		again, err := v2.ProjectRuntimeSnapshot(ctx, project.Project)
		if err != nil {
			return RebuildResultV1{}, err
		}
		if string(again.Bytes) != string(projection.Bytes) {
			return RebuildResultV1{}, fmt.Errorf("snapshot bytes for %s are not deterministic", project.Project)
		}
		if err := validateDetailAndHistoryReads(ctx, v2, project.Project, assignments[project.Project]); err != nil {
			return RebuildResultV1{}, err
		}
		validated++
	}

	sort.Slice(mappings, func(i, j int) bool {
		left := mappings[i].SourceType + "\x00" + mappings[i].SourceKey + "\x00" + mappings[i].TargetKey
		right := mappings[j].SourceType + "\x00" + mappings[j].SourceKey + "\x00" + mappings[j].TargetKey
		return left < right
	})
	sort.Slice(projectResults, func(i, j int) bool { return projectResults[i].Project < projectResults[j].Project })

	return RebuildResultV1{
		Schema:      rebuildResultSchema,
		Status:      "rebuilt",
		StoreEpoch:  epoch,
		Projects:    projectResults,
		Mappings:    mappings,
		Validation:  MigrationValidationV1{Status: "passed", SnapshotsValidated: validated},
		SourceCount: len(records),
	}, nil
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

func assignProjectKeys(projectID, projectKind string, records []rebuildSourceRecord) (map[string]*assignedKey, []MigrationMapping, []MigrationBlocker) {
	blockers := make([]MigrationBlocker, 0)
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

	usedKeys := make(map[string]string) // targetKey -> nodeID
	assignments := make(map[string]*assignedKey, len(records))
	mappings := make([]MigrationMapping, 0, len(records))

	for _, record := range records {
		v2Type, ok := mapNodeType(record.nodeType)
		if !ok {
			continue // goals / removed types are #122
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

		history := make([]rebuildHistoryItem, 0, len(record.priorVersions))
		for _, prior := range record.priorVersions {
			if prior.version >= record.version {
				continue
			}
			priorSemantic, _, priorErr := mapSemanticRecord(v2Type, prior.properties)
			if priorErr != nil {
				// Prior versions that are not reusable under closed fields are
				// skipped rather than failing the whole rebuild when the head is good.
				continue
			}
			history = append(history, rebuildHistoryItem{
				version: prior.version,
				record:  priorSemantic,
				at:      coalesceString(prior.updatedAt, record.updatedAt),
			})
		}
		sort.Slice(history, func(i, j int) bool { return history[i].version < history[j].version })

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
	}
	return assignments, mappings, blockers
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
		WHERE h.node_type IN ('entity','exploration_objective','attempt','project_fact','finding','solution','evidence_artifact')
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
