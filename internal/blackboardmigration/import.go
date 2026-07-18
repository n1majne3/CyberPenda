package blackboardmigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"pentest/internal/blackboard"
)

var graphStableKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]{0,159}$`)

type LegacyImportResultV1 struct {
	MappingDigest   string         `json:"mapping_digest"`
	MappingVerified bool           `json:"mapping_verified"`
	ParityDigest    string         `json:"parity_digest"`
	Mappings        map[string]int `json:"mappings"`
	ParityChecks    map[string]int `json:"parity_checks"`
	Projects        int            `json:"projects"`
	Mutations       int            `json:"mutations"`
}

type legacyMapping struct {
	projectID, sourceTable, sourceKind, legacyPrimaryID string
	originalStableKey                                   string
	originalVersion                                     *int
	sourceRowHash, targetKind, targetID, status         string
	targetVersion                                       *int
	compatibilityMetadata                               map[string]any
}

type legacyFactVersion struct {
	id, key, category, summary, body, confidence, scopeStatus, createdAt string
	version                                                              int
}

type legacyFactCurrent struct {
	id, key, category, summary, body, confidence, scopeStatus, createdAt, updatedAt string
}

type legacyFindingVersion struct {
	id, key, title, description, status, target, proof, impact, recommendation string
	cvssVersion, cvssVector, createdAt                                         string
	version                                                                    int
}

type legacyFindingCurrent struct {
	id, key, title, description, status, target, proof, impact, recommendation string
	cvssVersion, cvssVector, createdAt, updatedAt                              string
	version                                                                    int
}

func (s *Service) importLegacyGraph(ctx context.Context, sourceDigest string) (LegacyImportResultV1, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LegacyImportResultV1{}, fmt.Errorf("begin M02 import transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := s.importLegacyGraphInTransaction(ctx, tx, sourceDigest, "")
	if err != nil {
		return LegacyImportResultV1{}, err
	}
	if s.commitDisposableImport {
		if err := tx.Commit(); err != nil {
			return LegacyImportResultV1{}, fmt.Errorf("commit disposable M02 import: %w", err)
		}
	}
	return result, nil
}

func (s *Service) importLegacyGraphInTransaction(ctx context.Context, tx *sql.Tx, sourceDigest, cutoverID string) (LegacyImportResultV1, error) {
	projectRows, err := tx.QueryContext(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		return LegacyImportResultV1{}, err
	}
	var projectIDs []string
	for projectRows.Next() {
		var projectID string
		if err := projectRows.Scan(&projectID); err != nil {
			projectRows.Close()
			return LegacyImportResultV1{}, err
		}
		projectIDs = append(projectIDs, projectID)
	}
	if err := projectRows.Close(); err != nil {
		return LegacyImportResultV1{}, err
	}

	graph := blackboard.NewGraphService(s.db, nil, nil)
	allMappings := make([]legacyMapping, 0)
	result := LegacyImportResultV1{Mappings: make(map[string]int), ParityChecks: make(map[string]int), Projects: len(projectIDs)}
	decoderDigests := make([]string, 0, len(projectIDs))
	for _, projectID := range projectIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET kind='pentest' WHERE id=?`, projectID); err != nil {
			return LegacyImportResultV1{}, fmt.Errorf("backfill Project kind: %w", err)
		}
		if err := backfillLegacyContinuations(ctx, tx, projectID); err != nil {
			return LegacyImportResultV1{}, err
		}
		plan, mappings, err := s.buildProjectImportPlan(ctx, tx, projectID, sourceDigest)
		if err != nil {
			return LegacyImportResultV1{}, err
		}
		allMappings = append(allMappings, mappings...)
		mutationSequence := 0
		if len(plan.Nodes) == 0 {
			if _, err := graph.InitializeLegacyImportProject(ctx, tx, projectID, s.clock().UTC().Format(time.RFC3339Nano)); err != nil {
				return LegacyImportResultV1{}, fmt.Errorf("initialize empty legacy Project %s: %w", projectID, err)
			}
		} else {
			mutation, err := graph.ApplyLegacyImportPlan(ctx, tx, plan)
			if err != nil {
				return LegacyImportResultV1{}, fmt.Errorf("import legacy Project %s through Apply: %w", projectID, err)
			}
			mutationSequence = mutation.MutationSequence
			result.Mutations++
		}
		if err := s.failCutover(CutoverFailureAfterProjectImport); err != nil {
			return LegacyImportResultV1{}, err
		}
		if len(plan.Nodes) != 0 {
			if _, err := graph.MeasureLegacyImportProject(ctx, tx, projectID, s.clock().UTC().Format(time.RFC3339Nano)); err != nil {
				return LegacyImportResultV1{}, fmt.Errorf("measure imported Project %s projection: %w", projectID, err)
			}
		}
		if err := s.failCutover(CutoverFailureAfterHeadBuild); err != nil {
			return LegacyImportResultV1{}, err
		}
		for i := range mappings {
			if err := insertLegacyMapping(ctx, tx, mappings[i], mutationSequence, cutoverID, s.clock().UTC().Format("2006-01-02T15:04:05.000000000Z07:00")); err != nil {
				return LegacyImportResultV1{}, err
			}
			result.Mappings[mappings[i].status]++
		}
		if err := s.failCutover(CutoverFailureAfterMappings); err != nil {
			return LegacyImportResultV1{}, err
		}
		decoderDigest, err := importPlanDigest(plan)
		if err != nil {
			return LegacyImportResultV1{}, err
		}
		decoderDigests = append(decoderDigests, decoderDigest)
		result.ParityChecks["offline_source_decode"]++
		if err := s.failCutover(CutoverFailureAfterParity); err != nil {
			return LegacyImportResultV1{}, err
		}
	}
	result.MappingDigest, err = legacyMappingsDigest(allMappings)
	if err != nil {
		return LegacyImportResultV1{}, err
	}
	persistedDigest, err := persistedLegacyMappingsDigest(ctx, tx)
	if err != nil {
		return LegacyImportResultV1{}, err
	}
	if persistedDigest != result.MappingDigest {
		return LegacyImportResultV1{}, fmt.Errorf("persisted legacy mapping digest mismatch: memory=%s persisted=%s", result.MappingDigest, persistedDigest)
	}
	result.MappingVerified = true
	parityHash := sha256.New()
	writeFrame(parityHash, []byte("legacy_blackboard_offline_decoder_v1"))
	for _, digest := range decoderDigests {
		writeFrame(parityHash, []byte(digest))
	}
	result.ParityDigest = hex.EncodeToString(parityHash.Sum(nil))
	return result, nil
}

