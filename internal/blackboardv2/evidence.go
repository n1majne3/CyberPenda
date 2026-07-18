package blackboardv2

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"pentest/internal/store"
)

// EvidenceConfig supplies the managed and Runtime filesystem roots used by
// RetainEvidenceForContinuation.
type EvidenceConfig struct {
	ArtifactRoot string
	RuntimeRoot  string
	Failures     EvidenceFailureInjector
}

// EvidenceFailurePoint names a durable retention boundary used to verify
// uncertain-retry recovery.
type EvidenceFailurePoint string

const (
	EvidenceFailureBeforeReservation      EvidenceFailurePoint = "before_reservation"
	EvidenceFailureBeforeFilePublish      EvidenceFailurePoint = "before_file_publish"
	EvidenceFailureAfterDirectoryCreate   EvidenceFailurePoint = "after_managed_directory_create"
	EvidenceFailureAfterDirectorySync     EvidenceFailurePoint = "after_managed_directory_parent_sync"
	EvidenceFailureBeforeTempCopy         EvidenceFailurePoint = "before_temp_copy"
	EvidenceFailureMidTempCopy            EvidenceFailurePoint = "mid_temp_copy"
	EvidenceFailureAfterTempDirectorySync EvidenceFailurePoint = "after_temp_directory_sync"
	EvidenceFailureAfterTempSync          EvidenceFailurePoint = "after_temp_sync"
	EvidenceFailureBeforeFileRename       EvidenceFailurePoint = "before_file_rename"
	EvidenceFailureAfterFileRename        EvidenceFailurePoint = "after_file_rename"
	EvidenceFailureBeforePublishStore     EvidenceFailurePoint = "before_publication_checkpoint"
	EvidenceFailureAfterFilePublish       EvidenceFailurePoint = "file_publish"
	EvidenceFailureAfterPayloadGCClaim    EvidenceFailurePoint = "after_payload_gc_claim"
	EvidenceFailureAfterPayloadUnlink     EvidenceFailurePoint = "after_payload_unlink"
	EvidenceFailureAfterGraphCommit       EvidenceFailurePoint = "semantic_commit"
	EvidenceFailureAfterResultStore       EvidenceFailurePoint = "result_store"
)

// EvidenceFailureInjector can simulate a lost response or process failure at
// a durable retention boundary.
type EvidenceFailureInjector interface {
	FailAfter(EvidenceFailurePoint) error
}

// EvidenceLink is a closed [relation,target_key] pair.
type EvidenceLink [2]string

// UnmarshalJSON rejects non-pair and non-string Evidence links.
func (link *EvidenceLink) UnmarshalJSON(raw []byte) error {
	var values []string
	if err := decodeJSON(raw, &values); err != nil {
		return fmt.Errorf("decode Evidence link: %w", err)
	}
	if len(values) != 2 {
		return fmt.Errorf("Evidence link must contain relation and target key")
	}
	*link = EvidenceLink{values[0], values[1]}
	return nil
}

// RetainEvidenceRequest is the closed runtime Evidence request from the v2
// contract. Project, origin, produced, and integrity values are server-bound.
type RetainEvidenceRequest struct {
	IdempotencyKey string         `json:"idempotency_key"`
	Key            string         `json:"key"`
	Version        int            `json:"version,omitempty"`
	Attempt        string         `json:"attempt"`
	SourcePath     string         `json:"source_path"`
	ArtifactType   string         `json:"artifact_type"`
	Summary        string         `json:"summary"`
	MediaType      string         `json:"media_type,omitempty"`
	CapturedAt     string         `json:"captured_at,omitempty"`
	Links          []EvidenceLink `json:"links,omitempty"`
}

// UnmarshalJSON enforces the frozen closed request before adapters can discard
// unknown fields.
func (request *RetainEvidenceRequest) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := decodeJSON(raw, &fields); err != nil {
		return err
	}
	allowed := map[string]bool{
		"idempotency_key": true, "key": true, "version": true, "attempt": true,
		"source_path": true, "artifact_type": true, "summary": true,
		"media_type": true, "captured_at": true, "links": true,
	}
	if err := rejectUnknownFields(fields, allowed); err != nil {
		return err
	}
	result := RetainEvidenceRequest{}
	var err error
	if result.IdempotencyKey, err = decodeRequiredString(fields, "idempotency_key"); err != nil {
		return err
	}
	if result.Key, err = decodeRequiredString(fields, "key"); err != nil {
		return err
	}
	if result.Attempt, err = decodeRequiredString(fields, "attempt"); err != nil {
		return err
	}
	if result.SourcePath, err = decodeRequiredString(fields, "source_path"); err != nil {
		return err
	}
	if result.ArtifactType, err = decodeRequiredString(fields, "artifact_type"); err != nil {
		return err
	}
	if result.Summary, err = decodeRequiredString(fields, "summary"); err != nil {
		return err
	}
	for field, destination := range map[string]*string{"media_type": &result.MediaType, "captured_at": &result.CapturedAt} {
		if value, ok := fields[field]; ok {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return fmt.Errorf("%s must be a string", field)
			}
			if err := decodeJSON(value, destination); err != nil {
				return fmt.Errorf("decode %s: %w", field, err)
			}
			if *destination == "" {
				return fmt.Errorf("%s must not be empty", field)
			}
		}
	}
	if value, ok := fields["version"]; ok {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("version must be a positive integer")
		}
		if err := decodeJSON(value, &result.Version); err != nil {
			return fmt.Errorf("decode version: %w", err)
		}
		if result.Version < 1 {
			return fmt.Errorf("version must be a positive integer")
		}
	}
	if value, ok := fields["links"]; ok {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("links must be an array")
		}
		if err := decodeJSON(value, &result.Links); err != nil {
			return fmt.Errorf("decode links: %w", err)
		}
	}
	*request = result
	return nil
}

type evidenceSource struct {
	root         *os.Root
	file         *os.File
	relativePath string
	identity     string
	sha256       string
	size         int64
	info         os.FileInfo
}

type evidenceRequestRow struct {
	requestHash     string
	sourceIdentity  string
	sha256          string
	size            int64
	internalPath    string
	tempPath        string
	previousTemp    string
	migration27Temp string
	publisherToken  string
	publisherID     string
	payloadOwned    bool
	status          string
	resultJSON      string
}

type retainedEvidenceMetadata struct {
	sha256 string
	size   int64
}

