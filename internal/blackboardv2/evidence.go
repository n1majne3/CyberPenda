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
	EvidenceFailureBeforeReservation  EvidenceFailurePoint = "before_reservation"
	EvidenceFailureBeforeFilePublish  EvidenceFailurePoint = "before_file_publish"
	EvidenceFailureAfterFileRename    EvidenceFailurePoint = "after_file_rename"
	EvidenceFailureBeforePublishStore EvidenceFailurePoint = "before_publication_checkpoint"
	EvidenceFailureAfterFilePublish   EvidenceFailurePoint = "file_publish"
	EvidenceFailureAfterGraphCommit   EvidenceFailurePoint = "semantic_commit"
	EvidenceFailureAfterResultStore   EvidenceFailurePoint = "result_store"
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
	internalPath   string
	payloadOwned   bool
	status         string
	resultJSON     string
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
			if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET status='completed',result_json=?,updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, string(resultJSON), time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, request.IdempotencyKey); err != nil {
				return ChangeResult{}, fmt.Errorf("complete recovered Evidence request: %w", err)
			}
			return replay, nil
		}
	}
	taskID, status, err := s.continuationTaskStatus(ctx, projectID, continuationID)
	if err != nil {
		return ChangeResult{}, err
	}
	if strings.TrimSpace(s.evidenceConfig.RuntimeRoot) == "" || strings.TrimSpace(s.evidenceConfig.ArtifactRoot) == "" {
		return ChangeResult{}, fmt.Errorf("Evidence Runtime Root and Artifact Root must be configured")
	}
	var source *evidenceSource
	if !exists {
		if !continuationCanWrite(status) {
			return ChangeResult{}, semanticError("closed_continuation", "trusted Continuation is closed for new Blackboard writes", "", nil)
		}
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
			if err := json.Unmarshal([]byte(row.resultJSON), &replay); err != nil {
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
		if source == nil {
			opened, err := s.openRuntimeEvidenceSource(taskID, request.SourcePath)
			if err != nil {
				s.cleanupDefinitiveEvidenceFailure(ctx, projectID, continuationID, request.IdempotencyKey, row, err)
				return ChangeResult{}, err
			}
			source = &opened
			defer source.file.Close()
			defer source.root.Close()
		}
		if err := validateEvidenceReservationRow(row, requestHash, *source); err != nil {
			s.cleanupDefinitiveEvidenceFailure(ctx, projectID, continuationID, request.IdempotencyKey, row, err)
			return ChangeResult{}, err
		}
		if err := s.ensureEvidencePublished(ctx, projectID, continuationID, request.IdempotencyKey, &row, *source); err != nil {
			s.cleanupDefinitiveEvidenceFailure(ctx, projectID, continuationID, request.IdempotencyKey, row, err)
			return ChangeResult{}, err
		}
	} else if row.status == "reserved" {
		if err := s.syncAndCheckpointPublishedEvidence(ctx, projectID, continuationID, request.IdempotencyKey, row.internalPath); err != nil {
			return ChangeResult{}, err
		}
	}
	result, err := s.applyRetainedEvidence(ctx, projectID, continuationID, request, semanticPath, retainedEvidenceMetadata{sha256: row.sha256, size: row.size}, true)
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("begin Evidence reservation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO blackboard_v2_evidence_requests(project_id,continuation_id,idempotency_key,request_hash,source_identity,source_sha256,source_size_bytes,managed_internal_path,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?, 'reserved',?,?)`, projectID, continuationID, key, requestHash, source.identity, source.sha256, source.size, internalPath, now, now)
	if err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("reserve Evidence request: %w", err)
	}
	rows, _ := result.RowsAffected()
	var row evidenceRequestRow
	var payloadOwned int
	if err := tx.QueryRowContext(ctx, `SELECT request_hash,source_identity,source_sha256,source_size_bytes,managed_internal_path,payload_owned,status,result_json FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, projectID, continuationID, key).Scan(&row.requestHash, &row.sourceIdentity, &row.sha256, &row.size, &row.internalPath, &payloadOwned, &row.status, &row.resultJSON); err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("read reserved Evidence request: %w", err)
	}
	row.payloadOwned = payloadOwned == 1
	if row.requestHash != requestHash || row.internalPath != internalPath {
		return evidenceRequestRow{}, false, semanticError("idempotency_conflict", "idempotency key was already used with different semantics", "idempotency_key", map[string]any{"idempotency_key": key})
	}
	if err := validateEvidenceReservationRow(row, requestHash, source); err != nil {
		return evidenceRequestRow{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return evidenceRequestRow{}, false, fmt.Errorf("commit Evidence reservation: %w", err)
	}
	return row, rows == 1, nil
}

func (s *Service) readEvidenceRequest(ctx context.Context, projectID, continuationID, key string) (evidenceRequestRow, bool, error) {
	var row evidenceRequestRow
	var payloadOwned int
	err := s.db.QueryRowContext(ctx, `SELECT request_hash,source_identity,source_sha256,source_size_bytes,managed_internal_path,payload_owned,status,result_json FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, projectID, continuationID, key).Scan(&row.requestHash, &row.sourceIdentity, &row.sha256, &row.size, &row.internalPath, &payloadOwned, &row.status, &row.resultJSON)
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
		if err := json.Unmarshal([]byte(raw), &fact); err != nil {
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

func (s *Service) validateEvidenceDependentFactBases(ctx context.Context, tx *sql.Tx, projectID string, dependent map[string]string) error {
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
			return semanticError("semantic_validation", "Evidence lifecycle change would leave a confirmed Fact without a valid basis", dependent[key], map[string]any{"fact": key})
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
		if err := json.Unmarshal([]byte(raw), &evidence); err != nil {
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
		if err := json.Unmarshal([]byte(raw), &attempt); err != nil {
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

func (s *Service) ensureEvidencePublished(ctx context.Context, projectID, continuationID, key string, row *evidenceRequestRow, source evidenceSource) error {
	if !evidenceSourceStillSame(source) {
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
	directoryPath := filepath.Dir(row.internalPath)
	destinationRoot, err := openSecureDirectory(managedRoot, directoryPath)
	if err != nil {
		return fmt.Errorf("open managed Evidence directory: %w", err)
	}
	defer destinationRoot.Close()
	name := filepath.Base(row.internalPath)
	if existing, err := destinationRoot.Open(name); err == nil {
		_ = existing.Close()
		ready, verifyErr := s.verifyManagedEvidencePayload(row.internalPath, row.sha256, row.size)
		if verifyErr != nil {
			return verifyErr
		}
		if !ready {
			return fmt.Errorf("managed Evidence disappeared during publication")
		}
		return s.syncAndCheckpointPublishedEvidence(ctx, projectID, continuationID, key, row.internalPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect managed Evidence destination: %w", err)
	}
	tempName, err := newEvidenceTempName()
	if err != nil {
		return err
	}
	temp, err := destinationRoot.OpenFile(tempName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create managed Evidence temp file: %w", err)
	}
	defer destinationRoot.Remove(tempName)
	copiedHash := sha256.New()
	copiedSize, copyErr := io.Copy(io.MultiWriter(temp, copiedHash), source.file)
	if copyErr == nil && (copiedSize != source.size || hex.EncodeToString(copiedHash.Sum(nil)) != source.sha256) {
		_ = temp.Close()
		return semanticError("evidence_source_changed", "Evidence source bytes changed during retention", "source_path", nil)
	}
	if copyErr == nil {
		copyErr = temp.Chmod(0o400)
	}
	if copyErr == nil {
		copyErr = temp.Sync()
	}
	closeErr := temp.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return fmt.Errorf("write managed Evidence: %w", copyErr)
	}
	if !evidenceSourceStillSame(source) {
		return semanticError("evidence_source_changed", "Evidence source was replaced during retention", "source_path", nil)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET payload_owned=1,updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND managed_internal_path=?`, time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, key, row.internalPath); err != nil {
		return fmt.Errorf("claim managed Evidence payload: %w", err)
	}
	row.payloadOwned = true
	if err := destinationRoot.Rename(tempName, name); err != nil {
		return fmt.Errorf("publish managed Evidence: %w", err)
	}
	if err := s.failEvidence(EvidenceFailureAfterFileRename); err != nil {
		return err
	}
	if err := syncEvidenceDirectory(destinationRoot); err != nil {
		return err
	}
	if err := s.failEvidence(EvidenceFailureBeforePublishStore); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET status='published',updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, key); err != nil {
		return fmt.Errorf("checkpoint Evidence publication: %w", err)
	}
	row.status = "published"
	return s.failEvidence(EvidenceFailureAfterFilePublish)
}

func (s *Service) syncAndCheckpointPublishedEvidence(ctx context.Context, projectID, continuationID, key, internalPath string) error {
	root, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
	if err != nil {
		return fmt.Errorf("open managed Artifact Root: %w", err)
	}
	defer root.Close()
	directory, err := openSecureDirectory(root, filepath.Dir(internalPath))
	if err != nil {
		return fmt.Errorf("open managed Evidence directory: %w", err)
	}
	defer directory.Close()
	if err := syncEvidenceDirectory(directory); err != nil {
		return err
	}
	if err := s.failEvidence(EvidenceFailureBeforePublishStore); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET status='published',updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, key); err != nil {
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
	if !errors.As(cause, &semanticErr) || semanticErr.Code == "idempotency_conflict" {
		return
	}
	var receiptCount int
	receiptKey := "retain-evidence-v2:" + continuationID + ":" + key
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_v2_idempotency_receipts WHERE project_id=? AND idempotency_key=?`, projectID, receiptKey).Scan(&receiptCount); err != nil || receiptCount != 0 {
		return
	}
	if row.payloadOwned {
		semanticPath, err := semanticEvidencePath(projectID, row.internalPath, row.sha256)
		if err != nil {
			return
		}
		var references int
		if err := s.db.QueryRowContext(ctx, `
			SELECT
				(SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND managed_internal_path=? AND NOT (continuation_id=? AND idempotency_key=?)) +
				(SELECT COUNT(*) FROM blackboard_v2_records WHERE project_id=? AND type='evidence' AND json_extract(record_json,'$.managed_path')=?)`,
			projectID, row.internalPath, continuationID, key, projectID, semanticPath,
		).Scan(&references); err != nil || references != 0 {
			return
		}
		root, err := os.OpenRoot(s.evidenceConfig.ArtifactRoot)
		if err != nil {
			return
		}
		removeErr := root.Remove(row.internalPath)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			_ = root.Close()
			return
		}
		directory, err := openSecureDirectory(root, filepath.Dir(row.internalPath))
		if err == nil {
			err = syncEvidenceDirectory(directory)
			_ = directory.Close()
		}
		_ = root.Close()
		if err != nil {
			return
		}
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=?`, projectID, continuationID, key, row.requestHash)
}

func newEvidenceTempName() (string, error) {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("create Evidence temp name: %w", err)
	}
	return ".retain-" + hex.EncodeToString(bytes[:]), nil
}

func evidenceSourceStillSame(source evidenceSource) bool {
	current, err := source.root.Lstat(source.relativePath)
	return err == nil && current.Mode()&os.ModeSymlink == 0 && os.SameFile(source.info, current)
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

func (s *Service) applyRetainedEvidence(ctx context.Context, projectID, continuationID string, request RetainEvidenceRequest, managedPath string, metadata retainedEvidenceMetadata, durablyReserved bool) (ChangeResult, error) {
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
		if err := json.Unmarshal([]byte(raw), &terminal); err != nil {
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