func backfillLegacyContinuations(ctx context.Context, tx *sql.Tx, projectID string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT c.id,c.task_id,c.runtime_profile_id,c.started_at
		FROM task_continuations c JOIN tasks t ON t.id=c.task_id
		WHERE t.project_id=? ORDER BY c.task_id,c.number,c.id`, projectID)
	if err != nil {
		return err
	}
	type continuation struct{ id, taskID, profileID, startedAt string }
	var continuations []continuation
	for rows.Next() {
		var value continuation
		if err := rows.Scan(&value.id, &value.taskID, &value.profileID, &value.startedAt); err != nil {
			rows.Close()
			return err
		}
		continuations = append(continuations, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range continuations {
		var runtimeConfigID any
		candidateRows, err := tx.QueryContext(ctx, `
			SELECT id,created_at FROM task_runtime_config_versions
			WHERE task_id=? AND runtime_profile_id=? AND created_at<=?
			ORDER BY created_at DESC,id`, value.taskID, value.profileID, value.startedAt)
		if err != nil {
			return err
		}
		var candidates []struct{ id, createdAt string }
		for candidateRows.Next() {
			var candidate struct{ id, createdAt string }
			if err := candidateRows.Scan(&candidate.id, &candidate.createdAt); err != nil {
				candidateRows.Close()
				return err
			}
			candidates = append(candidates, candidate)
		}
		if err := candidateRows.Close(); err != nil {
			return err
		}
		if len(candidates) > 0 {
			latest := candidates[0].createdAt
			matches := 0
			for _, candidate := range candidates {
				if candidate.createdAt == latest {
					matches++
				}
			}
			if matches == 1 {
				runtimeConfigID = candidates[0].id
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task_continuations SET runtime_config_version_id=?,blackboard_reconciliation_status='legacy_not_applicable',blackboard_reconciliation_mutation_id='' WHERE id=?`, runtimeConfigID, value.id); err != nil {
			return fmt.Errorf("backfill legacy Continuation %s: %w", value.id, err)
		}
	}
	return nil
}

func (s *Service) buildProjectImportPlan(ctx context.Context, tx *sql.Tx, projectID, sourceDigest string) (blackboard.LegacyImportPlanV1, []legacyMapping, error) {
	plan := blackboard.LegacyImportPlanV1{ProjectID: projectID, ProjectKind: "pentest"}
	mappings := []legacyMapping{newLegacyMapping(projectID, "projects", "project", projectID, "", nil, map[string]any{"id": projectID, "kind": "pentest"}, "project", projectID, nil, "imported", nil)}

	taskRows, err := tx.QueryContext(ctx, `SELECT id,goal,status,created_at,updated_at FROM tasks WHERE project_id=? ORDER BY created_at,id`, projectID)
	if err != nil {
		return plan, nil, err
	}
	for taskRows.Next() {
		var id, goal, status, createdAt, updatedAt string
		if err := taskRows.Scan(&id, &goal, &status, &createdAt, &updatedAt); err != nil {
			taskRows.Close()
			return plan, nil, err
		}
		nodeID := migrationIdentity("node", projectID, "tasks", id)
		plan.Nodes = append(plan.Nodes, blackboard.LegacyImportNodeV1{
			OperationID: "goal:" + id, ID: nodeID, NodeType: blackboard.NodeTypeGoal,
			StableKey: "task:" + id + ":goal", CreatedAt: createdAt,
			Versions: []blackboard.LegacyImportNodeVersionV1{{Version: 1, Properties: map[string]any{"task_id": id, "text": goal, "task_status": status}, UpdatedAt: updatedAt}},
			Sources:  []blackboard.LegacyImportSourceV1{{Table: "tasks", PrimaryID: id}},
		})
		mappings = append(mappings, newLegacyMapping(projectID, "tasks", "task", id, "", nil, map[string]any{"id": id, "goal": goal, "status": status, "created_at": createdAt, "updated_at": updatedAt}, "goal", nodeID, intPointer(1), "imported", nil))
	}
	if err := taskRows.Close(); err != nil {
		return plan, nil, err
	}

	history, historyMappings, err := readLegacyFactHistory(ctx, tx, projectID)
	if err != nil {
		return plan, nil, err
	}
	mappings = append(mappings, historyMappings...)
	current, err := readLegacyCurrentFacts(ctx, tx, projectID)
	if err != nil {
		return plan, nil, err
	}
	keys := make(map[string]struct{}, len(history)+len(current))
	for key := range history {
		keys[key] = struct{}{}
	}
	for key := range current {
		keys[key] = struct{}{}
	}
	orderedKeys := make([]string, 0, len(keys))
	for key := range keys {
		orderedKeys = append(orderedKeys, key)
	}
	sort.Strings(orderedKeys)
	nodeByLegacyKey := make(map[string]blackboard.LegacyImportNodeV1, len(orderedKeys))
	for _, key := range orderedKeys {
		versions := append([]legacyFactVersion(nil), history[key]...)
		currentFact, hasCurrent := current[key]
		if hasCurrent {
			currentProperties := normalizedFactProperties(currentFact.category, currentFact.summary, currentFact.body, currentFact.confidence, currentFact.scopeStatus)
			if len(versions) == 0 {
				versions = append(versions, legacyFactVersion{id: currentFact.id, key: key, version: 1, category: currentFact.category, summary: currentFact.summary, body: currentFact.body, confidence: currentFact.confidence, scopeStatus: currentFact.scopeStatus, createdAt: currentFact.updatedAt})
			} else {
				last := versions[len(versions)-1]
				lastProperties := normalizedFactProperties(last.category, last.summary, last.body, last.confidence, last.scopeStatus)
				if !reflect.DeepEqual(lastProperties, currentProperties) {
					versions = append(versions, legacyFactVersion{id: currentFact.id, key: key, version: last.version + 1, category: currentFact.category, summary: currentFact.summary, body: currentFact.body, confidence: currentFact.confidence, scopeStatus: currentFact.scopeStatus, createdAt: currentFact.updatedAt})
				}
			}
		}
		if len(versions) == 0 {
			continue
		}
		nodeID := migrationIdentity("node", projectID, "project_fact_versions", key)
		createdAt := versions[0].createdAt
		if hasCurrent && currentFact.id != "" && legacyIDGloballyUnique(ctx, tx, currentFact.id) {
			nodeID = currentFact.id
			createdAt = currentFact.createdAt
		}
		stableKey := normalizedLegacyStableKey(projectID, "fact", key)
		node := blackboard.LegacyImportNodeV1{OperationID: "fact:" + shortHash(key), ID: nodeID, NodeType: blackboard.NodeTypeProjectFact, StableKey: stableKey, CreatedAt: createdAt}
		for _, version := range versions {
			node.Versions = append(node.Versions, blackboard.LegacyImportNodeVersionV1{Version: version.version, Properties: normalizedFactProperties(version.category, version.summary, version.body, version.confidence, version.scopeStatus), UpdatedAt: version.createdAt})
			ordinal := version.version
			node.Sources = append(node.Sources, blackboard.LegacyImportSourceV1{Table: "project_fact_versions", PrimaryID: version.id, Key: key, Version: &ordinal})
		}
		if hasCurrent {
			node.Sources = append(node.Sources, blackboard.LegacyImportSourceV1{Table: "project_facts", PrimaryID: currentFact.id, Key: key})
		}
		plan.Nodes = append(plan.Nodes, node)
		nodeByLegacyKey[key] = node
		for index := range mappings {
			if mappings[index].sourceTable == "project_fact_versions" && mappings[index].originalStableKey == key {
				mappings[index].targetID = nodeID
			}
		}
		if stableKey != key {
			plan.Aliases = append(plan.Aliases, blackboard.LegacyImportAliasV1{NodeType: blackboard.NodeTypeProjectFact, Key: key, CanonicalNodeID: nodeID, LegacyNonconforming: true})
		}
		if hasCurrent {
			version := node.Versions[len(node.Versions)-1].Version
			mappings = append(mappings, newLegacyMapping(projectID, "project_facts", "fact", currentFact.id, key, nil, currentFact, "project_fact", nodeID, &version, "imported", nil))
		}
	}

	aliases, merges, aliasMappings, err := readLegacyFactAliases(ctx, tx, projectID, nodeByLegacyKey)
	if err != nil {
		return plan, nil, err
	}
	plan.Aliases = append(plan.Aliases, aliases...)
	plan.Merges = append(plan.Merges, merges...)
	mappings = append(mappings, aliasMappings...)
	markLegacyRebadgedCopies(history, aliasMappings, mappings)

	edges, relationMappings, err := readLegacyFactRelations(ctx, tx, projectID, nodeByLegacyKey)
	if err != nil {
		return plan, nil, err
	}
	plan.Edges = append(plan.Edges, edges...)
	mappings = append(mappings, relationMappings...)

	findingNodes, findingMappings, findingAliases, findingMerges, err := readLegacyFindings(ctx, tx, projectID)
	if err != nil {
		return plan, nil, err
	}
	orderedFindingKeys := make([]string, 0, len(findingNodes))
	for key := range findingNodes {
		orderedFindingKeys = append(orderedFindingKeys, key)
	}
	sort.Strings(orderedFindingKeys)
	for _, key := range orderedFindingKeys {
		plan.Nodes = append(plan.Nodes, findingNodes[key])
	}
	plan.Aliases = append(plan.Aliases, findingAliases...)
	plan.Merges = append(plan.Merges, findingMerges...)
	mappings = append(mappings, findingMappings...)

	evidenceNodes, evidenceEdges, evidenceMappings, err := s.readLegacyEvidence(ctx, tx, projectID, nodeByLegacyKey, findingNodes)
	if err != nil {
		return plan, nil, err
	}
	plan.Nodes = append(plan.Nodes, evidenceNodes...)
	plan.Edges = append(plan.Edges, evidenceEdges...)
	mappings = append(mappings, evidenceMappings...)

	plan.SourceDigest = sourceDigest
	plan.PlanDigest, err = importPlanDigest(plan)
	if err != nil {
		return plan, nil, err
	}
	plan.IdempotencyKey = "legacy-blackboard-v1:" + sourceDigest + ":" + projectID
	if err := refreshMappingSourceHashes(ctx, tx, mappings); err != nil {
		return plan, nil, err
	}
	return plan, mappings, nil
}