// RetainEvidenceForContinuation retains one confined payload and atomically
// publishes its semantic Evidence record and relationships.
func (s *Service) RetainEvidenceForContinuation(ctx context.Context, projectID, continuationID string, request RetainEvidenceRequest) (ChangeResult, error) {
	if err := validateRetainEvidenceRequest(request); err != nil {
		return ChangeResult{}, err
	}
	if continuationID == "" {
		return ChangeResult{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	requestHash, err := retainedEvidenceRequestHash(request)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("hash retained Evidence request: %w", err)
	}
	row, exists, err := s.readEvidenceRequest(ctx, projectID, continuationID, request.IdempotencyKey)
	if err != nil {
		return ChangeResult{}, err
	}
	if exists {
		if row.requestHash != requestHash {
			return ChangeResult{}, semanticError("idempotency_conflict", "idempotency key was already used with different semantics", "idempotency_key", map[string]any{"idempotency_key": request.IdempotencyKey})
		}
		if row.status == "completed" {
			var replay ChangeResult
			if err := decodeJSON([]byte(row.resultJSON), &replay); err != nil {
				return ChangeResult{}, fmt.Errorf("decode retained Evidence replay: %w", err)
			}
			return replay, nil
		}
		if replay, found, err := s.readRetainedEvidenceSemanticReplay(ctx, projectID, continuationID, request.IdempotencyKey, requestHash); err != nil {
			return ChangeResult{}, err
		} else if found {
			resultJSON, err := json.Marshal(replay)
			if err != nil {
				return ChangeResult{}, fmt.Errorf("encode recovered Evidence result: %w", err)
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET status='completed',result_json=?,updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, string(resultJSON), time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, request.IdempotencyKey); err != nil {
				return ChangeResult{}, fmt.Errorf("complete recovered Evidence request: %w", err)
			}
			return replay, nil
		}
	}
	request, err = s.resolveRetainedEvidenceRedirects(ctx, projectID, request)
	if err != nil {
		return ChangeResult{}, err
	}
	taskID, status, err := s.continuationTaskStatus(ctx, projectID, continuationID)
	if err != nil {
		return ChangeResult{}, err
	}
	if !exists && !continuationCanWrite(status) {
		return ChangeResult{}, semanticError("closed_continuation", "trusted Continuation is closed for new Blackboard writes", "", nil)
	}
	if strings.TrimSpace(s.evidenceConfig.RuntimeRoot) == "" || strings.TrimSpace(s.evidenceConfig.ArtifactRoot) == "" {
		return ChangeResult{}, fmt.Errorf("Evidence Runtime Root and Artifact Root must be configured")
	}
	if exists {
		recovered, err := s.recoverOwnedEvidenceGC(ctx, projectID, continuationID, request.IdempotencyKey, row)
		if err != nil {
			return ChangeResult{}, err
		}
		if recovered {
			exists = false
			row = evidenceRequestRow{}
		}
	}
	var source *evidenceSource
	if !exists {
		if err := s.validateRetainedEvidencePreconditions(ctx, projectID, continuationID, request); err != nil {
			return ChangeResult{}, err
		}
		opened, err := s.openRuntimeEvidenceSource(taskID, request.SourcePath)
		if err != nil {
			return ChangeResult{}, err
		}
		source = &opened
		defer source.file.Close()
		defer source.root.Close()
		internalPath, err := plannedEvidenceInternalPath(projectID, source.sha256, source.relativePath)
		if err != nil {
			return ChangeResult{}, err
		}
		if err := s.failEvidence(EvidenceFailureBeforeReservation); err != nil {
			return ChangeResult{}, err
		}
		row, _, err = s.reserveEvidenceRequest(ctx, projectID, continuationID, request.IdempotencyKey, requestHash, internalPath, *source)
		if err != nil {
			return ChangeResult{}, err
		}
		if row.status == "completed" {
			var replay ChangeResult
			if err := decodeJSON([]byte(row.resultJSON), &replay); err != nil {
				return ChangeResult{}, fmt.Errorf("decode raced Evidence replay: %w", err)
			}
			return replay, nil
		}
	}
	semanticPath, err := semanticEvidencePath(projectID, row.internalPath, row.sha256)
	if err != nil {
		return ChangeResult{}, err
	}
	payloadReady, err := s.verifyManagedEvidencePayload(row.internalPath, row.sha256, row.size)
	if err != nil {
		return ChangeResult{}, err
	}
	if !payloadReady {
		tempReady, err := s.verifyJournaledEvidenceTemp(row.tempPath, row.sha256, row.size)
		if err != nil {
			return ChangeResult{}, err
		}
		recoveryExists, offlineRecoveryValid, err := s.evidencePublicationRecoveryExists(ctx, projectID, continuationID, request.IdempotencyKey, row)
		if err != nil {
			return ChangeResult{}, err
		}
		if !tempReady && !offlineRecoveryValid && source == nil {
			opened, err := s.openRuntimeEvidenceSource(taskID, request.SourcePath)
			if err != nil && !recoveryExists {
				s.cleanupDefinitiveEvidenceFailure(ctx, projectID, continuationID, request.IdempotencyKey, row, err)
				return ChangeResult{}, err
			}
			if err == nil {
				source = &opened
				defer source.file.Close()
				defer source.root.Close()
			}
		}
		if source != nil {
			if err := validateEvidenceReservationRow(row, requestHash, *source); err != nil {
				s.cleanupDefinitiveEvidenceFailure(ctx, projectID, continuationID, request.IdempotencyKey, row, err)
				return ChangeResult{}, err
			}
		}
		if err := s.ensureEvidencePublished(ctx, projectID, continuationID, request.IdempotencyKey, &row, source); err != nil {
			s.cleanupDefinitiveEvidenceFailure(ctx, projectID, continuationID, request.IdempotencyKey, row, err)
			return ChangeResult{}, err
		}
	} else if row.status == "reserved" {
		if err := s.syncAndCheckpointPublishedEvidence(ctx, projectID, continuationID, request.IdempotencyKey, row.internalPath, row.tempPath); err != nil {
			return ChangeResult{}, err
		}
	}
	payloadReady, err = s.verifyManagedEvidencePayload(row.internalPath, row.sha256, row.size)
	if err != nil {
		return ChangeResult{}, err
	}
	if !payloadReady {
		return ChangeResult{}, semanticError("evidence_integrity_failed", "managed Evidence payload failed integrity verification before semantic commit", "key", nil)
	}
	result, err := s.applyRetainedEvidence(ctx, projectID, continuationID, request, requestHash, semanticPath, retainedEvidenceMetadata{sha256: row.sha256, size: row.size}, true)
	if err != nil {
		s.cleanupDefinitiveEvidenceFailure(ctx, projectID, continuationID, request.IdempotencyKey, row, err)
		return ChangeResult{}, err
	}
	if err := s.failEvidence(EvidenceFailureAfterGraphCommit); err != nil {
		return ChangeResult{}, err
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("encode retained Evidence result: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET status='completed',result_json=?,updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, string(resultJSON), time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, request.IdempotencyKey); err != nil {
		return ChangeResult{}, fmt.Errorf("complete Evidence request: %w", err)
	}
	if err := s.failEvidence(EvidenceFailureAfterResultStore); err != nil {
		return ChangeResult{}, err
	}
	return result, nil
}

func (s *Service) resolveRetainedEvidenceRedirects(ctx context.Context, projectID string, request RetainEvidenceRequest) (RetainEvidenceRequest, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RetainEvidenceRequest{}, fmt.Errorf("begin Evidence redirect resolution: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureProjectExists(ctx, tx, projectID); err != nil {
		return RetainEvidenceRequest{}, err
	}
	return resolveRetainedEvidenceRedirectsTx(ctx, tx, projectID, request)
}

func resolveRetainedEvidenceRedirectsTx(ctx context.Context, tx *sql.Tx, projectID string, request RetainEvidenceRequest) (RetainEvidenceRequest, error) {
	var err error
	request.Key, _, err = resolveKeyRedirect(ctx, tx, projectID, request.Key)
	if err != nil {
		return RetainEvidenceRequest{}, err
	}
	for index := range request.Links {
		request.Links[index][1], _, err = resolveKeyRedirect(ctx, tx, projectID, request.Links[index][1])
		if err != nil {
			return RetainEvidenceRequest{}, err
		}
	}
	return request, nil
}

func (s *Service) readRetainedEvidenceSemanticReplay(ctx context.Context, projectID, continuationID, idempotencyKey, requestHash string) (ChangeResult, bool, error) {
	receiptKey := "retain-evidence-v2:" + continuationID + ":" + idempotencyKey
	var storedHash, raw, storedContinuationID string
	err := s.db.QueryRowContext(ctx, `SELECT request_hash,result_json,continuation_id FROM blackboard_v2_idempotency_receipts WHERE project_id=? AND idempotency_key=?`, projectID, receiptKey).Scan(&storedHash, &raw, &storedContinuationID)
	if errors.Is(err, sql.ErrNoRows) {
		return ChangeResult{}, false, nil
	}
	if err != nil {
		return ChangeResult{}, false, fmt.Errorf("read retained Evidence semantic receipt: %w", err)
	}
	if storedContinuationID != continuationID {
		return ChangeResult{}, false, semanticError("authority_denied", "retained Evidence receipt belongs to another trusted origin", "idempotency_key", nil)
	}
	if storedHash != requestHash {
		return ChangeResult{}, false, semanticError("idempotency_conflict", "idempotency key was already used with different semantics", "idempotency_key", map[string]any{"idempotency_key": idempotencyKey})
	}
	var result ChangeResult
	if err := decodeJSON([]byte(raw), &result); err != nil {
		return ChangeResult{}, false, fmt.Errorf("decode retained Evidence semantic receipt: %w", err)
	}
	return result, true, nil
}

func (s *Service) validateRetainedEvidencePreconditions(ctx context.Context, projectID, continuationID string, request RetainEvidenceRequest) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin producing Attempt validation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureProjectExists(ctx, tx, projectID); err != nil {
		return err
	}
	attempt, err := loadCurrentRecord(ctx, tx, projectID, request.Attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return semanticError("semantic_validation", "producing Attempt must be current and open", "attempt", map[string]any{"key": request.Attempt})
	}
	if err != nil {
		return err
	}
	if attempt.typ != "attempt" {
		return semanticError("semantic_validation", "attempt must reference an Attempt", "attempt", nil)
	}
	if err := requireAttemptOwner(ctx, tx, projectID, request.Attempt, continuationID, "attempt"); err != nil {
		return err
	}
	if attempt.record.attemptRecord().Status != "open" {
		return semanticError("semantic_validation", "producing Attempt must be current and open", "attempt", map[string]any{"key": request.Attempt})
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, request.Key)
	if errors.Is(err, sql.ErrNoRows) {
		if request.Version != 0 {
			return semanticError("not_found", fmt.Sprintf("%s was not found", request.Key), "key", map[string]any{"key": request.Key})
		}
		if used, err := historicalKeyExists(ctx, tx, projectID, request.Key); err != nil {
			return err
		} else if used {
			return semanticError("key_conflict", fmt.Sprintf("%s already exists in Semantic History", request.Key), "key", map[string]any{"key": request.Key})
		}
	} else {
		if err != nil {
			return err
		}
		if existing.typ != "evidence" {
			return semanticError("key_conflict", fmt.Sprintf("%s already exists", request.Key), "key", map[string]any{"key": request.Key})
		}
		if request.Version == 0 {
			return semanticError("semantic_validation", "current Evidence version is required for replacement", "version", nil)
		}
		if request.Version != existing.version {
			return semanticError("version_conflict", fmt.Sprintf("%s changed", request.Key), "version", map[string]any{"key": request.Key, "expected_version": float64(request.Version), "current_version": float64(existing.version), "next_action": "read_current_record"})
		}
	}
	for index, link := range request.Links {
		target, err := loadCurrentRecord(ctx, tx, projectID, link[1])
		if errors.Is(err, sql.ErrNoRows) {
			return semanticError("not_found", fmt.Sprintf("%s was not found", link[1]), fmt.Sprintf("links[%d][1]", index), map[string]any{"key": link[1]})
		}
		if err != nil {
			return err
		}
		if err := validateRelationshipEndpoint(link[0], "evidence", target.typ, fmt.Sprintf("links[%d][0]", index)); err != nil {
			return err
		}
	}
	return nil
}

func validateRetainEvidenceRequest(request RetainEvidenceRequest) error {
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		return semanticError("semantic_validation", "idempotency_key is required", "idempotency_key", nil)
	}
	if err := validateKey(request.Key, "key"); err != nil {
		return err
	}
	if request.Version < 0 {
		return semanticError("semantic_validation", "version must be positive when provided", "version", nil)
	}
	if err := validateKey(request.Attempt, "attempt"); err != nil {
		return err
	}
	if strings.TrimSpace(request.SourcePath) == "" {
		return semanticError("semantic_validation", "source_path is required", "source_path", nil)
	}
	if err := validateConciseText(request.ArtifactType, "artifact_type"); err != nil {
		return err
	}
	if err := validateSemanticText(request.Summary, "summary"); err != nil {
		return err
	}
	if request.MediaType != "" {
		if err := validateConciseText(request.MediaType, "media_type"); err != nil {
			return err
		}
	}
	if request.CapturedAt != "" {
		if _, err := time.Parse(time.RFC3339, request.CapturedAt); err != nil {
			return semanticError("semantic_validation", "captured_at must be an RFC3339 timestamp", "captured_at", nil)
		}
	}
	for index, link := range request.Links {
		if link[0] != "evidences" && link[0] != "about" {
			return semanticError("semantic_validation", "Retain Evidence links may only be evidences or about", fmt.Sprintf("links[%d][0]", index), nil)
		}
		if err := validateKey(link[1], fmt.Sprintf("links[%d][1]", index)); err != nil {
			return err
		}
	}
	return nil
}

func retainedEvidenceRequestHash(request RetainEvidenceRequest) (string, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) continuationTaskStatus(ctx context.Context, projectID, continuationID string) (string, string, error) {
	var taskID, status string
	err := s.db.QueryRowContext(ctx, `SELECT task.id, continuation.status FROM task_continuations AS continuation JOIN tasks AS task ON task.id=continuation.task_id WHERE continuation.id=? AND task.project_id=?`, continuationID, projectID).Scan(&taskID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", semanticError("authority_denied", "trusted Continuation does not own this Project interface", "", nil)
	}
	if err != nil {
		return "", "", fmt.Errorf("validate trusted Continuation Project: %w", err)
	}
	return taskID, status, nil
}

func (s *Service) openRuntimeEvidenceSource(taskID, sourcePath string) (evidenceSource, error) {
	rootPath, relativePath, err := s.runtimeEvidenceSourceLocation(taskID, sourcePath)
	if err != nil {
		return evidenceSource{}, err
	}
	anchor, err := os.OpenRoot(s.evidenceConfig.RuntimeRoot)
	if err != nil {
		return evidenceSource{}, semanticError("evidence_source_forbidden", "Runtime Evidence root cannot be opened", "source_path", nil)
	}
	defer anchor.Close()
	rootRelative, ok := relativeWithinRoot(s.evidenceConfig.RuntimeRoot, rootPath)
	if !ok {
		return evidenceSource{}, semanticError("evidence_source_forbidden", "Evidence source escapes the Task roots", "source_path", nil)
	}
	root, file, info, fileName, err := openSecureRegularFile(anchor, filepath.Join(rootRelative, relativePath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return evidenceSource{}, semanticError("evidence_source_changed", "Evidence source is missing", "source_path", nil)
		}
		return evidenceSource{}, semanticError("evidence_source_forbidden", "Evidence source escapes permitted roots or is not readable", "source_path", nil)
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		file.Close()
		root.Close()
		return evidenceSource{}, fmt.Errorf("hash Evidence source: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		root.Close()
		return evidenceSource{}, fmt.Errorf("rewind Evidence source: %w", err)
	}
	identityPath := filepath.Join(rootPath, relativePath)
	return evidenceSource{root: root, file: file, relativePath: fileName, identity: fileIdentity(identityPath, info), sha256: hex.EncodeToString(hash.Sum(nil)), size: size, info: info}, nil
}

func openSecureRegularFile(root *os.Root, relative string) (*os.Root, *os.File, os.FileInfo, string, error) {
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, nil, nil, "", errors.New("Evidence source path is not a confined file")
	}
	directory, err := openExistingSecureDirectory(root, filepath.Dir(clean))
	if err != nil {
		return nil, nil, nil, "", err
	}
	name := filepath.Base(clean)
	info, err := directory.Lstat(name)
	if err != nil {
		directory.Close()
		return nil, nil, nil, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		directory.Close()
		return nil, nil, nil, "", errors.New("Evidence source contains a symbolic link or is not a regular file")
	}
	file, err := directory.Open(name)
	if err != nil {
		directory.Close()
		return nil, nil, nil, "", err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		file.Close()
		directory.Close()
		return nil, nil, nil, "", errors.New("Evidence source changed while opening")
	}
	return directory, file, info, name, nil
}

func openExistingSecureDirectory(root *os.Root, relative string) (*os.Root, error) {
	clean := filepath.Clean(relative)
	if clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, errors.New("Evidence source directory escapes its root")
	}
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	if clean == "." {
		return current, nil
	}
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		info, err := current.Lstat(component)
		if err != nil {
			current.Close()
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			current.Close()
			return nil, errors.New("Evidence source directory contains a symbolic link or non-directory")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			current.Close()
			return nil, err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			next.Close()
			current.Close()
			return nil, errors.New("Evidence source directory changed while opening")
		}
		current.Close()
		current = next
	}
	return current, nil
}

