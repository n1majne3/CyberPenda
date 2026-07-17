package blackboardv2

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

type mergeRelationship struct {
	from, relation, to, reason, createdAt, updatedAt string
	version                                          int
}

func applyMerge(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, []RelationVersionTuple, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := validateKey(change.Source, path+".source"); err != nil {
		return revision, "", 0, nil, err
	}
	if err := validateKey(change.Canonical, path+".canonical"); err != nil {
		return revision, "", 0, nil, err
	}
	if change.Source == change.Canonical {
		return revision, "", 0, nil, semanticError("semantic_validation", "merge source and canonical must differ", path+".canonical", nil)
	}
	if redirected, err := keyIsRedirect(ctx, tx, projectID, change.Source); err != nil {
		return revision, "", 0, nil, err
	} else if redirected {
		return revision, "", 0, nil, semanticError("key_conflict", "merge source is already a Blackboard Key Redirect", path+".source", map[string]any{"key": change.Source})
	}
	if hasSources, err := keyHasRedirectSources(ctx, tx, projectID, change.Source); err != nil {
		return revision, "", 0, nil, err
	} else if hasSources {
		return revision, "", 0, nil, semanticError("key_conflict", "merge source is a canonical key for existing Blackboard Key Redirects", path+".source", map[string]any{"key": change.Source})
	}
	if redirected, err := keyIsRedirect(ctx, tx, projectID, change.Canonical); err != nil {
		return revision, "", 0, nil, err
	} else if redirected {
		return revision, "", 0, nil, semanticError("key_conflict", "merge canonical cannot be a Blackboard Key Redirect", path+".canonical", map[string]any{"key": change.Canonical})
	}

	source, err := loadMergeRecord(ctx, tx, projectID, change.Source, path+".source")
	if err != nil {
		return revision, "", 0, nil, err
	}
	canonical, err := loadMergeRecord(ctx, tx, projectID, change.Canonical, path+".canonical")
	if err != nil {
		return revision, "", 0, nil, err
	}
	if source.typ != canonical.typ {
		return revision, "", 0, nil, semanticError("semantic_validation", "Record Merge requires matching Project Knowledge types", path+".canonical", map[string]any{"source_type": source.typ, "canonical_type": canonical.typ})
	}
	if !isProjectKnowledgeType(source.typ) {
		return revision, "", 0, nil, semanticError("semantic_validation", "Current Work cannot be merged", path+".source", map[string]any{"type": source.typ})
	}
	if change.SourceVersion != source.version {
		return revision, "", 0, nil, mergeVersionConflict(path+".source_version", source.key, change.SourceVersion, source.version)
	}
	if change.CanonicalVersion != canonical.version {
		return revision, "", 0, nil, mergeVersionConflict(path+".canonical_version", canonical.key, change.CanonicalVersion, canonical.version)
	}
	if duplicateFingerprint(source) == "" || duplicateFingerprint(source) != duplicateFingerprint(canonical) {
		return revision, "", 0, nil, semanticError("semantic_validation", "Record Merge requires an approved project-local similarity candidate", path+".source", map[string]any{"source": source.key, "canonical": canonical.key, "next_action": "review_similarity_candidate"})
	}

	startRevision := revision
	canonicalVersion := canonical.version
	if change.CanonicalRecord != nil || len(change.Clear) != 0 {
		canonicalRecord := change.CanonicalRecord
		if canonicalRecord == nil {
			canonicalRecord = unchangedMergePatch(canonical)
		}
		updated := Change{Op: "update", Key: canonical.key, Version: canonical.version, Type: canonical.typ, Record: canonicalRecord, Clear: change.Clear}
		nextRevision, key, version, _, err := applyUpdateRecord(ctx, tx, projectID, revision, index, updated, now)
		if err != nil {
			return revision, "", 0, nil, err
		}
		revision, canonicalVersion, canonical.key = nextRevision, version, key
	}
	if revision == startRevision {
		nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
		if err != nil {
			return revision, "", 0, nil, err
		}
		revision = nextRevision
	}

	relationships, err := loadMergeRelationships(ctx, tx, projectID, source.key)
	if err != nil {
		return revision, "", 0, nil, err
	}
	for _, relationship := range relationships {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO blackboard_v2_relationship_history(project_id,from_key,relation,to_key,version,reason,recorded_at)
			VALUES(?,?,?,?,?,?,?)`,
			projectID, relationship.from, relationship.relation, relationship.to, relationship.version, relationship.reason, now,
		); err != nil {
			return revision, "", 0, nil, fmt.Errorf("store merged relationship history: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_relationships WHERE project_id=? AND (from_key=? OR to_key=?)`, projectID, source.key, source.key); err != nil {
		return revision, "", 0, nil, fmt.Errorf("remove merged source relationships: %w", err)
	}

	changedRelations := make([]RelationVersionTuple, 0, len(relationships))
	for _, relationship := range relationships {
		from, to := relationship.from, relationship.to
		if from == source.key {
			from = canonical.key
		}
		if to == source.key {
			to = canonical.key
		}
		if from == to {
			continue
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blackboard_v2_relationships WHERE project_id=? AND from_key=? AND relation=? AND to_key=?)`, projectID, from, relationship.relation, to).Scan(&exists); err != nil {
			return revision, "", 0, nil, fmt.Errorf("check merged relationship collision: %w", err)
		}
		if exists != 0 {
			continue
		}
		if err := validateMergedRelationshipCycle(ctx, tx, projectID, relationship.relation, from, to, path); err != nil {
			return revision, "", 0, nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO blackboard_v2_relationships(project_id,from_key,relation,to_key,version,reason,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?)`,
			projectID, from, relationship.relation, to, relationship.version, relationship.reason, relationship.createdAt, relationship.updatedAt,
		); err != nil {
			return revision, "", 0, nil, fmt.Errorf("rewrite merged relationship: %w", err)
		}
		changedRelations = append(changedRelations, RelationVersionTuple{from, relationship.relation, to, relationship.version})
	}

	sourceJSON, err := json.Marshal(source.record)
	if err != nil {
		return revision, "", 0, nil, fmt.Errorf("encode merged source history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_record_history(project_id,key,version,type,record_json,recorded_at)
		VALUES(?,?,?,?,?,?)`, projectID, source.key, source.version, source.typ, string(sourceJSON), now); err != nil {
		return revision, "", 0, nil, fmt.Errorf("store merged source history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_records WHERE project_id=? AND key=?`, projectID, source.key); err != nil {
		return revision, "", 0, nil, fmt.Errorf("remove merged source record: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO blackboard_v2_key_redirects(project_id,source_key,canonical_key,created_at) VALUES(?,?,?,?)`, projectID, source.key, canonical.key, now); err != nil {
		return revision, "", 0, nil, fmt.Errorf("create Blackboard Key Redirect: %w", err)
	}
	sort.Slice(changedRelations, func(i, j int) bool { return relationKey(changedRelations[i]) < relationKey(changedRelations[j]) })
	return revision, canonical.key, canonicalVersion, changedRelations, nil
}

func unchangedMergePatch(record storedRecord) any {
	switch record.typ {
	case "entity":
		value := record.record.entityRecord().Name
		return EntityPatch{Name: &value}
	case "fact":
		value := record.record.factRecord().Summary
		return FactPatch{Summary: &value}
	case "finding":
		value := record.record.findingOutputRecord().Title
		return FindingPatch{Title: &value}
	case "solution":
		value := record.record.solutionRecord().Summary
		return SolutionPatch{Summary: &value}
	case "evidence":
		value := record.record.evidenceRecord().Summary
		return EvidencePatch{Summary: &value}
	default:
		return nil
	}
}

func loadMergeRecord(ctx context.Context, tx *sql.Tx, projectID, key, path string) (storedRecord, error) {
	record, err := loadCurrentRecordDirect(ctx, tx, projectID, key)
	if errors.Is(err, sql.ErrNoRows) {
		return storedRecord{}, semanticError("not_found", fmt.Sprintf("%s was not found", key), path, map[string]any{"key": key})
	}
	return record, err
}

func keyIsRedirect(ctx context.Context, tx *sql.Tx, projectID, key string) (bool, error) {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blackboard_v2_key_redirects WHERE project_id=? AND source_key=?)`, projectID, key).Scan(&exists); err != nil {
		return false, fmt.Errorf("check Blackboard Key Redirect: %w", err)
	}
	return exists != 0, nil
}

func keyHasRedirectSources(ctx context.Context, tx *sql.Tx, projectID, key string) (bool, error) {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blackboard_v2_key_redirects WHERE project_id=? AND canonical_key=?)`, projectID, key).Scan(&exists); err != nil {
		return false, fmt.Errorf("check Blackboard Key Redirect target: %w", err)
	}
	return exists != 0, nil
}