func markLegacyRebadgedCopies(history map[string][]legacyFactVersion, aliasMappings []legacyMapping, mappings []legacyMapping) {
	for _, alias := range aliasMappings {
		if alias.status != "merged" {
			continue
		}
		canonicalKey, _ := alias.compatibilityMetadata["canonical_key"].(string)
		canonicalVersions := history[canonicalKey]
		sourceVersions := history[alias.originalStableKey]
		if len(sourceVersions) == 0 || len(canonicalVersions) < len(sourceVersions) {
			continue
		}
		copyStart := len(canonicalVersions) - len(sourceVersions)
		matchesCopiedSuffix := true
		for index := range sourceVersions {
			canonicalVersion := canonicalVersions[copyStart+index]
			sourceVersion := sourceVersions[index]
			canonicalProperties := normalizedFactProperties(canonicalVersion.category, canonicalVersion.summary, canonicalVersion.body, canonicalVersion.confidence, canonicalVersion.scopeStatus)
			sourceProperties := normalizedFactProperties(sourceVersion.category, sourceVersion.summary, sourceVersion.body, sourceVersion.confidence, sourceVersion.scopeStatus)
			if canonicalVersion.createdAt != sourceVersion.createdAt || !reflect.DeepEqual(canonicalProperties, sourceProperties) {
				matchesCopiedSuffix = false
				break
			}
		}
		if !matchesCopiedSuffix {
			continue
		}
		for _, canonicalVersion := range canonicalVersions[copyStart:] {
			for index := range mappings {
				if mappings[index].sourceTable == "project_fact_versions" && mappings[index].originalStableKey == canonicalKey && mappings[index].originalVersion != nil && *mappings[index].originalVersion == canonicalVersion.version {
					mappings[index].status = "legacy_rebadged_copy"
				}
			}
		}
	}
}