func (s *Service) runtimeEvidenceSourceLocation(taskID, sourcePath string) (string, string, error) {
	taskRoot := filepath.Join(s.evidenceConfig.RuntimeRoot, taskID)
	workdir := filepath.Join(taskRoot, "workdir")
	artifacts := filepath.Join(taskRoot, "artifacts")
	clean := filepath.Clean(sourcePath)
	switch {
	case filepath.IsAbs(clean) && (clean == "/task/workdir" || strings.HasPrefix(clean, "/task/workdir/")):
		return workdir, strings.TrimPrefix(strings.TrimPrefix(clean, "/task/workdir"), "/"), nil
	case filepath.IsAbs(clean) && (clean == "/task/artifacts" || strings.HasPrefix(clean, "/task/artifacts/")):
		return artifacts, strings.TrimPrefix(strings.TrimPrefix(clean, "/task/artifacts"), "/"), nil
	case !filepath.IsAbs(clean):
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", "", semanticError("evidence_source_forbidden", "Evidence source escapes the Task roots", "source_path", nil)
		}
		return workdir, clean, nil
	}
	if relative, ok := relativeWithinRoot(workdir, clean); ok {
		return workdir, relative, nil
	}
	if relative, ok := relativeWithinRoot(artifacts, clean); ok {
		return artifacts, relative, nil
	}
	return "", "", semanticError("evidence_source_forbidden", "Evidence source escapes the Task roots", "source_path", nil)
}

func relativeWithinRoot(root, path string) (string, bool) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	relative, err := filepath.Rel(absRoot, absPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", false
	}
	return relative, true
}

func fileIdentity(path string, info os.FileInfo) string {
	identity := filepath.Clean(path)
	value := reflect.ValueOf(info.Sys())
	if value.IsValid() && value.Kind() == reflect.Pointer && !value.IsNil() {
		value = value.Elem()
	}
	if value.IsValid() && value.Kind() == reflect.Struct {
		device, inode := value.FieldByName("Dev"), value.FieldByName("Ino")
		if device.IsValid() && inode.IsValid() && device.CanInterface() && inode.CanInterface() {
			identity += fmt.Sprintf("\x00dev=%v\x00ino=%v", device.Interface(), inode.Interface())
		}
	}
	return identity
}

func plannedEvidenceInternalPath(projectID, digest, sourcePath string) (string, error) {
	if len(digest) != 64 {
		return "", fmt.Errorf("invalid Evidence digest")
	}
	name := filepath.Base(sourcePath)
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = "artifact"
	}
	projectDigest := sha256.Sum256([]byte(projectID))
	path := filepath.Join("projects", hex.EncodeToString(projectDigest[:]), "retained", digest, name)
	if clean := filepath.Clean(path); clean != path || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("planned Evidence path escapes managed storage")
	}
	return path, nil
}

func plannedEvidenceTempPath(internalPath, continuationID, key, requestHash string) (string, error) {
	if len(requestHash) != 64 || continuationID == "" || key == "" {
		return "", fmt.Errorf("invalid Evidence request hash")
	}
	marker := string(filepath.Separator) + "retained" + string(filepath.Separator)
	index := strings.Index(internalPath, marker)
	if index <= 0 {
		return "", fmt.Errorf("planned Evidence path lacks its retained namespace")
	}
	projectRoot := internalPath[:index]
	scope := sha256.Sum256([]byte(continuationID + "\x00" + key))
	path := filepath.Join(projectRoot, ".evidence-staging", hex.EncodeToString(scope[:]), requestHash)
	if clean := filepath.Clean(path); clean != path || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.Contains(clean, marker) {
		return "", fmt.Errorf("planned Evidence temp path escapes private staging")
	}
	return path, nil
}

func migration27EvidenceTempPath(internalPath, continuationID, key, requestHash string) (string, error) {
	if len(requestHash) != 64 || continuationID == "" || key == "" {
		return "", fmt.Errorf("invalid Evidence request hash")
	}
	marker := string(filepath.Separator) + "retained" + string(filepath.Separator)
	index := strings.Index(internalPath, marker)
	if index <= 0 {
		return "", fmt.Errorf("planned Evidence path lacks its retained namespace")
	}
	return filepath.Join(internalPath[:index], ".evidence-staging", hex.EncodeToString([]byte(continuationID)), hex.EncodeToString([]byte(key)), requestHash), nil
}

func filesystemSafeEvidencePath(path string) bool {
	clean := filepath.Clean(path)
	if clean != path || clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false
	}
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." || len(component) > 255 {
			return false
		}
	}
	return true
}

func semanticEvidencePath(projectID, internalPath, digest string) (string, error) {
	planned, err := plannedEvidenceInternalPath(projectID, digest, filepath.Base(internalPath))
	if err != nil {
		return "", err
	}
	if filepath.Clean(internalPath) != planned {
		return "", fmt.Errorf("stored Evidence path does not match its Project and digest")
	}
	return filepath.ToSlash(filepath.Join("artifacts", "retained", digest, filepath.Base(internalPath))), nil
}

func validateEvidenceReservationRow(row evidenceRequestRow, requestHash string, source evidenceSource) error {
	if row.requestHash != requestHash {
		return semanticError("idempotency_conflict", "idempotency key was already used with different semantics", "idempotency_key", nil)
	}
	if row.sourceIdentity != source.identity || row.sha256 != source.sha256 || row.size != source.size {
		return semanticError("evidence_source_changed", "Evidence source changed across idempotent retry", "source_path", nil)
	}
	if !isOneOf(row.status, "reserved", "published", "completed") {
		return fmt.Errorf("invalid retained Evidence request status %q", row.status)
	}
	return nil
}

func (s *Service) reserveEvidenceRequest(ctx context.Context, projectID, continuationID, key, requestHash, internalPath string, source evidenceSource) (evidenceRequestRow, bool, error) {
	tempPath, err := plannedEvidenceTempPath(internalPath, continuationID, key, requestHash)
	if err != nil {
		return evidenceRequestRow{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("begin Evidence reservation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	status, err := continuationProjectStatus(ctx, tx, projectID, continuationID)
	if err != nil {
		return evidenceRequestRow{}, false, err
	}
	if !continuationCanWrite(status) {
		return evidenceRequestRow{}, false, semanticError("closed_continuation", "trusted Continuation is closed for new Blackboard writes", "", nil)
	}
	currentOwner, pinned, err := continuationOwnsCurrentWorkingPath(ctx, tx, continuationID)
	if err != nil {
		return evidenceRequestRow{}, false, err
	}
	if pinned && !currentOwner {
		return evidenceRequestRow{}, false, semanticError("closed_continuation", "trusted Continuation no longer owns the Task Working Snapshot", "", nil)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO blackboard_v2_evidence_requests(project_id,continuation_id,idempotency_key,request_hash,source_identity,source_sha256,source_size_bytes,managed_internal_path,temp_internal_path,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?, 'reserved',?,?)`, projectID, continuationID, key, requestHash, source.identity, source.sha256, source.size, internalPath, tempPath, now, now)
	if err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("reserve Evidence request: %w", err)
	}
	rows, _ := result.RowsAffected()
	var row evidenceRequestRow
	var payloadOwned int
	if err := tx.QueryRowContext(ctx, `SELECT request_hash,source_identity,source_sha256,source_size_bytes,managed_internal_path,temp_internal_path,previous_temp_internal_path,migration27_temp_internal_path,publisher_token,publisher_temp_identity,payload_owned,status,result_json FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, projectID, continuationID, key).Scan(&row.requestHash, &row.sourceIdentity, &row.sha256, &row.size, &row.internalPath, &row.tempPath, &row.previousTemp, &row.migration27Temp, &row.publisherToken, &row.publisherID, &payloadOwned, &row.status, &row.resultJSON); err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("read reserved Evidence request: %w", err)
	}
	row.payloadOwned = payloadOwned == 1
	if row.requestHash != requestHash || row.internalPath != internalPath || row.tempPath != tempPath {
		return evidenceRequestRow{}, false, semanticError("idempotency_conflict", "idempotency key was already used with different semantics", "idempotency_key", map[string]any{"idempotency_key": key})
	}
	if err := validateEvidenceReservationRow(row, requestHash, source); err != nil {
		return evidenceRequestRow{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO blackboard_v2_evidence_payloads(project_id,managed_internal_path,sha256,size_bytes,state,created_at,updated_at) VALUES(?,?,?,?,'active',?,?)`, projectID, internalPath, source.sha256, source.size, now, now); err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("claim Evidence payload path: %w", err)
	}
	var payloadDigest, payloadState string
	var payloadSize int64
	if err := tx.QueryRowContext(ctx, `SELECT sha256,size_bytes,state FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=?`, projectID, internalPath).Scan(&payloadDigest, &payloadSize, &payloadState); err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("read Evidence payload claim: %w", err)
	}
	if payloadDigest != source.sha256 || payloadSize != source.size {
		return evidenceRequestRow{}, false, fmt.Errorf("Evidence payload claim metadata conflict")
	}
	if payloadState == "gc" {
		return evidenceRequestRow{}, false, &Error{Code: "evidence_payload_gc_in_progress", Message: "Evidence payload cleanup is in progress", Path: "source_path", Retryable: true}
	}
	if err := tx.Commit(); err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("commit Evidence reservation: %w", err)
	}
	return row, rows == 1, nil
}

func (s *Service) readEvidenceRequest(ctx context.Context, projectID, continuationID, key string) (evidenceRequestRow, bool, error) {
	var row evidenceRequestRow
	var payloadOwned int
	err := s.db.QueryRowContext(ctx, `SELECT request_hash,source_identity,source_sha256,source_size_bytes,managed_internal_path,temp_internal_path,previous_temp_internal_path,migration27_temp_internal_path,publisher_token,publisher_temp_identity,payload_owned,status,result_json FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, projectID, continuationID, key).Scan(&row.requestHash, &row.sourceIdentity, &row.sha256, &row.size, &row.internalPath, &row.tempPath, &row.previousTemp, &row.migration27Temp, &row.publisherToken, &row.publisherID, &payloadOwned, &row.status, &row.resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return evidenceRequestRow{}, false, nil
	}
	if err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("read Evidence request: %w", err)
	}
	row.payloadOwned = payloadOwned == 1
	return row, true, nil
}

func (s *Service) verifyManagedEvidencePayload(internalPath, digest string, size int64) (bool, error) {
	root, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
	if err != nil {
		return false, fmt.Errorf("open managed Artifact Root: %w", err)
	}
	defer root.Close()
	directory, file, info, _, err := openSecureRegularFile(root, internalPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, semanticError("evidence_integrity_failed", "managed Evidence path is missing, symlinked, or unsafe", "key", nil)
	}
	defer directory.Close()
	defer file.Close()
	if !info.Mode().IsRegular() {
		return false, semanticError("evidence_integrity_failed", "managed Evidence payload is not a regular file", "key", nil)
	}
	hash := sha256.New()
	actualSize, err := io.Copy(hash, file)
	if err != nil {
		return false, fmt.Errorf("hash managed Evidence payload: %w", err)
	}
	if actualSize != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return false, semanticError("evidence_integrity_failed", "managed Evidence payload failed integrity verification", "key", nil)
	}
	return true, nil
}

func (s *Service) evidenceIntegrityValid(projectID string, record EvidenceRecord) (bool, error) {
	if strings.TrimSpace(s.evidenceConfig.ArtifactRoot) == "" || record.Status != "available" {
		return false, nil
	}
	internalPath, err := plannedEvidenceInternalPath(projectID, record.SHA256, filepath.Base(record.ManagedPath))
	if err != nil {
		return false, nil
	}
	semanticPath, err := semanticEvidencePath(projectID, internalPath, record.SHA256)
	if err != nil || semanticPath != record.ManagedPath {
		return false, nil
	}
	valid, err := s.verifyManagedEvidencePayload(internalPath, record.SHA256, record.Size)
	if err != nil {
		var semanticErr *Error
		if errors.As(err, &semanticErr) {
			return false, nil
		}
		return false, err
	}
	return valid, nil
}

func collectEvidenceDependentConfirmedFacts(ctx context.Context, tx *sql.Tx, projectID, evidenceKey, path string, dependent map[string]string) error {
	record, err := loadCurrentRecord(ctx, tx, projectID, evidenceKey)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && record.typ != "evidence") {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT fact.key, fact.record_json
		FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS fact
		  ON fact.project_id=rel.project_id AND fact.key=rel.to_key AND fact.type='fact'
		WHERE rel.project_id=? AND rel.from_key=? AND rel.relation='evidences'
		ORDER BY fact.key`, projectID, evidenceKey)
	if err != nil {
		return fmt.Errorf("read Evidence-dependent Facts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return fmt.Errorf("scan Evidence-dependent Fact: %w", err)
		}
		var fact FactRecord
		if err := decodeJSON([]byte(raw), &fact); err != nil {
			return fmt.Errorf("decode Evidence-dependent Fact: %w", err)
		}
		if fact.Confidence == "confirmed" {
			dependent[key] = path
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate Evidence-dependent Facts: %w", err)
	}
	return nil
}

func collectSupportingFactDependentConfirmedFacts(ctx context.Context, tx *sql.Tx, projectID, supportKey, path string, dependent map[string]string) error {
	record, err := loadCurrentRecord(ctx, tx, projectID, supportKey)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && record.typ != "fact") {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT target.key, target.record_json
		FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS target
		  ON target.project_id=rel.project_id AND target.key=rel.to_key AND target.type='fact'
		WHERE rel.project_id=? AND rel.from_key=? AND rel.relation='supports'
		ORDER BY target.key`, projectID, supportKey)
	if err != nil {
		return fmt.Errorf("read supporting-Fact-dependent Facts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return fmt.Errorf("scan supporting-Fact-dependent Fact: %w", err)
		}
		var fact FactRecord
		if err := decodeJSON([]byte(raw), &fact); err != nil {
			return fmt.Errorf("decode supporting-Fact-dependent Fact: %w", err)
		}
		if fact.Confidence == "confirmed" {
			dependent[key] = path
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate supporting-Fact-dependent Facts: %w", err)
	}
	return nil
}

func (s *Service) validateDependentConfirmedFactBases(ctx context.Context, tx *sql.Tx, projectID string, dependent map[string]string) error {
	keys := make([]string, 0, len(dependent))
	for key := range dependent {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fact, err := loadCurrentRecord(ctx, tx, projectID, key)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if fact.typ != "fact" || fact.record.factRecord().Confidence != "confirmed" {
			continue
		}
		valid, err := s.factHasDurableBasis(ctx, tx, projectID, key)
		if err != nil {
			return err
		}
		if !valid {
			return semanticError("semantic_validation", "relationship or lifecycle change would leave a confirmed Fact without a valid basis", dependent[key], map[string]any{"fact": key})
		}
	}
	return nil
}

func (s *Service) factHasDurableBasis(ctx context.Context, tx *sql.Tx, projectID, factKey string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT evidence.record_json
		FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS evidence
		  ON evidence.project_id=rel.project_id AND evidence.key=rel.from_key AND evidence.type='evidence'
		WHERE rel.project_id=? AND rel.relation='evidences' AND rel.to_key=?
		ORDER BY evidence.key`, projectID, factKey)
	if err != nil {
		return false, fmt.Errorf("read durable Evidence bases: %w", err)
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return false, fmt.Errorf("scan durable Evidence basis: %w", err)
		}
		var evidence EvidenceRecord
		if err := decodeJSON([]byte(raw), &evidence); err != nil {
			rows.Close()
			return false, fmt.Errorf("decode durable Evidence basis: %w", err)
		}
		valid, err := s.evidenceIntegrityValid(projectID, evidence)
		if err != nil {
			rows.Close()
			return false, err
		}
		if valid {
			rows.Close()
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, fmt.Errorf("iterate durable Evidence bases: %w", err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close durable Evidence bases: %w", err)
	}
	var confirmedSupport int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS support
		  ON support.project_id=rel.project_id AND support.key=rel.from_key AND support.type='fact'
		WHERE rel.project_id=? AND rel.relation='supports' AND rel.to_key=?
		  AND json_extract(support.record_json,'$.confidence')='confirmed'`, projectID, factKey).Scan(&confirmedSupport); err != nil {
		return false, fmt.Errorf("read durable confirmed Fact bases: %w", err)
	}
	if confirmedSupport != 0 {
		return true, nil
	}
	rows, err = tx.QueryContext(ctx, `
		SELECT attempt.record_json
		FROM blackboard_v2_relationship_history AS rel
		JOIN blackboard_v2_record_history AS attempt
		  ON attempt.project_id=rel.project_id AND attempt.key=rel.from_key AND attempt.type='attempt'
		WHERE rel.project_id=? AND rel.relation='produced' AND rel.to_key=?
		ORDER BY attempt.key, attempt.version`, projectID, factKey)
	if err != nil {
		return false, fmt.Errorf("read durable producing Attempt bases: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, fmt.Errorf("scan durable producing Attempt basis: %w", err)
		}
		var attempt AttemptRecord
		if err := decodeJSON([]byte(raw), &attempt); err != nil {
			return false, fmt.Errorf("decode durable producing Attempt basis: %w", err)
		}
		if attempt.Status == "succeeded" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate durable producing Attempt bases: %w", err)
	}
	return false, nil
}

