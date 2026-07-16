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
	"strings"
	"time"
)

const evidenceRequestKind = "retain_evidence_v2"

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
	EvidenceFailureBeforeFilePublish EvidenceFailurePoint = "before_file_publish"
	EvidenceFailureAfterFilePublish  EvidenceFailurePoint = "file_publish"
	EvidenceFailureAfterGraphCommit  EvidenceFailurePoint = "semantic_commit"
	EvidenceFailureAfterResultStore  EvidenceFailurePoint = "result_store"
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
	if err := json.Unmarshal(raw, &values); err != nil {
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
	if err := json.Unmarshal(raw, &fields); err != nil {
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
			if err := json.Unmarshal(value, destination); err != nil {
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
		if err := json.Unmarshal(value, &result.Version); err != nil {
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
		if err := json.Unmarshal(value, &result.Links); err != nil {
			return fmt.Errorf("decode links: %w", err)
		}
	}
	*request = result
	return nil
}

type evidenceSource struct {
	root         *os.Root
	file         *os.File
	rootPath     string
	relativePath string
	identity     string
	sha256       string
	size         int64
	info         os.FileInfo
}

type evidenceRequestRow struct {
	requestHash    string
	sourceIdentity string
	sha256         string
	size           int64
	status         string
	managedPath    string
	resultJSON     string
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
	scope := "v2-continuation:" + continuationID
	row, exists, err := s.readEvidenceRequest(ctx, projectID, scope, request.IdempotencyKey)
	if err != nil {
		return ChangeResult{}, err
	}
	if exists {
		if row.requestHash != requestHash {
			return ChangeResult{}, semanticError("idempotency_conflict", "idempotency key was already used with different semantics", "idempotency_key", map[string]any{"idempotency_key": request.IdempotencyKey})
		}
		if row.status == "completed" {
			var replay ChangeResult
			if err := json.Unmarshal([]byte(row.resultJSON), &replay); err != nil {
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
			if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_interface_requests SET status='completed',result_json=?,updated_at=? WHERE project_id=? AND idempotency_scope=? AND request_kind=? AND idempotency_key=?`, string(resultJSON), time.Now().UTC().Format(time.RFC3339Nano), projectID, scope, evidenceRequestKind, request.IdempotencyKey); err != nil {
				return ChangeResult{}, fmt.Errorf("complete recovered Evidence request: %w", err)
			}
			return replay, nil
		}
	}
	taskID, status, err := s.continuationTaskStatus(ctx, projectID, continuationID)
	if err != nil {
		return ChangeResult{}, err
	}
	if !continuationCanWrite(status) {
		return ChangeResult{}, semanticError("closed_continuation", "trusted Continuation is closed for new Blackboard writes", "", nil)
	}
	if err := s.validateRetainedEvidencePreconditions(ctx, projectID, continuationID, request); err != nil {
		return ChangeResult{}, err
	}
	if strings.TrimSpace(s.evidenceConfig.RuntimeRoot) == "" || strings.TrimSpace(s.evidenceConfig.ArtifactRoot) == "" {
		return ChangeResult{}, fmt.Errorf("Evidence Runtime Root and Artifact Root must be configured")
	}
	source, err := s.openRuntimeEvidenceSource(taskID, request.SourcePath)
	if err != nil {
		return ChangeResult{}, err
	}
	defer source.file.Close()
	defer source.root.Close()
	if !exists {
		row, exists, err = s.reserveEvidenceRequest(ctx, projectID, scope, request.IdempotencyKey, requestHash, source)
		if err != nil {
			return ChangeResult{}, err
		}
	}
	if exists && (row.sourceIdentity != source.identity || row.sha256 != source.sha256 || row.size != source.size) {
		return ChangeResult{}, semanticError("evidence_source_changed", "Evidence source changed across idempotent retry", "source_path", nil)
	}
	managedPath := row.managedPath
	if managedPath == "" {
		if err := s.failEvidence(EvidenceFailureBeforeFilePublish); err != nil {
			return ChangeResult{}, err
		}
		managedPath, err = s.publishEvidenceSource(source, taskID)
		if err != nil {
			return ChangeResult{}, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_interface_requests SET status='published',managed_path=?,updated_at=? WHERE project_id=? AND idempotency_scope=? AND request_kind=? AND idempotency_key=?`, managedPath, time.Now().UTC().Format(time.RFC3339Nano), projectID, scope, evidenceRequestKind, request.IdempotencyKey); err != nil {
			return ChangeResult{}, fmt.Errorf("checkpoint Evidence publication: %w", err)
		}
		if err := s.failEvidence(EvidenceFailureAfterFilePublish); err != nil {
			return ChangeResult{}, err
		}
	}
	result, err := s.applyRetainedEvidence(ctx, projectID, continuationID, request, managedPath, source)
	if err != nil {
		return ChangeResult{}, err
	}
	if err := s.failEvidence(EvidenceFailureAfterGraphCommit); err != nil {
		return ChangeResult{}, err
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("encode retained Evidence result: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_interface_requests SET status='completed',result_json=?,updated_at=? WHERE project_id=? AND idempotency_scope=? AND request_kind=? AND idempotency_key=?`, string(resultJSON), time.Now().UTC().Format(time.RFC3339Nano), projectID, scope, evidenceRequestKind, request.IdempotencyKey); err != nil {
		return ChangeResult{}, fmt.Errorf("complete Evidence request: %w", err)
	}
	if err := s.failEvidence(EvidenceFailureAfterResultStore); err != nil {
		return ChangeResult{}, err
	}
	return result, nil
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
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
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
	root, err := openSecureDirectory(anchor, rootRelative)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return evidenceSource{}, semanticError("evidence_source_changed", "Evidence source is missing", "source_path", nil)
		}
		return evidenceSource{}, semanticError("evidence_source_forbidden", "Evidence source root cannot be opened", "source_path", nil)
	}
	file, err := root.Open(relativePath)
	if err != nil {
		root.Close()
		if errors.Is(err, os.ErrNotExist) {
			return evidenceSource{}, semanticError("evidence_source_changed", "Evidence source is missing", "source_path", nil)
		}
		return evidenceSource{}, semanticError("evidence_source_forbidden", "Evidence source escapes permitted roots or is not readable", "source_path", nil)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		root.Close()
		return evidenceSource{}, semanticError("evidence_source_forbidden", "Evidence source must be a regular file", "source_path", nil)
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
	return evidenceSource{root: root, file: file, rootPath: rootPath, relativePath: relativePath, identity: fileIdentity(identityPath, info), sha256: hex.EncodeToString(hash.Sum(nil)), size: size, info: info}, nil
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

func (s *Service) reserveEvidenceRequest(ctx context.Context, projectID, scope, key, requestHash string, source evidenceSource) (evidenceRequestRow, bool, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO blackboard_interface_requests(project_id,idempotency_scope,request_kind,idempotency_key,request_hash,source_identity,source_sha256,source_size_bytes,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?, 'reserved',?,?)`, projectID, scope, evidenceRequestKind, key, requestHash, source.identity, source.sha256, source.size, now, now)
	if err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("reserve Evidence request: %w", err)
	}
	rows, _ := result.RowsAffected()
	row, found, err := s.readEvidenceRequest(ctx, projectID, scope, key)
	if err != nil {
		return evidenceRequestRow{}, false, err
	}
	if !found {
		return evidenceRequestRow{}, false, fmt.Errorf("reserved Evidence request was not found")
	}
	return row, rows == 0, nil
}

func (s *Service) readEvidenceRequest(ctx context.Context, projectID, scope, key string) (evidenceRequestRow, bool, error) {
	var row evidenceRequestRow
	err := s.db.QueryRowContext(ctx, `SELECT request_hash,source_identity,source_sha256,source_size_bytes,status,managed_path,result_json FROM blackboard_interface_requests WHERE project_id=? AND idempotency_scope=? AND request_kind=? AND idempotency_key=?`, projectID, scope, evidenceRequestKind, key).Scan(&row.requestHash, &row.sourceIdentity, &row.sha256, &row.size, &row.status, &row.managedPath, &row.resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return evidenceRequestRow{}, false, nil
	}
	if err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("read Evidence request: %w", err)
	}
	return row, true, nil
}

func (s *Service) publishEvidenceSource(source evidenceSource, taskID string) (string, error) {
	if !evidenceSourceStillSame(source) {
		return "", semanticError("evidence_source_changed", "Evidence source was replaced during retention", "source_path", nil)
	}
	taskArtifacts := filepath.Join(s.evidenceConfig.RuntimeRoot, taskID, "artifacts")
	taskRelative, ok := relativeWithinRoot(s.evidenceConfig.ArtifactRoot, taskArtifacts)
	if !ok {
		return "", fmt.Errorf("Task Artifact Root escapes managed Artifact Root")
	}
	managedRoot, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
	if err != nil {
		return "", fmt.Errorf("open managed Artifact Root: %w", err)
	}
	defer managedRoot.Close()
	taskRoot, err := openSecureDirectory(managedRoot, taskRelative)
	if err != nil {
		return "", fmt.Errorf("open Task Artifact Root: %w", err)
	}
	defer taskRoot.Close()
	destinationRelative := filepath.Join("retained", source.sha256)
	destinationRoot, err := openSecureDirectory(taskRoot, destinationRelative)
	if err != nil {
		return "", fmt.Errorf("open managed Evidence directory: %w", err)
	}
	defer destinationRoot.Close()
	name := filepath.Base(source.relativePath)
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = "artifact"
	}
	existingInfo, lstatErr := destinationRoot.Lstat(name)
	if lstatErr == nil && existingInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("managed Evidence destination is a symbolic link")
	}
	if lstatErr != nil && !errors.Is(lstatErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect managed Evidence: %w", lstatErr)
	}
	if existing, err := destinationRoot.Open(name); err == nil {
		defer existing.Close()
		openedInfo, statErr := existing.Stat()
		if statErr != nil || existingInfo == nil || !os.SameFile(existingInfo, openedInfo) {
			return "", fmt.Errorf("managed Evidence changed while opening")
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, existing)
		if copyErr != nil || size != source.size || hex.EncodeToString(hash.Sum(nil)) != source.sha256 {
			return "", fmt.Errorf("managed Evidence content-address collision")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("open managed Evidence: %w", err)
	} else {
		tempName, err := newEvidenceTempName()
		if err != nil {
			return "", err
		}
		temp, err := destinationRoot.OpenFile(tempName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return "", fmt.Errorf("create managed Evidence temp file: %w", err)
		}
		defer destinationRoot.Remove(tempName)
		copiedHash := sha256.New()
		copiedSize, copyErr := io.Copy(io.MultiWriter(temp, copiedHash), source.file)
		if copyErr == nil && (copiedSize != source.size || hex.EncodeToString(copiedHash.Sum(nil)) != source.sha256) {
			_ = temp.Close()
			return "", semanticError("evidence_source_changed", "Evidence source bytes changed during retention", "source_path", nil)
		}
		if copyErr == nil {
			copyErr = temp.Sync()
		}
		closeErr := temp.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			return "", fmt.Errorf("write managed Evidence: %w", copyErr)
		}
		if !evidenceSourceStillSame(source) {
			return "", semanticError("evidence_source_changed", "Evidence source was replaced during retention", "source_path", nil)
		}
		if err := destinationRoot.Rename(tempName, name); err != nil {
			return "", fmt.Errorf("publish managed Evidence: %w", err)
		}
	}
	directory, err := destinationRoot.Open(".")
	if err != nil {
		return "", fmt.Errorf("open managed Evidence directory for sync: %w", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return "", fmt.Errorf("sync managed Evidence directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return "", fmt.Errorf("close managed Evidence directory: %w", err)
	}
	return filepath.ToSlash(filepath.Join(taskRelative, destinationRelative, name)), nil
}

func newEvidenceTempName() (string, error) {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("create Evidence temp name: %w", err)
	}
	return ".retain-" + hex.EncodeToString(bytes[:]), nil
}

func evidenceSourceStillSame(source evidenceSource) bool {
	current, err := source.root.Stat(source.relativePath)
	return err == nil && os.SameFile(source.info, current)
}

func openSecureDirectory(root *os.Root, relative string) (*os.Root, error) {
	clean := filepath.Clean(relative)
	if clean == "." {
		return root.OpenRoot(".")
	}
	if clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, errors.New("managed directory escapes its root")
	}
	current := ""
	var final os.FileInfo
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return nil, err
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.New("managed directory contains a non-directory or symbolic link")
		}
		final = info
	}
	directory, err := root.OpenRoot(clean)
	if err != nil {
		return nil, err
	}
	opened, err := directory.Stat(".")
	if err != nil || !os.SameFile(final, opened) {
		directory.Close()
		return nil, errors.New("managed directory changed while opening")
	}
	return directory, nil
}

func (s *Service) applyRetainedEvidence(ctx context.Context, projectID, continuationID string, request RetainEvidenceRequest, managedPath string, source evidenceSource) (ChangeResult, error) {
	requestHash, err := retainedEvidenceRequestHash(request)
	if err != nil {
		return ChangeResult{}, err
	}
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
		return replay, nil
	}
	status, err := continuationProjectStatus(ctx, tx, projectID, continuationID)
	if err != nil {
		return ChangeResult{}, err
	}
	if !continuationCanWrite(status) {
		return ChangeResult{}, semanticError("closed_continuation", "trusted Continuation is closed for new Blackboard writes", "", nil)
	}
	attempt, err := loadCurrentRecord(ctx, tx, projectID, request.Attempt)
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
	record := EvidenceRecord{Status: "available", ArtifactType: request.ArtifactType, Summary: request.Summary, MediaType: request.MediaType, SourcePath: request.SourcePath, ManagedPath: managedPath, SHA256: source.sha256, Size: source.size, CapturedAt: request.CapturedAt}
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
	links := make([]EvidenceLink, 0, len(request.Links)+1)
	links = append(links, EvidenceLink{"produced", request.Attempt})
	links = append(links, request.Links...)
	for index, link := range links {
		change := Change{Op: "relate", From: request.Key, Relation: link[0], To: link[1]}
		if link[0] == "produced" {
			change.From, change.To = request.Attempt, request.Key
		}
		nextRevision, tuple, changed, err := applyRelate(ctx, tx, projectID, revision, index, change, now)
		if err != nil {
			return ChangeResult{}, err
		}
		if changed {
			revision = nextRevision
			changedRelations[relationKey(tuple)] = tuple
		}
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
	return result, nil
}

func (s *Service) failEvidence(point EvidenceFailurePoint) error {
	if s.evidenceConfig.Failures == nil {
		return nil
	}
	return s.evidenceConfig.Failures.FailAfter(point)
}