func refreshMappingSourceHashes(ctx context.Context, tx *sql.Tx, mappings []legacyMapping) error {
	for index := range mappings {
		rows, err := tx.QueryContext(ctx, `SELECT * FROM "`+mappings[index].sourceTable+`" WHERE id=?`, mappings[index].legacyPrimaryID)
		if err != nil {
			return fmt.Errorf("hash legacy mapping source %s/%s: %w", mappings[index].sourceTable, mappings[index].legacyPrimaryID, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return err
		}
		if !rows.Next() {
			rows.Close()
			return fmt.Errorf("legacy mapping source %s/%s is missing", mappings[index].sourceTable, mappings[index].legacyPrimaryID)
		}
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for column := range values {
			destinations[column] = &values[column]
		}
		if err := rows.Scan(destinations...); err != nil {
			rows.Close()
			return err
		}
		hash := sha256.New()
		writeFrame(hash, []byte("legacy_blackboard_mapping_source_v1"))
		writeFrame(hash, []byte(mappings[index].sourceTable))
		for column, name := range columns {
			writeFrame(hash, []byte(name))
			writeFrame(hash, canonicalSQLValue(values[column]))
		}
		mappings[index].sourceRowHash = hex.EncodeToString(hash.Sum(nil))
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func readLegacyCurrentFacts(ctx context.Context, tx *sql.Tx, projectID string) (map[string]legacyFactCurrent, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,fact_key,category,summary,body,confidence,scope_status,created_at,updated_at FROM project_facts WHERE project_id=? ORDER BY fact_key,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]legacyFactCurrent)
	for rows.Next() {
		var fact legacyFactCurrent
		if err := rows.Scan(&fact.id, &fact.key, &fact.category, &fact.summary, &fact.body, &fact.confidence, &fact.scopeStatus, &fact.createdAt, &fact.updatedAt); err != nil {
			return nil, err
		}
		result[fact.key] = fact
	}
	return result, rows.Err()
}

func readLegacyFactHistory(ctx context.Context, tx *sql.Tx, projectID string) (map[string][]legacyFactVersion, []legacyMapping, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,fact_key,version,category,summary,body,confidence,scope_status,created_at FROM project_fact_versions WHERE project_id=? ORDER BY fact_key,version,id`, projectID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	history := make(map[string][]legacyFactVersion)
	var mappings []legacyMapping
	for rows.Next() {
		var version legacyFactVersion
		if err := rows.Scan(&version.id, &version.key, &version.version, &version.category, &version.summary, &version.body, &version.confidence, &version.scopeStatus, &version.createdAt); err != nil {
			return nil, nil, err
		}
		history[version.key] = append(history[version.key], version)
		ordinal := version.version
		mappings = append(mappings, newLegacyMapping(projectID, "project_fact_versions", "fact_version", version.id, version.key, &ordinal, version, "project_fact", "", &ordinal, "imported", nil))
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	for key, versions := range history {
		for index := 1; index < len(versions); index++ {
			status := ""
			previous := versions[index-1]
			current := versions[index]
			if reflect.DeepEqual(normalizedFactProperties(previous.category, previous.summary, previous.body, previous.confidence, previous.scopeStatus), normalizedFactProperties(current.category, current.summary, current.body, current.confidence, current.scopeStatus)) {
				status = "legacy_noop_version"
			} else if previous.confidence == "deprecated" && current.confidence != "deprecated" {
				status = "legacy_transition_exception"
			}
			if status != "" {
				for mappingIndex := range mappings {
					if mappings[mappingIndex].originalStableKey == key && mappings[mappingIndex].originalVersion != nil && *mappings[mappingIndex].originalVersion == current.version {
						mappings[mappingIndex].status = status
					}
				}
			}
		}
	}
	return history, mappings, nil
}

func readLegacyFactAliases(ctx context.Context, tx *sql.Tx, projectID string, nodes map[string]blackboard.LegacyImportNodeV1) ([]blackboard.LegacyImportAliasV1, []blackboard.LegacyImportMergeV1, []legacyMapping, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,alias_fact_key,canon_fact_key,created_at FROM fact_key_aliases WHERE project_id=? ORDER BY alias_fact_key,id`, projectID)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	type aliasRow struct{ id, alias, canonical, createdAt string }
	var source []aliasRow
	graph := make(map[string]string)
	for rows.Next() {
		var row aliasRow
		if err := rows.Scan(&row.id, &row.alias, &row.canonical, &row.createdAt); err != nil {
			return nil, nil, nil, err
		}
		source = append(source, row)
		graph[row.alias] = row.canonical
	}
	var aliases []blackboard.LegacyImportAliasV1
	var merges []blackboard.LegacyImportMergeV1
	var mappings []legacyMapping
	for _, row := range source {
		target, ok := flattenLegacyAlias(row.alias, graph, nodes)
		status := "unresolvable_legacy_alias"
		targetID := ""
		if ok {
			node := nodes[target]
			targetID = node.ID
			if source, sourceHasHistory := nodes[row.alias]; sourceHasHistory && source.ID != node.ID {
				status = "merged"
				merges = append(merges, blackboard.LegacyImportMergeV1{OperationID: "merge:" + shortHash(row.id), SourceNodeID: source.ID, CanonicalNodeID: node.ID, SourceExpectedVersion: source.Versions[len(source.Versions)-1].Version, CanonicalExpectedVersion: node.Versions[len(node.Versions)-1].Version, Source: blackboard.LegacyImportSourceV1{Table: "fact_key_aliases", PrimaryID: row.id, Key: row.alias}, MergedAt: row.createdAt})
			} else {
				status = "alias"
				aliases = append(aliases, blackboard.LegacyImportAliasV1{NodeType: blackboard.NodeTypeProjectFact, Key: row.alias, CanonicalNodeID: targetID, LegacyNonconforming: !graphStableKeyPattern.MatchString(row.alias), Source: blackboard.LegacyImportSourceV1{Table: "fact_key_aliases", PrimaryID: row.id, Key: row.alias}})
			}
		}
		mappings = append(mappings, newLegacyMapping(projectID, "fact_key_aliases", "fact_alias", row.id, row.alias, nil, row, "project_fact", targetID, nil, status, map[string]any{"canonical_key": row.canonical, "created_at": row.createdAt}))
	}
	return aliases, merges, mappings, rows.Err()
}