func (s *Service) verifyJournaledEvidenceTemp(tempPath, digest string, size int64) (bool, error) {
	root, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
	if err != nil {
		return false, fmt.Errorf("open managed Artifact Root: %w", err)
	}
	defer root.Close()
	directory, file, _, _, err := openSecureRegularFile(root, tempPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, semanticError("evidence_integrity_failed", "journaled Evidence temp path is symlinked or unsafe", "source_path", nil)
	}
	defer directory.Close()
	defer file.Close()
	hash := sha256.New()
	actualSize, err := io.Copy(hash, file)
	if err != nil {
		return false, fmt.Errorf("hash journaled Evidence temp: %w", err)
	}
	return actualSize == size && hex.EncodeToString(hash.Sum(nil)) == digest, nil
}

func (s *Service) evidencePublicationRecoveryExists(ctx context.Context, projectID, continuationID, key string, row evidenceRequestRow) (bool, bool, error) {
	root, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
	if err != nil {
		return false, false, fmt.Errorf("open managed Artifact Root: %w", err)
	}
	defer root.Close()
	stagingDirectory, err := s.openSecureEvidenceDirectory(root, filepath.Dir(row.tempPath))
	if err != nil {
		return false, false, fmt.Errorf("open private Evidence staging directory: %w", err)
	}
	defer stagingDirectory.Close()
	deterministicExists := false
	if _, err := stagingDirectory.Lstat(filepath.Base(row.tempPath)); err == nil {
		deterministicExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, false, fmt.Errorf("inspect journaled Evidence temp: %w", err)
	}
	if migration27Temp, err := migration27EvidenceTempPath(row.internalPath, continuationID, key, row.requestHash); err == nil && row.migration27Temp == migration27Temp && filesystemSafeEvidencePath(migration27Temp) {
		valid, err := s.verifyJournaledEvidenceTemp(migration27Temp, row.sha256, row.size)
		if err != nil {
			return false, false, err
		}
		if valid {
			return true, true, nil
		}
	}
	if row.previousTemp != "" && row.previousTemp != row.tempPath {
		claimed, err := s.evidenceManagedPathClaimed(ctx, projectID, row.previousTemp)
		if err != nil {
			return false, false, err
		}
		if !claimed {
			valid, err := s.verifyJournaledEvidenceTemp(row.previousTemp, row.sha256, row.size)
			if err != nil {
				return false, false, err
			}
			if valid {
				return true, true, nil
			}
		}
	}
	finalDirectory, err := s.openSecureEvidenceDirectory(root, filepath.Dir(row.internalPath))
	if err != nil {
		return false, false, fmt.Errorf("open managed Evidence recovery directory: %w", err)
	}
	defer finalDirectory.Close()
	opened, err := finalDirectory.Open(".")
	if err != nil {
		return false, false, fmt.Errorf("open managed Evidence recovery directory listing: %w", err)
	}
	entries, readErr := opened.ReadDir(-1)
	closeErr := opened.Close()
	if readErr != nil {
		return false, false, fmt.Errorf("read managed Evidence recovery directory: %w", readErr)
	}
	if closeErr != nil {
		return false, false, fmt.Errorf("close managed Evidence recovery directory: %w", closeErr)
	}
	for _, entry := range entries {
		if !isHistoricalEvidenceTempName(entry.Name()) {
			continue
		}
		candidatePath := filepath.Join(filepath.Dir(row.internalPath), entry.Name())
		claimed, err := s.evidenceManagedPathClaimed(ctx, projectID, candidatePath)
		if err != nil {
			return false, false, err
		}
		if claimed {
			continue
		}
		valid, err := legacyEvidenceCandidateValid(finalDirectory, entry.Name(), row.sha256, row.size)
		if err != nil {
			return false, false, err
		}
		if valid {
			return true, true, nil
		}
	}
	return deterministicExists, false, nil
}

func (s *Service) evidenceManagedPathClaimed(ctx context.Context, projectID, internalPath string) (bool, error) {
	return evidenceManagedPathClaimedBy(ctx, s.db, projectID, internalPath)
}

type evidenceClaimQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func evidenceManagedPathClaimedBy(ctx context.Context, querier evidenceClaimQuerier, projectID, internalPath string) (bool, error) {
	var references int
	if err := querier.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=?) +
			(SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND managed_internal_path=?)`,
		projectID, internalPath, projectID, internalPath,
	).Scan(&references); err != nil {
		return false, fmt.Errorf("check managed Evidence path claim: %w", err)
	}
	return references != 0, nil
}

func isHistoricalEvidenceTempName(name string) bool {
	const prefix = ".retain-"
	if len(name) != len(prefix)+24 || !strings.HasPrefix(name, prefix) {
		return false
	}
	for _, character := range name[len(prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func legacyEvidenceCandidateValid(root *os.Root, name, digest string, size int64) (bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, nil
	}
	file, err := root.Open(name)
	if err != nil {
		return false, nil
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false, nil
	}
	valid, err := verifyOpenEvidenceFile(file, digest, size)
	if err != nil {
		return false, nil
	}
	return valid, nil
}

func (s *Service) ensureEvidencePublished(ctx context.Context, projectID, continuationID, key string, row *evidenceRequestRow, source *evidenceSource) error {
	if source != nil && !evidenceSourceStillSame(*source) {
		return semanticError("evidence_source_changed", "Evidence source was replaced during retention", "source_path", nil)
	}
	if err := s.failEvidence(EvidenceFailureBeforeFilePublish); err != nil {
		return err
	}
	managedRoot, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
	if err != nil {
		return fmt.Errorf("open managed Artifact Root: %w", err)
	}
	defer managedRoot.Close()
	finalDirectoryPath := filepath.Dir(row.internalPath)
	destinationRoot, err := s.openSecureEvidenceDirectory(managedRoot, finalDirectoryPath)
	if err != nil {
		return fmt.Errorf("open managed Evidence directory: %w", err)
	}
	defer destinationRoot.Close()
	stagingDirectoryPath := filepath.Dir(row.tempPath)
	stagingRoot, err := s.openSecureEvidenceDirectory(managedRoot, stagingDirectoryPath)
	if err != nil {
		return fmt.Errorf("open private Evidence staging directory: %w", err)
	}
	defer stagingRoot.Close()
	name := filepath.Base(row.internalPath)
	tempName := filepath.Base(row.tempPath)
	if _, err := destinationRoot.Lstat(name); err == nil {
		existing, err := openLockedEvidenceFile(destinationRoot, name, "managed Evidence payload")
		if err != nil {
			return err
		}
		defer unlockAndCloseEvidencePublisher(existing)
		ready, verifyErr := verifyOpenEvidenceFile(existing, row.sha256, row.size)
		if verifyErr != nil {
			return verifyErr
		}
		if !ready {
			return semanticError("evidence_integrity_failed", "managed Evidence payload failed integrity verification", "key", nil)
		}
		if _, err := s.sweepLegacyEvidenceTemps(ctx, projectID, *row, destinationRoot, nil); err != nil {
			return err
		}
		if _, err := s.recoverMigration27EvidenceTemp(managedRoot, continuationID, key, *row, nil); err != nil {
			return err
		}
		if _, err := s.recoverPreviousEvidenceTemp(ctx, projectID, *row, destinationRoot, nil); err != nil {
			return err
		}
		if err := removeInactiveJournaledEvidenceTemp(stagingRoot, tempName); err != nil {
			return err
		}
		return s.syncAndCheckpointPublishedEvidence(ctx, projectID, continuationID, key, row.internalPath, row.tempPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect managed Evidence destination: %w", err)
	}
	publisher, err := s.acquireEvidencePublisher(ctx, stagingRoot, projectID, continuationID, key, row)
	if err != nil {
		return err
	}
	defer publisher.close()
	if err := syncEvidenceDirectory(stagingRoot); err != nil {
		return err
	}
	tempReady, err := verifyOpenEvidenceFile(publisher.file, row.sha256, row.size)
	if err != nil {
		return fmt.Errorf("verify locked Evidence temp: %w", err)
	}
	migration27Adopted, err := s.recoverMigration27EvidenceTemp(managedRoot, continuationID, key, *row, func() (*os.File, error) {
		if tempReady {
			return nil, nil
		}
		return publisher.writer(stagingRoot, tempName)
	})
	if err != nil {
		return err
	}
	tempReady = tempReady || migration27Adopted
	previousAdopted, err := s.recoverPreviousEvidenceTemp(ctx, projectID, *row, destinationRoot, func() (*os.File, error) {
		if tempReady {
			return nil, nil
		}
		return publisher.writer(stagingRoot, tempName)
	})
	if err != nil {
		return err
	}
	tempReady = tempReady || previousAdopted
	var legacyDestination func() (*os.File, error)
	if !tempReady {
		legacyDestination = func() (*os.File, error) { return publisher.writer(stagingRoot, tempName) }
	}
	legacyAdopted, err := s.sweepLegacyEvidenceTemps(ctx, projectID, *row, destinationRoot, legacyDestination)
	if err != nil {
		return err
	}
	tempReady = tempReady || legacyAdopted
	if !tempReady {
		if source == nil {
			return semanticError("evidence_source_changed", "Evidence source is required to rebuild an incomplete journaled temp", "source_path", nil)
		}
		temp, err := publisher.writer(stagingRoot, tempName)
		if err != nil {
			return err
		}
		if err := temp.Truncate(0); err != nil {
			return fmt.Errorf("truncate journaled Evidence temp: %w", err)
		}
		if _, err := temp.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind journaled Evidence temp: %w", err)
		}
		if err := s.failEvidence(EvidenceFailureBeforeTempCopy); err != nil {
			return err
		}
		copiedHash := sha256.New()
		copiedSize, copyErr := copyEvidenceWithMidpointFailure(temp, copiedHash, source.file, func() error {
			return s.failEvidence(EvidenceFailureMidTempCopy)
		})
		if copyErr == nil && (copiedSize != source.size || hex.EncodeToString(copiedHash.Sum(nil)) != source.sha256) {
			return semanticError("evidence_source_changed", "Evidence source bytes changed during retention", "source_path", nil)
		}
		if copyErr != nil {
			return fmt.Errorf("write journaled Evidence temp: %w", copyErr)
		}
		if !evidenceSourceStillSame(*source) {
			return semanticError("evidence_source_changed", "Evidence source was replaced during retention", "source_path", nil)
		}
	}
	if err := publisher.file.Chmod(0o400); err != nil {
		return fmt.Errorf("make journaled Evidence temp immutable: %w", err)
	}
	if err := publisher.file.Sync(); err != nil {
		return fmt.Errorf("sync journaled Evidence temp: %w", err)
	}
	if err := syncEvidenceDirectory(stagingRoot); err != nil {
		return err
	}
	if err := s.failEvidence(EvidenceFailureAfterTempDirectorySync); err != nil {
		return err
	}
	if err := s.failEvidence(EvidenceFailureAfterTempSync); err != nil {
		return err
	}
	ready, err := verifyOpenEvidenceFile(publisher.file, row.sha256, row.size)
	if err != nil {
		return fmt.Errorf("rehash synced Evidence temp: %w", err)
	}
	if !ready {
		return semanticError("evidence_integrity_failed", "synced Evidence temp failed integrity verification", "source_path", nil)
	}
	if err := publisher.validate(ctx, s.db, stagingRoot, projectID, continuationID, key, *row); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET payload_owned=1,updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND managed_internal_path=? AND publisher_token=? AND publisher_temp_identity=?`, time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, key, row.internalPath, publisher.token, publisher.identity)
	if err != nil {
		return fmt.Errorf("claim managed Evidence payload: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return &Error{Code: "evidence_publication_in_progress", Message: "Evidence publisher claim changed before publication", Path: "idempotency_key", Retryable: true}
	}
	row.payloadOwned = true
	if err := s.failEvidence(EvidenceFailureBeforeFileRename); err != nil {
		return err
	}
	if err := managedRoot.Rename(row.tempPath, row.internalPath); err != nil {
		return fmt.Errorf("publish managed Evidence: %w", err)
	}
	lockedInfo, err := publisher.file.Stat()
	if err != nil {
		return fmt.Errorf("stat published Evidence inode: %w", err)
	}
	finalInfo, err := destinationRoot.Lstat(name)
	if err != nil || !os.SameFile(lockedInfo, finalInfo) {
		return semanticError("evidence_integrity_failed", "published Evidence path is not the synced publisher inode", "source_path", nil)
	}
	if err := s.failEvidence(EvidenceFailureAfterFileRename); err != nil {
		return err
	}
	if err := syncEvidenceDirectory(destinationRoot); err != nil {
		return err
	}
	if err := syncEvidenceDirectory(stagingRoot); err != nil {
		return err
	}
	if err := s.failEvidence(EvidenceFailureBeforePublishStore); err != nil {
		return err
	}
	ready, err = s.verifyManagedEvidencePayload(row.internalPath, row.sha256, row.size)
	if err != nil {
		return err
	}
	if !ready {
		return semanticError("evidence_integrity_failed", "published Evidence payload failed integrity verification", "key", nil)
	}
	result, err = s.db.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET status='published',publisher_token='',publisher_temp_identity='',updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND publisher_token=? AND publisher_temp_identity=?`, time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, key, publisher.token, publisher.identity)
	if err != nil {
		return fmt.Errorf("checkpoint Evidence publication: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return &Error{Code: "evidence_publication_in_progress", Message: "Evidence publisher claim changed before checkpoint", Path: "idempotency_key", Retryable: true}
	}
	row.status = "published"
	return s.failEvidence(EvidenceFailureAfterFilePublish)
}

type evidencePublisher struct {
	file     *os.File
	token    string
	identity string
	writerFD *os.File
}

func (s *Service) acquireEvidencePublisher(ctx context.Context, root *os.Root, projectID, continuationID, key string, row *evidenceRequestRow) (*evidencePublisher, error) {
	tempName := filepath.Base(row.tempPath)
	if info, err := root.Lstat(tempName); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, semanticError("evidence_integrity_failed", "journaled Evidence temp path is symlinked", "source_path", nil)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect journaled Evidence temp: %w", err)
	}
	file, err := root.OpenFile(tempName, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open journaled Evidence temp publisher: %w", err)
	}
	if err := lockEvidencePublisherFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		unlockAndCloseEvidencePublisher(file)
		return nil, semanticError("evidence_integrity_failed", "journaled Evidence temp is not a regular file", "source_path", nil)
	}
	pathInfo, err := root.Lstat(tempName)
	if err != nil || !os.SameFile(info, pathInfo) {
		unlockAndCloseEvidencePublisher(file)
		return nil, semanticError("evidence_integrity_failed", "journaled Evidence temp changed while claiming publisher", "source_path", nil)
	}
	token, err := newEvidencePublisherToken()
	if err != nil {
		unlockAndCloseEvidencePublisher(file)
		return nil, err
	}
	identity := fileIdentity(row.tempPath, info)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		unlockAndCloseEvidencePublisher(file)
		return nil, fmt.Errorf("begin Evidence publisher claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var payloadState string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=?`, projectID, row.internalPath).Scan(&payloadState); err != nil {
		unlockAndCloseEvidencePublisher(file)
		return nil, fmt.Errorf("validate Evidence publisher payload claim: %w", err)
	}
	if payloadState != "active" {
		unlockAndCloseEvidencePublisher(file)
		return nil, &Error{Code: "evidence_payload_gc_in_progress", Message: "Evidence payload cleanup is in progress", Path: "source_path", Retryable: true}
	}
	result, err := tx.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET publisher_token=?,publisher_temp_identity=?,updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=? AND temp_internal_path=? AND status='reserved'`, token, identity, time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, key, row.requestHash, row.tempPath)
	if err != nil {
		unlockAndCloseEvidencePublisher(file)
		return nil, fmt.Errorf("claim Evidence publisher: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		unlockAndCloseEvidencePublisher(file)
		return nil, &Error{Code: "evidence_publication_in_progress", Message: "Evidence publisher reservation changed", Path: "idempotency_key", Retryable: true}
	}
	if err := tx.Commit(); err != nil {
		unlockAndCloseEvidencePublisher(file)
		return nil, fmt.Errorf("commit Evidence publisher claim: %w", err)
	}
	row.publisherToken = token
	row.publisherID = identity
	return &evidencePublisher{file: file, token: token, identity: identity}, nil
}

func (publisher *evidencePublisher) writer(root *os.Root, name string) (*os.File, error) {
	if publisher.writerFD != nil {
		return publisher.writerFD, nil
	}
	if err := publisher.file.Chmod(0o600); err != nil {
		return nil, fmt.Errorf("make journaled Evidence temp writable: %w", err)
	}
	writer, err := root.OpenFile(name, os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open journaled Evidence temp writer: %w", err)
	}
	lockedInfo, lockErr := publisher.file.Stat()
	writerInfo, writerErr := writer.Stat()
	if lockErr != nil || writerErr != nil || !os.SameFile(lockedInfo, writerInfo) {
		_ = writer.Close()
		return nil, semanticError("evidence_integrity_failed", "journaled Evidence temp inode changed before write", "source_path", nil)
	}
	publisher.writerFD = writer
	return writer, nil
}

func (publisher *evidencePublisher) validate(ctx context.Context, db *store.DB, root *os.Root, projectID, continuationID, key string, row evidenceRequestRow) error {
	lockedInfo, err := publisher.file.Stat()
	if err != nil {
		return fmt.Errorf("stat locked Evidence publisher: %w", err)
	}
	pathInfo, err := root.Lstat(filepath.Base(row.tempPath))
	if err != nil || !os.SameFile(lockedInfo, pathInfo) {
		return semanticError("evidence_integrity_failed", "journaled Evidence temp inode changed before publication", "source_path", nil)
	}
	var token, identity string
	if err := db.QueryRowContext(ctx, `SELECT publisher_token,publisher_temp_identity FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=?`, projectID, continuationID, key, row.requestHash).Scan(&token, &identity); err != nil {
		return fmt.Errorf("validate Evidence publisher claim: %w", err)
	}
	if token != publisher.token || identity != publisher.identity || fileIdentity(row.tempPath, lockedInfo) != publisher.identity {
		return &Error{Code: "evidence_publication_in_progress", Message: "Evidence publisher claim changed", Path: "idempotency_key", Retryable: true}
	}
	return nil
}

func (publisher *evidencePublisher) close() {
	if publisher.writerFD != nil {
		_ = publisher.writerFD.Close()
	}
	unlockAndCloseEvidencePublisher(publisher.file)
}

func openLockedEvidenceFile(root *os.Root, name, description string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, semanticError("evidence_integrity_failed", description+" is symlinked or not a regular file", "source_path", nil)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", description, err)
	}
	if err := lockEvidencePublisherFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		unlockAndCloseEvidencePublisher(file)
		return nil, semanticError("evidence_integrity_failed", description+" changed while acquiring its inode lock", "source_path", nil)
	}
	return file, nil
}

func removeInactiveJournaledEvidenceTemp(root *os.Root, name string) error {
	if _, err := root.Lstat(name); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect stale journaled Evidence temp: %w", err)
	}
	file, err := openLockedEvidenceFile(root, name, "journaled Evidence temp")
	if err != nil {
		return err
	}
	defer unlockAndCloseEvidencePublisher(file)
	if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale journaled Evidence temp: %w", err)
	}
	return syncEvidenceDirectory(root)
}

func verifyOpenEvidenceFile(file *os.File, digest string, size int64) (bool, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	hash := sha256.New()
	actualSize, err := io.Copy(hash, file)
	if err != nil {
		return false, err
	}
	_, seekErr := file.Seek(0, io.SeekStart)
	return actualSize == size && hex.EncodeToString(hash.Sum(nil)) == digest, seekErr
}

func newEvidencePublisherToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create Evidence publisher token: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func (s *Service) sweepLegacyEvidenceTemps(ctx context.Context, projectID string, row evidenceRequestRow, root *os.Root, destination func() (*os.File, error)) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin legacy Evidence temp sweep: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var unresolved int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND source_sha256=? AND source_size_bytes=? AND status<>'completed'`, projectID, row.sha256, row.size).Scan(&unresolved); err != nil {
		return false, fmt.Errorf("count unresolved shared Evidence reservations: %w", err)
	}
	preserveValid := unresolved - 1
	if preserveValid < 0 {
		preserveValid = 0
	}
	adopted, err := sweepEvidenceRecoveryTemps(root, destination, row.sha256, row.size, preserveValid, func(name string) (bool, error) {
		if !isHistoricalEvidenceTempName(name) {
			return false, nil
		}
		candidatePath := filepath.Join(filepath.Dir(row.internalPath), name)
		claimed, err := evidenceManagedPathClaimedBy(ctx, tx, projectID, candidatePath)
		return !claimed, err
	})
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit legacy Evidence temp sweep: %w", err)
	}
	return adopted, nil
}