func loadMergeRelationships(ctx context.Context, tx *sql.Tx, projectID, source string) ([]mergeRelationship, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT from_key,relation,to_key,version,reason,created_at,updated_at
		FROM blackboard_v2_relationships
		WHERE project_id=? AND (from_key=? OR to_key=?)
		ORDER BY from_key,relation,to_key`, projectID, source, source)
	if err != nil {
		return nil, fmt.Errorf("read merged source relationships: %w", err)
	}
	defer rows.Close()
	var relationships []mergeRelationship
	for rows.Next() {
		var relationship mergeRelationship
		if err := rows.Scan(&relationship.from, &relationship.relation, &relationship.to, &relationship.version, &relationship.reason, &relationship.createdAt, &relationship.updatedAt); err != nil {
			return nil, fmt.Errorf("scan merged source relationship: %w", err)
		}
		relationships = append(relationships, relationship)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate merged source relationships: %w", err)
	}
	return relationships, nil
}

func validateMergedRelationshipCycle(ctx context.Context, tx *sql.Tx, projectID, relation, from, to, path string) error {
	var cycle bool
	var err error
	if relation == "supports" {
		fromRecord, fromErr := loadCurrentRecordDirect(ctx, tx, projectID, from)
		toRecord, toErr := loadCurrentRecordDirect(ctx, tx, projectID, to)
		if fromErr != nil {
			return fromErr
		}
		if toErr != nil {
			return toErr
		}
		if fromRecord.typ == "fact" && toRecord.typ == "fact" {
			cycle, err = factSupportsWouldCycle(ctx, tx, projectID, from, to)
		}
	} else if isOneOf(relation, "part_of", "derived_from", "depends_on") {
		cycle, err = relationshipWouldCycle(ctx, tx, projectID, relation, from, to)
	}
	if err != nil {
		return err
	}
	if cycle {
		return semanticError("semantic_validation", fmt.Sprintf("merge would create a %s relationship cycle", relation), path+".source", nil)
	}
	return nil
}

func isProjectKnowledgeType(typ string) bool {
	return isOneOf(typ, "entity", "fact", "finding", "solution", "evidence")
}

func mergeVersionConflict(path, key string, expected, current int) error {
	return semanticError("version_conflict", "record changed", path, map[string]any{"key": key, "expected_version": float64(expected), "current_version": float64(current), "next_action": "read_current_record"})
}

func duplicateFingerprint(record storedRecord) string {
	value := func(parts ...string) string {
		hash := sha256.Sum256([]byte("CyberPenda.BlackboardV2.Duplicate.v1\x00" + record.typ + "\x00" + strings.Join(parts, "\x00")))
		return hex.EncodeToString(hash[:])
	}
	switch record.typ {
	case "entity":
		entity := record.record.entityRecord()
		locator := normalizeMergeLocator(entity.Kind, entity.Locator)
		if entity.Kind == "" || locator == "" {
			return ""
		}
		return value(entity.Kind, locator)
	case "fact":
		fact := record.record.factRecord()
		summary := normalizeMergeText(fact.Summary)
		if fact.Category == "" || summary == "" {
			return ""
		}
		return value(fact.Category, summary)
	case "finding":
		finding := record.record.findingOutputRecord()
		target, title := normalizeMergeText(finding.Target), normalizeMergeText(finding.Title)
		if target == "" || title == "" {
			return ""
		}
		return value(target, title)
	case "solution":
		solution := record.record.solutionRecord()
		if solution.Kind == "" || solution.Value == "" {
			return ""
		}
		return value(solution.Kind, solution.Value)
	case "evidence":
		evidence := record.record.evidenceRecord()
		if evidence.SHA256 == "" {
			return ""
		}
		return value(evidence.SHA256)
	default:
		return ""
	}
}

func normalizeMergeText(value string) string {
	value = strings.ToLower(norm.NFKC.String(value))
	var normalized strings.Builder
	space := true
	for _, r := range value {
		if unicode.IsPunct(r) || unicode.IsSpace(r) || unicode.Is(unicode.Z, r) {
			if !space {
				normalized.WriteByte(' ')
				space = true
			}
			continue
		}
		normalized.WriteRune(r)
		space = false
	}
	return strings.TrimSpace(normalized.String())
}

func normalizeMergeLocator(kind, locator string) string {
	locator = strings.TrimSpace(norm.NFKC.String(locator))
	switch kind {
	case "host":
		return strings.TrimSuffix(strings.ToLower(locator), ".")
	case "endpoint":
		parsed, err := url.Parse(locator)
		if err == nil && parsed.IsAbs() {
			parsed.Scheme = strings.ToLower(parsed.Scheme)
			parsed.Host = strings.ToLower(parsed.Host)
			return parsed.String()
		}
	}
	return locator
}