func readLegacyFactRelations(ctx context.Context, tx *sql.Tx, projectID string, nodes map[string]blackboard.LegacyImportNodeV1) ([]blackboard.LegacyImportEdgeV1, []legacyMapping, error) {
	aliasRows, err := tx.QueryContext(ctx, `SELECT alias_fact_key,canon_fact_key FROM fact_key_aliases WHERE project_id=? ORDER BY alias_fact_key`, projectID)
	if err != nil {
		return nil, nil, err
	}
	aliases := make(map[string]string)
	for aliasRows.Next() {
		var alias, canonical string
		if err := aliasRows.Scan(&alias, &canonical); err != nil {
			aliasRows.Close()
			return nil, nil, err
		}
		aliases[alias] = canonical
	}
	if err := aliasRows.Close(); err != nil {
		return nil, nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,source_fact_key,target_fact_key,relation,summary,created_at,updated_at FROM project_fact_relations WHERE project_id=? ORDER BY created_at,id`, projectID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var edges []blackboard.LegacyImportEdgeV1
	var mappings []legacyMapping
	for rows.Next() {
		var id, sourceKey, targetKey, relation, summary, createdAt, updatedAt string
		if err := rows.Scan(&id, &sourceKey, &targetKey, &relation, &summary, &createdAt, &updatedAt); err != nil {
			return nil, nil, err
		}
		normalized := relation
		switch relation {
		case "leads-to":
			normalized = "leads_to"
		case "depends-on":
			normalized = "depends_on"
		}
		status := "audit_only_relation"
		targetID := ""
		resolvedSourceKey, sourceOK := flattenLegacyAlias(sourceKey, aliases, nodes)
		resolvedTargetKey, targetOK := flattenLegacyAlias(targetKey, aliases, nodes)
		if sourceOK && targetOK {
			if source, sourceExists := nodes[resolvedSourceKey]; sourceExists {
				if target, targetExists := nodes[resolvedTargetKey]; targetExists {
					var edgeType blackboard.EdgeType
					switch normalized {
					case "supports":
						edgeType = blackboard.EdgeTypeSupports
					case "contradicts":
						edgeType = blackboard.EdgeTypeContradicts
					case "leads_to":
						edgeType = blackboard.EdgeTypeLeadsTo
					}
					if edgeType != "" {
						status = "imported"
						targetID = migrationIdentity("edge", projectID, "project_fact_relations", id)
						edges = append(edges, blackboard.LegacyImportEdgeV1{OperationID: "relation:" + shortHash(id), ID: targetID, EdgeType: edgeType, FromNodeID: source.ID, ToNodeID: target.ID, Summary: summary, CreatedAt: createdAt, UpdatedAt: updatedAt, Source: blackboard.LegacyImportSourceV1{Table: "project_fact_relations", PrimaryID: id, Key: sourceKey}})
					}
				}
			}
		}
		metadata := map[string]any{"source_fact_key": sourceKey, "target_fact_key": targetKey, "relation": normalized, "summary": summary, "created_at": createdAt, "updated_at": updatedAt}
		mappings = append(mappings, newLegacyMapping(projectID, "project_fact_relations", "fact_relation", id, sourceKey, nil, metadata, "edge", targetID, nil, status, metadata))
	}
	return edges, mappings, rows.Err()
}

func flattenLegacyAlias(start string, aliases map[string]string, nodes map[string]blackboard.LegacyImportNodeV1) (string, bool) {
	seen := map[string]bool{}
	cursor := start
	for {
		if seen[cursor] {
			return "", false
		}
		seen[cursor] = true
		next, ok := aliases[cursor]
		if !ok {
			_, live := nodes[cursor]
			return cursor, live
		}
		cursor = next
	}
}

func normalizedFactProperties(category, summary, body, confidence, scope string) map[string]any {
	if category == "" {
		category = "uncategorized"
	}
	if confidence == "" {
		confidence = "tentative"
	}
	if scope != "in_scope" && scope != "out_of_scope" && scope != "unknown" {
		scope = "unknown"
	}
	return map[string]any{"category": category, "summary": summary, "body": body, "confidence": confidence, "scope_status": scope}
}

func readLegacyFindings(ctx context.Context, tx *sql.Tx, projectID string) (map[string]blackboard.LegacyImportNodeV1, []legacyMapping, []blackboard.LegacyImportAliasV1, []blackboard.LegacyImportMergeV1, error) {
	history, mappings, err := readLegacyFindingHistory(ctx, tx, projectID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	current, err := readLegacyCurrentFindings(ctx, tx, projectID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	keys := make(map[string]struct{}, len(history)+len(current))
	for key := range history {
		keys[key] = struct{}{}
	}
	for key := range current {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	nodes := make(map[string]blackboard.LegacyImportNodeV1, len(ordered))
	historyOnly := make(map[string]bool)
	for _, key := range ordered {
		versions := append([]legacyFindingVersion(nil), history[key]...)
		currentFinding, hasCurrent := current[key]
		if hasCurrent {
			properties := normalizedFindingProperties(currentFinding.title, currentFinding.description, currentFinding.status, currentFinding.target, currentFinding.proof, currentFinding.impact, currentFinding.recommendation, currentFinding.cvssVersion, currentFinding.cvssVector)
			if len(versions) == 0 {
				versions = append(versions, legacyFindingVersion{id: currentFinding.id, key: key, version: 1, title: currentFinding.title, description: currentFinding.description, status: currentFinding.status, target: currentFinding.target, proof: currentFinding.proof, impact: currentFinding.impact, recommendation: currentFinding.recommendation, cvssVersion: currentFinding.cvssVersion, cvssVector: currentFinding.cvssVector, createdAt: currentFinding.updatedAt})
			} else if !reflect.DeepEqual(normalizedFindingVersionProperties(versions[len(versions)-1]), properties) {
				versions = append(versions, legacyFindingVersion{id: currentFinding.id, key: key, version: versions[len(versions)-1].version + 1, title: currentFinding.title, description: currentFinding.description, status: currentFinding.status, target: currentFinding.target, proof: currentFinding.proof, impact: currentFinding.impact, recommendation: currentFinding.recommendation, cvssVersion: currentFinding.cvssVersion, cvssVector: currentFinding.cvssVector, createdAt: currentFinding.updatedAt})
			}
		}
		if len(versions) == 0 {
			continue
		}
		nodeID := migrationIdentity("node", projectID, "finding_versions", key)
		createdAt := versions[0].createdAt
		if hasCurrent && legacyIDGloballyUnique(ctx, tx, currentFinding.id) {
			nodeID = currentFinding.id
			createdAt = currentFinding.createdAt
		}
		stableKey := normalizedLegacyStableKey(projectID, "finding", key)
		node := blackboard.LegacyImportNodeV1{OperationID: "finding:" + shortHash(key), ID: nodeID, NodeType: blackboard.NodeTypeFinding, StableKey: stableKey, CreatedAt: createdAt}
		for _, version := range versions {
			ordinal := version.version
			node.Versions = append(node.Versions, blackboard.LegacyImportNodeVersionV1{Version: ordinal, Properties: normalizedFindingVersionProperties(version), UpdatedAt: version.createdAt})
			node.Sources = append(node.Sources, blackboard.LegacyImportSourceV1{Table: "finding_versions", PrimaryID: version.id, Key: key, Version: &ordinal})
		}
		if hasCurrent {
			node.Sources = append(node.Sources, blackboard.LegacyImportSourceV1{Table: "findings", PrimaryID: currentFinding.id, Key: key})
		}
		nodes[key] = node
		historyOnly[key] = !hasCurrent
		for index := range mappings {
			if mappings[index].sourceTable == "finding_versions" && mappings[index].originalStableKey == key {
				mappings[index].targetID = nodeID
			}
		}
		if hasCurrent {
			version := node.Versions[len(node.Versions)-1].Version
			mappings = append(mappings, newLegacyMapping(projectID, "findings", "finding", currentFinding.id, key, nil, currentFinding, "finding", nodeID, &version, "imported", nil))
		}
	}

	aliases, merges, aliasMappings, err := readLegacyFindingAliases(ctx, tx, projectID, nodes)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	mergedSourceIDs := make(map[string]bool, len(merges))
	for _, merge := range merges {
		mergedSourceIDs[merge.SourceNodeID] = true
	}
	for _, key := range ordered {
		node := nodes[key]
		if historyOnly[key] && !mergedSourceIDs[node.ID] {
			node.Disposition = blackboard.DispositionArchived
			nodes[key] = node
		}
		if node.StableKey != key {
			aliases = append(aliases, blackboard.LegacyImportAliasV1{NodeType: blackboard.NodeTypeFinding, Key: key, CanonicalNodeID: node.ID, LegacyNonconforming: true})
		}
	}
	mappings = append(mappings, aliasMappings...)
	return nodes, mappings, aliases, merges, nil
}

func readLegacyFindingHistory(ctx context.Context, tx *sql.Tx, projectID string) (map[string][]legacyFindingVersion, []legacyMapping, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,finding_key,version,title,description,status,target,proof,impact,recommendation,cvss_version,cvss_vector,created_at FROM finding_versions WHERE project_id=? ORDER BY finding_key,version,id`, projectID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	history := make(map[string][]legacyFindingVersion)
	var mappings []legacyMapping
	for rows.Next() {
		var value legacyFindingVersion
		if err := rows.Scan(&value.id, &value.key, &value.version, &value.title, &value.description, &value.status, &value.target, &value.proof, &value.impact, &value.recommendation, &value.cvssVersion, &value.cvssVector, &value.createdAt); err != nil {
			return nil, nil, err
		}
		history[value.key] = append(history[value.key], value)
		ordinal := value.version
		mappings = append(mappings, newLegacyMapping(projectID, "finding_versions", "finding_version", value.id, value.key, &ordinal, value, "finding", "", &ordinal, "imported", nil))
	}
	return history, mappings, rows.Err()
}

func readLegacyCurrentFindings(ctx context.Context, tx *sql.Tx, projectID string) (map[string]legacyFindingCurrent, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,finding_key,version,title,description,status,target,proof,impact,recommendation,cvss_version,cvss_vector,created_at,updated_at FROM findings WHERE project_id=? ORDER BY finding_key,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]legacyFindingCurrent)
	for rows.Next() {
		var value legacyFindingCurrent
		if err := rows.Scan(&value.id, &value.key, &value.version, &value.title, &value.description, &value.status, &value.target, &value.proof, &value.impact, &value.recommendation, &value.cvssVersion, &value.cvssVector, &value.createdAt, &value.updatedAt); err != nil {
			return nil, err
		}
		result[value.key] = value
	}
	return result, rows.Err()
}