func (s *Service) recoverPreviousEvidenceTemp(ctx context.Context, projectID string, row evidenceRequestRow, root *os.Root, destination func() (*os.File, error)) (bool, error) {
	if row.previousTemp == "" || row.previousTemp == row.tempPath || filepath.Dir(row.previousTemp) != filepath.Dir(row.internalPath) {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin previous Evidence temp recovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	claimed, err := evidenceManagedPathClaimedBy(ctx, tx, projectID, row.previousTemp)
	if err != nil || claimed {
		if err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit claimed previous Evidence temp check: %w", err)
		}
		return false, nil
	}
	previousName := filepath.Base(row.previousTemp)
	adopted, err := sweepEvidenceRecoveryTemps(root, destination, row.sha256, row.size, 0, func(name string) (bool, error) {
		return name == previousName, nil
	})
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit previous Evidence temp recovery: %w", err)
	}
	return adopted, nil
}

func (s *Service) recoverMigration27EvidenceTemp(root *os.Root, continuationID, key string, row evidenceRequestRow, destination func() (*os.File, error)) (bool, error) {
	expected, err := migration27EvidenceTempPath(row.internalPath, continuationID, key, row.requestHash)
	if err != nil || row.migration27Temp == "" || row.migration27Temp == row.tempPath || row.migration27Temp != expected || !filesystemSafeEvidencePath(row.migration27Temp) {
		return false, nil
	}
	directory, err := openExistingSecureDirectory(root, filepath.Dir(row.migration27Temp))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, semanticError("evidence_integrity_failed", "migration 27 Evidence temp path is symlinked or unsafe", "source_path", nil)
	}
	defer directory.Close()
	name := filepath.Base(row.migration27Temp)
	if _, err := directory.Lstat(name); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("inspect migration 27 Evidence temp: %w", err)
	}
	candidate, err := openLockedEvidenceFile(directory, name, "migration 27 Evidence temp")
	if err != nil {
		return false, err
	}
	defer unlockAndCloseEvidencePublisher(candidate)
	valid, err := verifyOpenEvidenceFile(candidate, row.sha256, row.size)
	if err != nil {
		return false, fmt.Errorf("verify migration 27 Evidence temp: %w", err)
	}
	adopted := false
	if valid && destination != nil {
		target, err := destination()
		if err != nil {
			return false, err
		}
		if target != nil {
			if err := target.Truncate(0); err == nil {
				_, err = target.Seek(0, io.SeekStart)
			}
			if err == nil {
				_, err = candidate.Seek(0, io.SeekStart)
			}
			if err == nil {
				_, err = io.Copy(target, candidate)
			}
			if err == nil {
				err = target.Sync()
			}
			if err == nil {
				var ready bool
				ready, err = verifyOpenEvidenceFile(target, row.sha256, row.size)
				if err == nil && !ready {
					err = errors.New("adopted migration 27 Evidence temp failed integrity verification")
				}
			}
			if err != nil {
				return false, fmt.Errorf("adopt migration 27 Evidence temp: %w", err)
			}
			adopted = true
		}
	}
	if err := directory.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("remove migration 27 Evidence temp: %w", err)
	}
	if err := syncEvidenceDirectory(directory); err != nil {
		return false, err
	}
	return adopted, nil
}

func sweepEvidenceRecoveryTemps(root *os.Root, destination func() (*os.File, error), digest string, size int64, preserveValid int, eligible func(string) (bool, error)) (bool, error) {
	directory, err := root.Open(".")
	if err != nil {
		return false, fmt.Errorf("open managed Evidence directory for legacy sweep: %w", err)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return false, fmt.Errorf("read managed Evidence directory for legacy sweep: %w", readErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close managed Evidence legacy sweep directory: %w", closeErr)
	}
	adopted := false
	removed := false
	for _, entry := range entries {
		name := entry.Name()
		allowed, err := eligible(name)
		if err != nil {
			return false, err
		}
		if !allowed {
			continue
		}
		valid := false
		info, statErr := root.Lstat(name)
		if statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			candidate, openErr := root.Open(name)
			if openErr != nil {
				return false, fmt.Errorf("open legacy Evidence temp: %w", openErr)
			}
			valid, openErr = verifyOpenEvidenceFile(candidate, digest, size)
			if openErr == nil && valid && !adopted && destination != nil {
				target, targetErr := destination()
				if targetErr != nil {
					_ = candidate.Close()
					return false, targetErr
				}
				if target != nil {
					if err := target.Truncate(0); err == nil {
						_, err = target.Seek(0, io.SeekStart)
					}
					if err == nil {
						_, err = candidate.Seek(0, io.SeekStart)
					}
					if err == nil {
						_, err = io.Copy(target, candidate)
					}
					if err == nil {
						err = target.Sync()
					}
					if err == nil {
						var ready bool
						ready, err = verifyOpenEvidenceFile(target, digest, size)
						if err == nil && !ready {
							err = errors.New("adopted Evidence temp failed integrity verification")
						}
					}
					if err != nil {
						_ = candidate.Close()
						return false, fmt.Errorf("adopt legacy Evidence temp: %w", err)
					}
					adopted = true
				}
			}
			if openErr != nil {
				_ = candidate.Close()
				return false, fmt.Errorf("verify legacy Evidence temp: %w", openErr)
			}
			if err := candidate.Close(); err != nil {
				return false, fmt.Errorf("close legacy Evidence temp: %w", err)
			}
		}
		if valid && preserveValid > 0 {
			preserveValid--
			continue
		}
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("remove legacy Evidence temp: %w", err)
		} else if err == nil {
			removed = true
		}
	}
	if removed {
		if err := syncEvidenceDirectory(root); err != nil {
			return false, err
		}
	}
	return adopted, nil
}

func copyEvidenceWithMidpointFailure(destination io.Writer, hash io.Writer, source io.Reader, fail func() error) (int64, error) {
	buffer := make([]byte, 32*1024)
	var copied int64
	failureChecked := false
	for {
		count, readErr := source.Read(buffer)
		if count != 0 {
			written, writeErr := io.MultiWriter(destination, hash).Write(buffer[:count])
			copied += int64(written)
			if writeErr != nil {
				return copied, writeErr
			}
			if !failureChecked {
				failureChecked = true
				if err := fail(); err != nil {
					return copied, err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return copied, nil
		}
		if readErr != nil {
			return copied, readErr
		}
	}
}

func removeJournaledEvidenceTemp(root *os.Root, name string) error {
	err := root.Remove(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove journaled Evidence temp: %w", err)
	}
	if err == nil {
		return syncEvidenceDirectory(root)
	}
	return nil
}

func (s *Service) syncAndCheckpointPublishedEvidence(ctx context.Context, projectID, continuationID, key, internalPath, tempPath string) error {
	root, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
	if err != nil {
		return fmt.Errorf("open managed Artifact Root: %w", err)
	}
	defer root.Close()
	directory, err := s.openSecureEvidenceDirectory(root, filepath.Dir(internalPath))
	if err != nil {
		return fmt.Errorf("open managed Evidence directory: %w", err)
	}
	defer directory.Close()
	stagingDirectory, err := s.openSecureEvidenceDirectory(root, filepath.Dir(tempPath))
	if err != nil {
		return fmt.Errorf("open private Evidence staging directory: %w", err)
	}
	defer stagingDirectory.Close()
	if err := syncEvidenceDirectory(directory); err != nil {
		return err
	}
	if err := syncEvidenceDirectory(stagingDirectory); err != nil {
		return err
	}
	if err := s.failEvidence(EvidenceFailureBeforePublishStore); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET status='published',publisher_token='',publisher_temp_identity='',updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, key); err != nil {
		return fmt.Errorf("checkpoint recovered Evidence publication: %w", err)
	}
	return s.failEvidence(EvidenceFailureAfterFilePublish)
}

func syncEvidenceDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open managed Evidence directory for sync: %w", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return fmt.Errorf("sync managed Evidence directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close managed Evidence directory: %w", err)
	}
	return nil
}