func normalizedFindingVersionProperties(value legacyFindingVersion) map[string]any {
	return normalizedFindingProperties(value.title, value.description, value.status, value.target, value.proof, value.impact, value.recommendation, value.cvssVersion, value.cvssVector)
}

func normalizedFindingProperties(title, description, status, target, proof, impact, recommendation, cvssVersion, cvssVector string) map[string]any {
	if cvssVersion != "4.0" && cvssVersion != "3.1" {
		cvssVersion = ""
	}
	if cvssVersion == "" {
		switch {
		case strings.HasPrefix(cvssVector, "CVSS:4.0/"):
			cvssVersion = "4.0"
		case strings.HasPrefix(cvssVector, "CVSS:3.1/"):
			cvssVersion = "3.1"
		}
	}
	return map[string]any{"title": title, "description": description, "status": status, "target": target, "proof": proof, "impact": impact, "recommendation": recommendation, "cvss_version": cvssVersion, "cvss_vector": cvssVector}
}

func readLegacyFindingAliases(ctx context.Context, tx *sql.Tx, projectID string, nodes map[string]blackboard.LegacyImportNodeV1) ([]blackboard.LegacyImportAliasV1, []blackboard.LegacyImportMergeV1, []legacyMapping, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,alias_finding_key,canon_finding_key,created_at FROM finding_key_aliases WHERE project_id=? ORDER BY alias_finding_key,id`, projectID)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	type aliasRow struct{ id, alias, canonical, createdAt string }
	var source []aliasRow
	graph := make(map[string]string)
	for rows.Next() {
		var row aliasRow
		if err := rows.Scan(&row.id, &row.alias, &row.canonical, &row.createdAt); err != nil {
			return nil, nil, nil, err
		}
		source = append(source, row)
		graph[row.alias] = row.canonical
	}
	var aliases []blackboard.LegacyImportAliasV1
	var merges []blackboard.LegacyImportMergeV1
	var mappings []legacyMapping
	for _, row := range source {
		target, ok := flattenLegacyAlias(row.alias, graph, nodes)
		status, targetID := "unresolvable_legacy_alias", ""
		if ok {
			targetNode := nodes[target]
			targetID = targetNode.ID
			if sourceNode, sourceExists := nodes[row.alias]; sourceExists && sourceNode.ID != targetNode.ID {
				status = "merged"
				merges = append(merges, blackboard.LegacyImportMergeV1{OperationID: "finding-merge:" + shortHash(row.id), SourceNodeID: sourceNode.ID, CanonicalNodeID: targetNode.ID, SourceExpectedVersion: sourceNode.Versions[len(sourceNode.Versions)-1].Version, CanonicalExpectedVersion: targetNode.Versions[len(targetNode.Versions)-1].Version, Source: blackboard.LegacyImportSourceV1{Table: "finding_key_aliases", PrimaryID: row.id, Key: row.alias}, MergedAt: row.createdAt})
			} else {
				status = "alias"
				aliases = append(aliases, blackboard.LegacyImportAliasV1{NodeType: blackboard.NodeTypeFinding, Key: row.alias, CanonicalNodeID: targetID, LegacyNonconforming: !graphStableKeyPattern.MatchString(row.alias), Source: blackboard.LegacyImportSourceV1{Table: "finding_key_aliases", PrimaryID: row.id, Key: row.alias}})
			}
		}
		metadata := map[string]any{"canonical_key": row.canonical, "created_at": row.createdAt}
		mappings = append(mappings, newLegacyMapping(projectID, "finding_key_aliases", "finding_alias", row.id, row.alias, nil, row, "finding", targetID, nil, status, metadata))
	}
	return aliases, merges, mappings, rows.Err()
}

func (s *Service) readLegacyEvidence(ctx context.Context, tx *sql.Tx, projectID string, factNodes, findingNodes map[string]blackboard.LegacyImportNodeV1) ([]blackboard.LegacyImportNodeV1, []blackboard.LegacyImportEdgeV1, []legacyMapping, error) {
	findingAliases, err := legacyAliasMap(ctx, tx, projectID, "finding_key_aliases", "alias_finding_key", "canon_finding_key")
	if err != nil {
		return nil, nil, nil, err
	}
	factAliases, err := legacyAliasMap(ctx, tx, projectID, "fact_key_aliases", "alias_fact_key", "canon_fact_key")
	if err != nil {
		return nil, nil, nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,evidence_key,attach_to_type,attach_to_key,artifact_type,source_path,managed_path,sha256,summary,created_at,updated_at FROM evidence_artifacts WHERE project_id=? ORDER BY evidence_key,id`, projectID)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	var nodes []blackboard.LegacyImportNodeV1
	var edges []blackboard.LegacyImportEdgeV1
	var mappings []legacyMapping
	for rows.Next() {
		var id, key, attachType, attachKey, artifactType, sourcePath, managedPath, storedDigest, summary, createdAt, updatedAt string
		if err := rows.Scan(&id, &key, &attachType, &attachKey, &artifactType, &sourcePath, &managedPath, &storedDigest, &summary, &createdAt, &updatedAt); err != nil {
			return nil, nil, nil, err
		}
		canonicalType := normalizeEvidenceType(artifactType)
		canonicalPath, actualDigest, size, status := inspectManagedEvidence(s.artifactRoot, key, managedPath)
		properties := map[string]any{"artifact_type": canonicalType, "source_path": sourcePath, "managed_path": canonicalPath, "sha256": actualDigest, "size_bytes": size, "summary": summary, "status": status, "captured_at": createdAt}
		nodeID := migrationIdentity("node", projectID, "evidence_artifacts", id)
		if legacyIDGloballyUnique(ctx, tx, id) {
			nodeID = id
		}
		metadata := map[string]any{"attach_to_type": attachType, "attach_to_key": attachKey, "managed_path": managedPath}
		mappingStatus := "imported"
		if canonicalType != artifactType {
			metadata["original_artifact_type"] = artifactType
		}
		if storedDigest != "" && actualDigest != "" && !strings.EqualFold(storedDigest, actualDigest) {
			metadata["recorded_sha256"] = storedDigest
			metadata["actual_sha256"] = actualDigest
			mappingStatus = "digest_mismatch"
		}
		if canonicalPath != managedPath {
			metadata["unsafe_or_missing_managed_path"] = managedPath
		}

		resolvedType, resolvedKey, targetID := resolveLegacyEvidenceTarget(attachType, attachKey, factAliases, findingAliases, factNodes, findingNodes)
		if targetID != "" {
			properties["migrated_attach_to_type"] = resolvedType
			properties["migrated_attach_to_key"] = resolvedKey
			edgeID := migrationIdentity("edge", projectID, "evidence_artifacts", id)
			edges = append(edges, blackboard.LegacyImportEdgeV1{OperationID: "evidence-edge:" + shortHash(id), ID: edgeID, EdgeType: blackboard.EdgeTypeEvidences, FromNodeID: nodeID, ToNodeID: targetID, Summary: summary, CreatedAt: createdAt, UpdatedAt: updatedAt, Source: blackboard.LegacyImportSourceV1{Table: "evidence_artifacts", PrimaryID: id, Key: key}})
		}
		node := blackboard.LegacyImportNodeV1{OperationID: "evidence:" + shortHash(key), ID: nodeID, NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: normalizedLegacyStableKey(projectID, "evidence", key), CreatedAt: createdAt, Versions: []blackboard.LegacyImportNodeVersionV1{{Version: 1, Properties: properties, UpdatedAt: updatedAt}}, Sources: []blackboard.LegacyImportSourceV1{{Table: "evidence_artifacts", PrimaryID: id, Key: key}}}
		nodes = append(nodes, node)
		mappings = append(mappings, newLegacyMapping(projectID, "evidence_artifacts", "evidence", id, key, nil, properties, "evidence_artifact", nodeID, intPointer(1), mappingStatus, metadata))
	}
	return nodes, edges, mappings, rows.Err()
}

func legacyAliasMap(ctx context.Context, tx *sql.Tx, projectID, table, aliasColumn, canonicalColumn string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+aliasColumn+`,`+canonicalColumn+` FROM "`+table+`" WHERE project_id=? ORDER BY `+aliasColumn, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var alias, canonical string
		if err := rows.Scan(&alias, &canonical); err != nil {
			return nil, err
		}
		result[alias] = canonical
	}
	return result, rows.Err()
}

func resolveLegacyEvidenceTarget(attachType, attachKey string, factAliases, findingAliases map[string]string, factNodes, findingNodes map[string]blackboard.LegacyImportNodeV1) (string, string, string) {
	switch attachType {
	case "fact":
		key, ok := flattenLegacyAlias(attachKey, factAliases, factNodes)
		if ok {
			return "fact", factNodes[key].StableKey, factNodes[key].ID
		}
	case "finding":
		key, ok := flattenLegacyAlias(attachKey, findingAliases, findingNodes)
		if ok {
			return "finding", findingNodes[key].StableKey, findingNodes[key].ID
		}
	}
	return "", "", ""
}

func normalizeEvidenceType(value string) string {
	switch value {
	case "http_exchange", "screenshot", "terminal_capture", "log", "pcap", "file", "binary", "source_code", "structured_data", "report", "other":
		return value
	default:
		return "other"
	}
}