func (s *Service) cleanupDefinitiveEvidenceFailure(ctx context.Context, projectID, continuationID, key string, row evidenceRequestRow, cause error) {
	var semanticErr *Error
	if !errors.As(cause, &semanticErr) || semanticErr.Retryable || semanticErr.Code == "idempotency_conflict" {
		return
	}
	guard, err := s.acquireEvidenceCleanupGuard(row)
	if err != nil {
		return
	}
	defer guard.close()
	if _, err := s.sweepLegacyEvidenceTemps(ctx, projectID, row, guard.finalDirectory, nil); err != nil {
		return
	}
	if _, err := s.recoverMigration27EvidenceTemp(guard.root, continuationID, key, row, nil); err != nil {
		return
	}
	if _, err := s.recoverPreviousEvidenceTemp(ctx, projectID, row, guard.finalDirectory, nil); err != nil {
		return
	}
	if err := removeJournaledEvidenceTemp(guard.stagingDirectory, filepath.Base(row.tempPath)); err != nil {
		return
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET publisher_token='',publisher_temp_identity='',updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=?`, time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, key, row.requestHash); err != nil {
		return
	}
	decision, err := s.claimEvidencePayloadGC(ctx, projectID, continuationID, key, row)
	if err != nil || decision == evidenceCleanupPreserve {
		return
	}
	if decision == evidenceCleanupRequestRetired {
		return
	}
	if err := s.failEvidence(EvidenceFailureAfterPayloadGCClaim); err != nil {
		return
	}
	if err := s.removeClaimedEvidencePayload(row); err != nil {
		return
	}
	if err := s.failEvidence(EvidenceFailureAfterPayloadUnlink); err != nil {
		return
	}
	_ = s.finalizeEvidencePayloadGC(ctx, projectID, continuationID, key, row)
}

type evidenceCleanupGuard struct {
	root             *os.Root
	finalDirectory   *os.Root
	stagingDirectory *os.Root
	files            []*os.File
}

func (s *Service) acquireEvidenceCleanupGuard(row evidenceRequestRow) (*evidenceCleanupGuard, error) {
	root, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
	if err != nil {
		return nil, fmt.Errorf("open managed Artifact Root for cleanup: %w", err)
	}
	finalDirectory, err := s.openSecureEvidenceDirectory(root, filepath.Dir(row.internalPath))
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("open managed Evidence directory for cleanup: %w", err)
	}
	stagingDirectory, err := s.openSecureEvidenceDirectory(root, filepath.Dir(row.tempPath))
	if err != nil {
		_ = finalDirectory.Close()
		_ = root.Close()
		return nil, fmt.Errorf("open private Evidence staging directory for cleanup: %w", err)
	}
	guard := &evidenceCleanupGuard{root: root, finalDirectory: finalDirectory, stagingDirectory: stagingDirectory}
	for _, candidate := range []struct {
		directory *os.Root
		name      string
	}{
		{directory: stagingDirectory, name: filepath.Base(row.tempPath)},
		{directory: finalDirectory, name: filepath.Base(row.internalPath)},
	} {
		if _, err := candidate.directory.Lstat(candidate.name); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			guard.close()
			return nil, fmt.Errorf("inspect Evidence cleanup inode: %w", err)
		}
		file, err := openLockedEvidenceFile(candidate.directory, candidate.name, "Evidence cleanup payload")
		if err != nil {
			guard.close()
			return nil, err
		}
		guard.files = append(guard.files, file)
	}
	return guard, nil
}

func (guard *evidenceCleanupGuard) close() {
	for index := len(guard.files) - 1; index >= 0; index-- {
		unlockAndCloseEvidencePublisher(guard.files[index])
	}
	if guard.stagingDirectory != nil {
		_ = guard.stagingDirectory.Close()
	}
	if guard.finalDirectory != nil {
		_ = guard.finalDirectory.Close()
	}
	if guard.root != nil {
		_ = guard.root.Close()
	}
}

type evidenceCleanupDecision uint8

const (
	evidenceCleanupPreserve evidenceCleanupDecision = iota
	evidenceCleanupRequestRetired
	evidenceCleanupOwnPayload
)

func (s *Service) claimEvidencePayloadGC(ctx context.Context, projectID, continuationID, key string, row evidenceRequestRow) (evidenceCleanupDecision, error) {
	semanticPath, err := semanticEvidencePath(projectID, row.internalPath, row.sha256)
	if err != nil {
		return evidenceCleanupPreserve, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return evidenceCleanupPreserve, fmt.Errorf("begin Evidence payload cleanup claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var requestCount, receiptCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=? AND managed_internal_path=?`, projectID, continuationID, key, row.requestHash, row.internalPath).Scan(&requestCount); err != nil {
		return evidenceCleanupPreserve, fmt.Errorf("validate Evidence cleanup request: %w", err)
	}
	receiptKey := "retain-evidence-v2:" + continuationID + ":" + key
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_v2_idempotency_receipts WHERE project_id=? AND idempotency_key=?`, projectID, receiptKey).Scan(&receiptCount); err != nil {
		return evidenceCleanupPreserve, fmt.Errorf("validate Evidence cleanup receipt: %w", err)
	}
	if requestCount != 1 || receiptCount != 0 {
		return evidenceCleanupPreserve, nil
	}
	var state, gcContinuationID, gcKey string
	err = tx.QueryRowContext(ctx, `SELECT state,gc_continuation_id,gc_idempotency_key FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=?`, projectID, row.internalPath).Scan(&state, &gcContinuationID, &gcKey)
	if errors.Is(err, sql.ErrNoRows) {
		if row.payloadOwned {
			return evidenceCleanupPreserve, nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=? AND managed_internal_path=?`, projectID, continuationID, key, row.requestHash, row.internalPath); err != nil {
			return evidenceCleanupPreserve, fmt.Errorf("delete unclaimed Evidence request: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return evidenceCleanupPreserve, fmt.Errorf("commit Evidence request cleanup decision: %w", err)
		}
		return evidenceCleanupRequestRetired, nil
	}
	if err != nil {
		return evidenceCleanupPreserve, fmt.Errorf("read Evidence payload cleanup claim: %w", err)
	}
	if state == "gc" {
		if gcContinuationID != continuationID || gcKey != key {
			return evidenceCleanupPreserve, nil
		}
		if err := tx.Commit(); err != nil {
			return evidenceCleanupPreserve, fmt.Errorf("commit resumed Evidence payload cleanup claim: %w", err)
		}
		return evidenceCleanupOwnPayload, nil
	}
	var semanticReferences int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_v2_records WHERE project_id=? AND type='evidence' AND json_extract(record_json,'$.managed_path')=?`, projectID, semanticPath).Scan(&semanticReferences); err != nil {
		return evidenceCleanupPreserve, fmt.Errorf("count semantic Evidence payload references: %w", err)
	}
	if semanticReferences != 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=? AND managed_internal_path=?`, projectID, continuationID, key, row.requestHash, row.internalPath); err != nil {
			return evidenceCleanupPreserve, fmt.Errorf("delete semantically referenced Evidence request: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return evidenceCleanupPreserve, fmt.Errorf("commit semantically referenced Evidence cleanup: %w", err)
		}
		return evidenceCleanupRequestRetired, nil
	}
	var nextContinuationID, nextKey string
	err = tx.QueryRowContext(ctx, `
		SELECT continuation_id,idempotency_key
		FROM blackboard_v2_evidence_requests
		WHERE project_id=? AND managed_internal_path=? AND NOT (continuation_id=? AND idempotency_key=?)
		ORDER BY created_at,continuation_id,idempotency_key
		LIMIT 1`, projectID, row.internalPath, continuationID, key).Scan(&nextContinuationID, &nextKey)
	if err == nil {
		transfer, err := tx.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET payload_owned=1,updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND managed_internal_path=?`, time.Now().UTC().Format(time.RFC3339Nano), projectID, nextContinuationID, nextKey, row.internalPath)
		if err != nil {
			return evidenceCleanupPreserve, fmt.Errorf("transfer shared Evidence payload ownership: %w", err)
		}
		if changed, _ := transfer.RowsAffected(); changed != 1 {
			return evidenceCleanupPreserve, fmt.Errorf("shared Evidence payload ownership target changed")
		}
		deleted, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=? AND managed_internal_path=?`, projectID, continuationID, key, row.requestHash, row.internalPath)
		if err != nil {
			return evidenceCleanupPreserve, fmt.Errorf("delete departing Evidence payload owner: %w", err)
		}
		if changed, _ := deleted.RowsAffected(); changed != 1 {
			return evidenceCleanupPreserve, fmt.Errorf("departing Evidence payload owner changed during transfer")
		}
		if err := tx.Commit(); err != nil {
			return evidenceCleanupPreserve, fmt.Errorf("commit shared Evidence payload ownership transfer: %w", err)
		}
		return evidenceCleanupRequestRetired, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return evidenceCleanupPreserve, fmt.Errorf("select next Evidence payload owner: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE blackboard_v2_evidence_payloads SET state='gc',gc_continuation_id=?,gc_idempotency_key=?,updated_at=? WHERE project_id=? AND managed_internal_path=? AND state='active'`, continuationID, key, now, projectID, row.internalPath)
	if err != nil {
		return evidenceCleanupPreserve, fmt.Errorf("claim Evidence payload cleanup: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return evidenceCleanupPreserve, nil
	}
	if err := tx.Commit(); err != nil {
		return evidenceCleanupPreserve, fmt.Errorf("commit Evidence payload cleanup claim: %w", err)
	}
	return evidenceCleanupOwnPayload, nil
}

func (s *Service) recoverOwnedEvidenceGC(ctx context.Context, projectID, continuationID, key string, row evidenceRequestRow) (bool, error) {
	var state, gcContinuationID, gcKey string
	err := s.db.QueryRowContext(ctx, `SELECT state,gc_continuation_id,gc_idempotency_key FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=?`, projectID, row.internalPath).Scan(&state, &gcContinuationID, &gcKey)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read recoverable Evidence payload cleanup: %w", err)
	}
	if state != "gc" || gcContinuationID != continuationID || gcKey != key {
		return false, nil
	}
	if err := s.removeClaimedEvidencePayload(row); err != nil {
		return false, err
	}
	if err := s.failEvidence(EvidenceFailureAfterPayloadUnlink); err != nil {
		return false, err
	}
	if err := s.finalizeEvidencePayloadGC(ctx, projectID, continuationID, key, row); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) removeClaimedEvidencePayload(row evidenceRequestRow) error {
	root, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
	if err != nil {
		return fmt.Errorf("open managed Artifact Root: %w", err)
	}
	defer root.Close()
	stagingDirectory, err := s.openSecureEvidenceDirectory(root, filepath.Dir(row.tempPath))
	if err != nil {
		return fmt.Errorf("open private Evidence staging directory: %w", err)
	}
	defer stagingDirectory.Close()
	if err := stagingDirectory.Remove(filepath.Base(row.tempPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove claimed Evidence staging temp: %w", err)
	}
	if err := syncEvidenceDirectory(stagingDirectory); err != nil {
		return err
	}
	finalDirectory, err := s.openSecureEvidenceDirectory(root, filepath.Dir(row.internalPath))
	if err != nil {
		return fmt.Errorf("open managed Evidence directory: %w", err)
	}
	defer finalDirectory.Close()
	if err := finalDirectory.Remove(filepath.Base(row.internalPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove claimed Evidence payload: %w", err)
	}
	return syncEvidenceDirectory(finalDirectory)
}

func (s *Service) finalizeEvidencePayloadGC(ctx context.Context, projectID, continuationID, key string, row evidenceRequestRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Evidence payload cleanup completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=? AND managed_internal_path=?`, projectID, continuationID, key, row.requestHash, row.internalPath); err != nil {
		return fmt.Errorf("delete cleaned Evidence request: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=? AND state='gc' AND gc_continuation_id=? AND gc_idempotency_key=?`, projectID, row.internalPath, continuationID, key)
	if err != nil {
		return fmt.Errorf("delete Evidence payload cleanup claim: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return fmt.Errorf("Evidence payload cleanup claim changed during recovery")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Evidence payload cleanup completion: %w", err)
	}
	return nil
}

func evidenceSourceStillSame(source evidenceSource) bool {
	current, err := source.root.Lstat(source.relativePath)
	return err == nil && current.Mode()&os.ModeSymlink == 0 && os.SameFile(source.info, current)
}

func (s *Service) openSecureEvidenceDirectory(root *os.Root, relative string) (*os.Root, error) {
	return openSecureDirectoryDurable(root, relative, s.failEvidence)
}

func openSecureDirectoryDurable(root *os.Root, relative string, checkpoint func(EvidenceFailurePoint) error) (*os.Root, error) {
	clean := filepath.Clean(relative)
	if clean == "." {
		return root.OpenRoot(".")
	}
	if clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, errors.New("managed directory escapes its root")
	}
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		info, err := current.Lstat(component)
		if errors.Is(err, os.ErrNotExist) {
			if err := current.Mkdir(component, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				_ = current.Close()
				return nil, err
			}
			if checkpoint != nil {
				if err := checkpoint(EvidenceFailureAfterDirectoryCreate); err != nil {
					_ = current.Close()
					return nil, err
				}
			}
		}
		if err := syncEvidenceDirectory(current); err != nil {
			_ = current.Close()
			return nil, err
		}
		if checkpoint != nil {
			if err := checkpoint(EvidenceFailureAfterDirectorySync); err != nil {
				_ = current.Close()
				return nil, err
			}
		}
		info, err = current.Lstat(component)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			_ = current.Close()
			return nil, errors.New("managed directory contains a non-directory or symbolic link")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			_ = current.Close()
			return nil, errors.New("managed directory changed while opening")
		}
		_ = current.Close()
		current = next
	}
	return current, nil
}

func (s *Service) applyRetainedEvidence(ctx context.Context, projectID, continuationID string, request RetainEvidenceRequest, requestHash, managedPath string, metadata retainedEvidenceMetadata, durablyReserved bool) (ChangeResult, error) {
	receiptKey := "retain-evidence-v2:" + continuationID + ":" + request.IdempotencyKey
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("begin retained Evidence semantic change: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureProjectState(ctx, tx, projectID); err != nil {
		return ChangeResult{}, err
	}
	if replay, ok, err := idempotencyReplay(ctx, tx, projectID, continuationID, receiptKey, requestHash); err != nil {
		return ChangeResult{}, err
	} else if ok {
		if err := tx.Commit(); err != nil {
			return ChangeResult{}, fmt.Errorf("commit retained Evidence replay: %w", err)
		}
		if err := s.rematerializeContinuationWorkingSnapshot(ctx, continuationID); err != nil {
			return ChangeResult{}, fmt.Errorf("recover retained Evidence Working Snapshot: %w", err)
		}
		return replay, nil
	}
	request, err = resolveRetainedEvidenceRedirectsTx(ctx, tx, projectID, request)
	if err != nil {
		return ChangeResult{}, err
	}
	if durablyReserved {
		internalPath, err := plannedEvidenceInternalPath(projectID, metadata.sha256, filepath.Base(managedPath))
		if err != nil {
			return ChangeResult{}, err
		}
		var payloadDigest, payloadState string
		var payloadSize int64
		err = tx.QueryRowContext(ctx, `SELECT sha256,size_bytes,state FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=?`, projectID, internalPath).Scan(&payloadDigest, &payloadSize, &payloadState)
		if errors.Is(err, sql.ErrNoRows) || payloadState == "gc" {
			return ChangeResult{}, &Error{Code: "evidence_payload_gc_in_progress", Message: "Evidence payload cleanup is in progress", Path: "source_path", Retryable: true}
		}
		if err != nil {
			return ChangeResult{}, fmt.Errorf("validate retained Evidence payload claim: %w", err)
		}
		if payloadDigest != metadata.sha256 || payloadSize != metadata.size {
			return ChangeResult{}, semanticError("evidence_integrity_failed", "retained Evidence payload claim failed integrity validation", "source_path", nil)
		}
		var reservationCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=? AND managed_internal_path=?`, projectID, continuationID, request.IdempotencyKey, requestHash, internalPath).Scan(&reservationCount); err != nil {
			return ChangeResult{}, fmt.Errorf("validate retained Evidence reservation: %w", err)
		}
		if reservationCount != 1 {
			return ChangeResult{}, &Error{Code: "evidence_reservation_changed", Message: "Evidence reservation changed during retention", Path: "idempotency_key", Retryable: true}
		}
	}
	status, err := continuationProjectStatus(ctx, tx, projectID, continuationID)
	if err != nil {
		return ChangeResult{}, err
	}
	if !durablyReserved && !continuationCanWrite(status) {
		return ChangeResult{}, semanticError("closed_continuation", "trusted Continuation is closed for new Blackboard writes", "", nil)
	}
	attempt, err := loadCurrentRecord(ctx, tx, projectID, request.Attempt)
	historicalAttempt := false
	if errors.Is(err, sql.ErrNoRows) && durablyReserved {
		if err := requireAttemptOwner(ctx, tx, projectID, request.Attempt, continuationID, "attempt"); err != nil {
			return ChangeResult{}, err
		}
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT record_json FROM blackboard_v2_record_history WHERE project_id=? AND key=? AND type='attempt' ORDER BY version DESC LIMIT 1`, projectID, request.Attempt).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ChangeResult{}, semanticError("semantic_validation", "durably reserved producing Attempt has no terminal history", "attempt", nil)
			}
			return ChangeResult{}, fmt.Errorf("read reserved producing Attempt history: %w", err)
		}
		var terminal AttemptRecord
		if err := decodeJSON([]byte(raw), &terminal); err != nil {
			return ChangeResult{}, fmt.Errorf("decode reserved producing Attempt history: %w", err)
		}
		if !isOneOf(terminal.Status, "succeeded", "failed", "blocked", "inconclusive", "interrupted") {
			return ChangeResult{}, semanticError("semantic_validation", "durably reserved producing Attempt is not terminal", "attempt", nil)
		}
		historicalAttempt = true
	} else {
		if errors.Is(err, sql.ErrNoRows) {
			return ChangeResult{}, semanticError("not_found", fmt.Sprintf("%s was not found", request.Attempt), "attempt", map[string]any{"key": request.Attempt})
		}
		if err != nil {
			return ChangeResult{}, err
		}
		if attempt.typ != "attempt" {
			return ChangeResult{}, semanticError("semantic_validation", "attempt must reference an Attempt", "attempt", nil)
		}
		if err := requireAttemptOwner(ctx, tx, projectID, request.Attempt, continuationID, "attempt"); err != nil {
			return ChangeResult{}, err
		}
		if attempt.record.attemptRecord().Status != "open" {
			return ChangeResult{}, semanticError("semantic_validation", "producing Attempt must be open for a new retain", "attempt", nil)
		}
	}
	record := EvidenceRecord{Status: "available", ArtifactType: request.ArtifactType, Summary: request.Summary, MediaType: request.MediaType, SourcePath: request.SourcePath, ManagedPath: managedPath, SHA256: metadata.sha256, Size: metadata.size, CapturedAt: request.CapturedAt}
	if err := validateEvidenceRecord(record, "record"); err != nil {
		return ChangeResult{}, err
	}
	revision, err := currentRevision(ctx, tx, projectID)
	if err != nil {
		return ChangeResult{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	changedRecords := make(map[string]int)
	existing, err := loadCurrentRecord(ctx, tx, projectID, request.Key)
	if errors.Is(err, sql.ErrNoRows) {
		if request.Version != 0 {
			return ChangeResult{}, semanticError("not_found", fmt.Sprintf("%s was not found", request.Key), "key", map[string]any{"key": request.Key})
		}
		if used, err := historicalKeyExists(ctx, tx, projectID, request.Key); err != nil {
			return ChangeResult{}, err
		} else if used {
			return ChangeResult{}, semanticError("key_conflict", fmt.Sprintf("%s already exists in Semantic History", request.Key), "key", map[string]any{"key": request.Key})
		}
		raw, err := json.Marshal(record)
		if err != nil {
			return ChangeResult{}, err
		}
		revision, err = incrementRevision(ctx, tx, projectID, revision)
		if err != nil {
			return ChangeResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO blackboard_v2_records(project_id,key,type,version,record_json,created_at,updated_at) VALUES(?,?,'evidence',1,?,?,?)`, projectID, request.Key, string(raw), now, now); err != nil {
			return ChangeResult{}, fmt.Errorf("store Blackboard v2 Evidence: %w", err)
		}
		changedRecords[request.Key] = 1
	} else {
		if err != nil {
			return ChangeResult{}, err
		}
		if existing.typ != "evidence" {
			return ChangeResult{}, semanticError("key_conflict", fmt.Sprintf("%s already exists", request.Key), "key", map[string]any{"key": request.Key})
		}
		if request.Version == 0 {
			return ChangeResult{}, semanticError("semantic_validation", "current Evidence version is required for replacement", "version", nil)
		}
		if request.Version != existing.version {
			return ChangeResult{}, semanticError("version_conflict", fmt.Sprintf("%s changed", request.Key), "version", map[string]any{"key": request.Key, "expected_version": float64(request.Version), "current_version": float64(existing.version), "next_action": "read_current_record"})
		}
		if !evidenceEqual(existing.record.evidenceRecord(), record) {
			nextRevision, _, version, _, err := replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, record, now)
			if err != nil {
				return ChangeResult{}, err
			}
			revision = nextRevision
			changedRecords[request.Key] = version
		}
	}
	changedRelations := make(map[string]RelationVersionTuple)
	if historicalAttempt {
		nextRevision, tuple, changed, err := applyHistoricalProduced(ctx, tx, projectID, revision, request.Attempt, request.Key, now)
		if err != nil {
			return ChangeResult{}, err
		}
		if changed {
			revision = nextRevision
			changedRelations[relationKey(tuple)] = tuple
		}
	} else {
		nextRevision, tuple, changed, err := applyRelate(ctx, tx, projectID, revision, 0, Change{Op: "relate", From: request.Attempt, Relation: "produced", To: request.Key}, now)
		if err != nil {
			return ChangeResult{}, err
		}
		if changed {
			revision = nextRevision
			changedRelations[relationKey(tuple)] = tuple
		}
	}
	for index, link := range request.Links {
		nextRevision, tuple, changed, err := applyRelate(ctx, tx, projectID, revision, index+1, Change{Op: "relate", From: request.Key, Relation: link[0], To: link[1]}, now)
		if err != nil {
			return ChangeResult{}, err
		}
		if changed {
			revision = nextRevision
			changedRelations[relationKey(tuple)] = tuple
		}
	}
	_, _, workingAdvanced, err := s.advanceContinuationWorkingSnapshotTx(ctx, tx, projectID, continuationID)
	if err != nil {
		return ChangeResult{}, err
	}
	result := makeChangeResult(revision, changedRecords, changedRelations)
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return ChangeResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO blackboard_v2_idempotency_receipts(project_id,idempotency_key,request_hash,result_json,created_at,continuation_id) VALUES(?,?,?,?,?,?)`, projectID, receiptKey, requestHash, string(resultJSON), now, continuationID); err != nil {
		return ChangeResult{}, fmt.Errorf("store retained Evidence receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ChangeResult{}, fmt.Errorf("commit retained Evidence semantic change: %w", err)
	}
	if workingAdvanced {
		if err := s.rematerializeContinuationWorkingSnapshot(ctx, continuationID); err != nil {
			return ChangeResult{}, fmt.Errorf("replace retained Evidence Working Snapshot: %w", err)
		}
	}
	return result, nil
}

func applyHistoricalProduced(ctx context.Context, tx *sql.Tx, projectID string, revision int, attemptKey, evidenceKey, now string) (int, RelationVersionTuple, bool, error) {
	var version int
	err := tx.QueryRowContext(ctx, `SELECT version FROM blackboard_v2_relationship_history WHERE project_id=? AND from_key=? AND relation='produced' AND to_key=? ORDER BY version DESC LIMIT 1`, projectID, attemptKey, evidenceKey).Scan(&version)
	if err == nil {
		return revision, RelationVersionTuple{attemptKey, "produced", evidenceKey, version}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, RelationVersionTuple{}, false, fmt.Errorf("read historical Evidence production: %w", err)
	}
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO blackboard_v2_relationship_history(project_id,from_key,relation,to_key,version,reason,recorded_at) VALUES(?,?,'produced',?,1,'',?)`, projectID, attemptKey, evidenceKey, now); err != nil {
		return revision, RelationVersionTuple{}, false, fmt.Errorf("store historical Evidence production: %w", err)
	}
	return nextRevision, RelationVersionTuple{attemptKey, "produced", evidenceKey, 1}, true, nil
}

func (s *Service) failEvidence(point EvidenceFailurePoint) error {
	if s.evidenceConfig.Failures == nil {
		return nil
	}
	return s.evidenceConfig.Failures.FailAfter(point)
}