func inspectManagedEvidence(artifactRoot, key, managedPath string) (string, string, int64, string) {
	missing := "missing://legacy/" + shortHash(key)
	if !pathIsConfined(artifactRoot, managedPath) {
		return missing, "", 0, "missing"
	}
	root, err := filepath.EvalSymlinks(artifactRoot)
	if err != nil {
		return filepath.ToSlash(managedPath), "", 0, "missing"
	}
	candidate := filepath.Join(root, filepath.FromSlash(managedPath))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return filepath.ToSlash(managedPath), "", 0, "missing"
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return missing, "", 0, "missing"
	}
	file, err := os.Open(resolved)
	if err != nil {
		return filepath.ToSlash(managedPath), "", 0, "missing"
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return filepath.ToSlash(managedPath), "", 0, "missing"
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return filepath.ToSlash(managedPath), "", 0, "missing"
	}
	return filepath.ToSlash(managedPath), hex.EncodeToString(hash.Sum(nil)), size, "available"
}

func normalizedLegacyStableKey(projectID, kind, original string) string {
	if graphStableKeyPattern.MatchString(original) {
		return original
	}
	sum := sha256.Sum256([]byte(projectID + "\x00" + original))
	return "legacy-import:" + kind + ":" + hex.EncodeToString(sum[:])
}

func migrationIdentity(domain, projectID, sourceTable, sourceID string) string {
	sum := sha256.Sum256([]byte(domain + "\x00" + projectID + "\x00" + sourceTable + "\x00" + sourceID))
	return "mig_" + hex.EncodeToString(sum[:])
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func legacyIDGloballyUnique(ctx context.Context, tx *sql.Tx, id string) bool {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM project_facts WHERE id=?) +
		(SELECT COUNT(*) FROM findings WHERE id=?) +
		(SELECT COUNT(*) FROM evidence_artifacts WHERE id=?)`, id, id, id).Scan(&count)
	return err == nil && count == 1
}

func importPlanDigest(plan blackboard.LegacyImportPlanV1) (string, error) {
	copy := plan
	copy.PlanDigest = ""
	copy.IdempotencyKey = ""
	body, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func newLegacyMapping(projectID, table, kind, primaryID, stableKey string, version *int, source any, targetKind, targetID string, targetVersion *int, status string, metadata map[string]any) legacyMapping {
	body := []byte(fmt.Sprintf("%#v", source))
	sum := sha256.Sum256(body)
	return legacyMapping{projectID: projectID, sourceTable: table, sourceKind: kind, legacyPrimaryID: primaryID, originalStableKey: stableKey, originalVersion: version, sourceRowHash: hex.EncodeToString(sum[:]), targetKind: targetKind, targetID: targetID, targetVersion: targetVersion, status: status, compatibilityMetadata: metadata}
}

func insertLegacyMapping(ctx context.Context, tx *sql.Tx, mapping legacyMapping, mutationSequence int, cutoverID, createdAt string) error {
	metadata, err := json.Marshal(mapping.compatibilityMetadata)
	if err != nil {
		return err
	}
	id := migrationIdentity("mapping", mapping.projectID, mapping.sourceTable, mapping.legacyPrimaryID+fmt.Sprint(mapping.originalVersion))
	_, err = tx.ExecContext(ctx, `INSERT INTO blackboard_legacy_mappings
		(id,project_id,source_table,source_kind,legacy_primary_id,original_stable_key,original_version,source_row_hash,target_kind,target_id,target_version,mapping_status,compatibility_metadata_json,migration_mutation_seq,cutover_id,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, mapping.projectID, mapping.sourceTable, mapping.sourceKind, mapping.legacyPrimaryID, mapping.originalStableKey, mapping.originalVersion,
		mapping.sourceRowHash, mapping.targetKind, mapping.targetID, mapping.targetVersion, mapping.status, string(metadata), mutationSequence, cutoverID, createdAt)
	if err != nil {
		return fmt.Errorf("insert legacy mapping: %w", err)
	}
	return nil
}

func legacyMappingsDigest(mappings []legacyMapping) (string, error) {
	type digestRow struct {
		ProjectID, SourceTable, SourceKind, LegacyPrimaryID, OriginalStableKey, SourceRowHash string
		OriginalVersion, TargetVersion                                                        *int
		TargetKind, TargetID, Status, Metadata                                                string
	}
	rows := make([]digestRow, 0, len(mappings))
	for _, mapping := range mappings {
		metadata, err := json.Marshal(mapping.compatibilityMetadata)
		if err != nil {
			return "", err
		}
		rows = append(rows, digestRow{mapping.projectID, mapping.sourceTable, mapping.sourceKind, mapping.legacyPrimaryID, mapping.originalStableKey, mapping.sourceRowHash, mapping.originalVersion, mapping.targetVersion, mapping.targetKind, mapping.targetID, mapping.status, string(metadata)})
	}
	sort.Slice(rows, func(i, j int) bool {
		left, _ := json.Marshal(rows[i])
		right, _ := json.Marshal(rows[j])
		return string(left) < string(right)
	})
	body, err := json.Marshal(rows)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("legacy_blackboard_mapping_v1\x00"), body...))
	return hex.EncodeToString(sum[:]), nil
}

func persistedLegacyMappingsDigest(ctx context.Context, tx *sql.Tx) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT project_id,source_table,source_kind,legacy_primary_id,original_stable_key,original_version,source_row_hash,target_kind,target_id,target_version,mapping_status,compatibility_metadata_json FROM blackboard_legacy_mappings ORDER BY project_id,source_table,legacy_primary_id,id`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var mappings []legacyMapping
	for rows.Next() {
		var mapping legacyMapping
		var originalVersion, targetVersion sql.NullInt64
		var metadataRaw string
		if err := rows.Scan(&mapping.projectID, &mapping.sourceTable, &mapping.sourceKind, &mapping.legacyPrimaryID, &mapping.originalStableKey, &originalVersion, &mapping.sourceRowHash, &mapping.targetKind, &mapping.targetID, &targetVersion, &mapping.status, &metadataRaw); err != nil {
			return "", err
		}
		if originalVersion.Valid {
			value := int(originalVersion.Int64)
			mapping.originalVersion = &value
		}
		if targetVersion.Valid {
			value := int(targetVersion.Int64)
			mapping.targetVersion = &value
		}
		if err := json.Unmarshal([]byte(metadataRaw), &mapping.compatibilityMetadata); err != nil {
			return "", err
		}
		mappings = append(mappings, mapping)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return legacyMappingsDigest(mappings)
}

func intPointer(value int) *int { return &value }
