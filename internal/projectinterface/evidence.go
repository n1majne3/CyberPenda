package projectinterface

import (
	"bytes"
	"context"
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

	"pentest/internal/blackboard"
)

// EvidenceFailurePoint is an injectable crash boundary permitted by the
// Runtime protocol acceptance seam.
type EvidenceFailurePoint string

const (
	EvidenceFailureBeforeFilePublish EvidenceFailurePoint = "before_file_publish"
	EvidenceFailureAfterFilePublish  EvidenceFailurePoint = "file_publish"
	EvidenceFailureAfterGraphCommit  EvidenceFailurePoint = "graph_commit"
	EvidenceFailureAfterResultStore  EvidenceFailurePoint = "result_store"
)

type EvidenceFailureInjector interface {
	FailAfter(EvidenceFailurePoint) error
}

type EvidenceLink struct {
	EdgeType blackboard.EdgeType `json:"edge_type"`
	To       blackboard.NodeRef  `json:"to"`
}

type RetainEvidenceRequest struct {
	ProtocolVersion   int                `json:"protocol_version"`
	IdempotencyKey    string             `json:"idempotency_key"`
	StableKey         string             `json:"stable_key"`
	ExpectedVersion   *int               `json:"expected_version"`
	ArtifactType      string             `json:"artifact_type"`
	MediaType         string             `json:"media_type,omitempty"`
	SourcePath        string             `json:"source_path"`
	Summary           string             `json:"summary"`
	CapturedAt        string             `json:"captured_at,omitempty"`
	ProducedByAttempt blackboard.NodeRef `json:"produced_by_attempt"`
	Links             []EvidenceLink     `json:"links,omitempty"`
}

type RetainedEvidenceResult struct {
	Node        blackboard.NodeRecord     `json:"node"`
	Edges       []blackboard.EdgeRecord   `json:"edges"`
	ManagedPath string                    `json:"managed_path"`
	SHA256      string                    `json:"sha256"`
	SizeBytes   int64                     `json:"size_bytes"`
	Mutation    blackboard.MutationResult `json:"mutation"`
}

type RetainEvidenceResponse struct {
	ProtocolVersion       int                    `json:"protocol_version"`
	RequestKind           string                 `json:"request_kind"`
	ProjectID             string                 `json:"project_id"`
	ObservedGraphRevision int                    `json:"observed_graph_revision"`
	Result                RetainedEvidenceResult `json:"result"`
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
	requestHash, sourceIdentity, sha256, status, managedPath, resultJSON string
	size                                                                 int64
}

// RetainEvidence publishes one confined payload and represents it through the
// graph service's single Apply seam. Durable request state makes every boundary
// retryable without a filesystem/database two-phase commit.
func (s *Service) RetainEvidence(ctx context.Context, principal Principal, req RetainEvidenceRequest) (RetainEvidenceResponse, error) {
	if err := requireGraph(s.graph); err != nil {
		return RetainEvidenceResponse{}, err
	}
	if err := validateRetainEvidenceRequest(principal, req); err != nil {
		return RetainEvidenceResponse{}, err
	}
	grantStatus := GrantStatusOpen
	if principal.isRuntime() {
		current, err := s.currentGrant(ctx, principal)
		if err != nil {
			return RetainEvidenceResponse{}, err
		}
		grantStatus = current.Status()
		if !grantStatus.IsReadable() {
			return RetainEvidenceResponse{}, continuationClosedError(grantStatus)
		}
	}

	source, err := s.openEvidenceSource(principal, req.SourcePath)
	if err != nil {
		return RetainEvidenceResponse{}, err
	}
	defer source.file.Close()
	defer source.root.Close()

	requestHash, err := evidenceRequestHash(req, source)
	if err != nil {
		return RetainEvidenceResponse{}, InternalError(err.Error())
	}
	scope := evidenceIdempotencyScope(principal)
	var row evidenceRequestRow
	var existed bool
	if principal.isRuntime() && !grantStatus.IsWriteable() {
		row, existed, err = s.readEvidenceRequest(ctx, principal.projectID(), scope, req.IdempotencyKey)
		if err != nil {
			return RetainEvidenceResponse{}, err
		}
		if !existed {
			return RetainEvidenceResponse{}, continuationClosedError(grantStatus)
		}
	} else {
		row, existed, err = s.reserveEvidenceRequest(ctx, principal.projectID(), scope, req.IdempotencyKey, requestHash, source)
	}
	if err != nil {
		return RetainEvidenceResponse{}, err
	}
	if existed {
		if row.sourceIdentity != source.identity || row.sha256 != source.sha256 || row.size != source.size {
			return RetainEvidenceResponse{}, ValidationError(ErrCodeEvidenceSourceChanged, "Evidence source changed across idempotent retry", "source_path")
		}
		if row.requestHash != requestHash {
			return RetainEvidenceResponse{}, ValidationError(blackboard.ErrCodeIdempotencyConflict, "Retain Evidence idempotency key was reused with a different request", "idempotency_key")
		}
		if row.status == "completed" {
			var replay RetainEvidenceResponse
			if err := json.Unmarshal([]byte(row.resultJSON), &replay); err != nil {
				return RetainEvidenceResponse{}, InternalError("decode retained Evidence replay: " + err.Error())
			}
			return replay, nil
		}
	}

	managedPath := row.managedPath
	if managedPath == "" {
		if err := s.failEvidence(EvidenceFailureBeforeFilePublish); err != nil {
			return RetainEvidenceResponse{}, err
		}
		managedPath, err = s.publishEvidenceSource(source, principal)
		if err != nil {
			return RetainEvidenceResponse{}, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_interface_requests SET status='published',managed_path=?,updated_at=? WHERE project_id=? AND idempotency_scope=? AND request_kind='retain_evidence' AND idempotency_key=?`, managedPath, time.Now().UTC().Format(time.RFC3339Nano), principal.projectID(), scope, req.IdempotencyKey); err != nil {
			return RetainEvidenceResponse{}, InternalError("checkpoint Evidence publication: " + err.Error())
		}
		if err := s.failEvidence(EvidenceFailureAfterFilePublish); err != nil {
			return RetainEvidenceResponse{}, err
		}
	}

	mutation, err := s.applyRetainedEvidence(ctx, principal, req, managedPath, source)
	if err != nil {
		return RetainEvidenceResponse{}, err
	}
	if err := s.failEvidence(EvidenceFailureAfterGraphCommit); err != nil {
		return RetainEvidenceResponse{}, err
	}
	node, err := s.graph.ReadNode(ctx, blackboard.ReadNodeRequest{ProjectID: principal.projectID(), NodeType: blackboard.NodeTypeEvidenceArtifact, Key: req.StableKey})
	if err != nil {
		return RetainEvidenceResponse{}, mapGraphError(err)
	}
	edges := make([]blackboard.EdgeRecord, 0, len(req.Links)+1)
	for _, operation := range mutation.Operations {
		if operation.EdgeID == "" {
			continue
		}
		edge, err := s.graph.ReadEdge(ctx, blackboard.ReadEdgeRequest{ProjectID: principal.projectID(), EdgeID: operation.EdgeID})
		if err != nil {
			return RetainEvidenceResponse{}, mapGraphError(err)
		}
		edges = append(edges, edge)
	}
	response := RetainEvidenceResponse{
		ProtocolVersion: RuntimeProtocolVersion, RequestKind: "retain_evidence", ProjectID: principal.projectID(), ObservedGraphRevision: mutation.GraphRevision,
		Result: RetainedEvidenceResult{Node: node.Node, Edges: edges, ManagedPath: managedPath, SHA256: source.sha256, SizeBytes: source.size, Mutation: mutation},
	}
	resultJSON, err := json.Marshal(response)
	if err != nil {
		return RetainEvidenceResponse{}, InternalError("encode retained Evidence result: " + err.Error())
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_interface_requests SET status='completed',result_json=?,updated_at=? WHERE project_id=? AND idempotency_scope=? AND request_kind='retain_evidence' AND idempotency_key=?`, string(resultJSON), time.Now().UTC().Format(time.RFC3339Nano), principal.projectID(), scope, req.IdempotencyKey); err != nil {
		return RetainEvidenceResponse{}, InternalError("complete Evidence request: " + err.Error())
	}
	if err := s.failEvidence(EvidenceFailureAfterResultStore); err != nil {
		return RetainEvidenceResponse{}, err
	}
	return response, nil
}

func validateRetainEvidenceRequest(principal Principal, req RetainEvidenceRequest) *Error {
	if req.ProtocolVersion != RuntimeProtocolVersion {
		return ValidationError(ErrCodeInvalidRequest, fmt.Sprintf("unsupported protocol version %d", req.ProtocolVersion), "protocol_version")
	}
	for _, field := range []struct{ path, value string }{
		{"idempotency_key", req.IdempotencyKey}, {"stable_key", req.StableKey},
		{"artifact_type", req.ArtifactType}, {"source_path", req.SourcePath}, {"summary", req.Summary},
	} {
		if strings.TrimSpace(field.value) == "" {
			return ValidationError(ErrCodeInvalidRequest, field.path+" is required", field.path)
		}
	}
	if principal.isRuntime() && req.ProducedByAttempt.ID == "" &&
		(req.ProducedByAttempt.NodeType != blackboard.NodeTypeAttempt || req.ProducedByAttempt.StableKey == "") {
		return ValidationError(ErrCodeInvalidRequest, "Runtime Evidence requires produced_by_attempt", "produced_by_attempt")
	}
	for i, link := range req.Links {
		if link.EdgeType != blackboard.EdgeTypeEvidences && link.EdgeType != blackboard.EdgeTypeAbout {
			return ValidationError(ErrCodeActorForbidden, "Retain Evidence links may only be evidences or about", fmt.Sprintf("links[%d].edge_type", i))
		}
	}
	return nil
}

func evidenceIdempotencyScope(principal Principal) string {
	if principal.isRuntime() {
		return "continuation:" + principal.Grant.ContinuationID
	}
	return "operator:" + principal.ActorID
}

func (s *Service) openEvidenceSource(principal Principal, sourcePath string) (evidenceSource, error) {
	rootPath, relativePath, err := s.evidenceSourceLocation(principal, sourcePath)
	if err != nil {
		return evidenceSource{}, err
	}
	root, err := s.openEvidenceSourceRoot(principal, rootPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return evidenceSource{}, ValidationError(ErrCodeEvidenceSourceChanged, "Evidence source is missing", "source_path")
		}
		return evidenceSource{}, ValidationError(ErrCodeEvidenceSourceForbidden, "Evidence source root cannot be opened", "source_path")
	}
	file, err := root.Open(relativePath)
	if err != nil {
		root.Close()
		if errors.Is(err, os.ErrNotExist) {
			return evidenceSource{}, ValidationError(ErrCodeEvidenceSourceChanged, "Evidence source is missing", "source_path")
		}
		if errors.Is(err, os.ErrPermission) {
			return evidenceSource{}, ValidationError(ErrCodeEvidenceSourceForbidden, "Evidence source escapes permitted roots or is not readable", "source_path")
		}
		return evidenceSource{}, ValidationError(ErrCodeEvidenceSourceForbidden, "Evidence source cannot be opened within permitted roots", "source_path")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		root.Close()
		return evidenceSource{}, ValidationError(ErrCodeEvidenceSourceForbidden, "Evidence source must be a regular file", "source_path")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		file.Close()
		root.Close()
		return evidenceSource{}, InternalError("hash Evidence source: " + err.Error())
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		root.Close()
		return evidenceSource{}, InternalError("rewind Evidence source: " + err.Error())
	}
	identityPath := filepath.Join(rootPath, relativePath)
	return evidenceSource{root: root, file: file, rootPath: rootPath, relativePath: relativePath, identity: fileIdentity(identityPath, info), sha256: hex.EncodeToString(hash.Sum(nil)), size: size, info: info}, nil
}

func (s *Service) openEvidenceSourceRoot(principal Principal, rootPath string) (*os.Root, error) {
	if !principal.isRuntime() {
		return os.OpenRoot(rootPath)
	}
	anchor, err := os.OpenRoot(s.runtimeRoot)
	if err != nil {
		return nil, err
	}
	defer anchor.Close()
	relative, ok := relativeWithinRoot(s.runtimeRoot, rootPath)
	if !ok {
		return nil, errors.New("Runtime source root escapes configured Runtime Root")
	}
	return openSecureDirectory(anchor, relative)
}

func (s *Service) evidenceSourceLocation(principal Principal, sourcePath string) (string, string, error) {
	if principal.isRuntime() {
		if strings.TrimSpace(s.runtimeRoot) == "" {
			return "", "", InternalError("Runtime Root is not configured")
		}
		taskRoot := filepath.Join(s.runtimeRoot, principal.Grant.TaskID)
		workdir, artifacts := filepath.Join(taskRoot, "workdir"), filepath.Join(taskRoot, "artifacts")
		clean := filepath.Clean(sourcePath)
		switch {
		case filepath.IsAbs(clean) && (clean == "/task/workdir" || strings.HasPrefix(clean, "/task/workdir/")):
			return workdir, strings.TrimPrefix(strings.TrimPrefix(clean, "/task/workdir"), "/"), nil
		case filepath.IsAbs(clean) && (clean == "/task/artifacts" || strings.HasPrefix(clean, "/task/artifacts/")):
			return artifacts, strings.TrimPrefix(strings.TrimPrefix(clean, "/task/artifacts"), "/"), nil
		case !filepath.IsAbs(clean):
			if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return "", "", ValidationError(ErrCodeEvidenceSourceForbidden, "Evidence source escapes the Task roots", "source_path")
			}
			return workdir, clean, nil
		}
		if relative, ok := relativeWithinRoot(workdir, clean); ok {
			return workdir, relative, nil
		}
		if relative, ok := relativeWithinRoot(artifacts, clean); ok {
			return artifacts, relative, nil
		}
		return "", "", ValidationError(ErrCodeEvidenceSourceForbidden, "Evidence source escapes the Task roots", "source_path")
	}
	if len(s.operatorRoots) == 0 {
		return "", "", ValidationError(ErrCodeEvidenceSourceForbidden, "no operator Evidence source root is configured", "source_path")
	}
	if !filepath.IsAbs(sourcePath) {
		return "", "", ValidationError(ErrCodeEvidenceSourceForbidden, "operator Evidence source_path must be absolute", "source_path")
	}
	clean := filepath.Clean(sourcePath)
	for _, root := range s.operatorRoots {
		if relative, ok := relativeWithinRoot(root, clean); ok {
			return root, relative, nil
		}
	}
	return "", "", ValidationError(ErrCodeEvidenceSourceForbidden, "Evidence source escapes configured operator roots", "source_path")
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

func evidenceRequestHash(req RetainEvidenceRequest, source evidenceSource) (string, error) {
	body := struct {
		Request  RetainEvidenceRequest `json:"request"`
		Identity string                `json:"source_identity"`
		SHA256   string                `json:"source_sha256"`
		Size     int64                 `json:"source_size_bytes"`
	}{req, source.identity, source.sha256, source.size}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) reserveEvidenceRequest(ctx context.Context, projectID, scope, key, requestHash string, source evidenceSource) (evidenceRequestRow, bool, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO blackboard_interface_requests(project_id,idempotency_scope,request_kind,idempotency_key,request_hash,source_identity,source_sha256,source_size_bytes,status,created_at,updated_at) VALUES(?,?, 'retain_evidence',?,?,?,?,?,'reserved',?,?)`, projectID, scope, key, requestHash, source.identity, source.sha256, source.size, now, now)
	if err != nil {
		return evidenceRequestRow{}, false, InternalError("reserve Evidence request: " + err.Error())
	}
	rows, _ := result.RowsAffected()
	var row evidenceRequestRow
	err = s.db.QueryRowContext(ctx, `SELECT request_hash,source_identity,source_sha256,source_size_bytes,status,managed_path,result_json FROM blackboard_interface_requests WHERE project_id=? AND idempotency_scope=? AND request_kind='retain_evidence' AND idempotency_key=?`, projectID, scope, key).
		Scan(&row.requestHash, &row.sourceIdentity, &row.sha256, &row.size, &row.status, &row.managedPath, &row.resultJSON)
	if err != nil {
		return evidenceRequestRow{}, false, InternalError("read Evidence request: " + err.Error())
	}
	return row, rows == 0, nil
}

func (s *Service) readEvidenceRequest(ctx context.Context, projectID, scope, key string) (evidenceRequestRow, bool, error) {
	var row evidenceRequestRow
	err := s.db.QueryRowContext(ctx, `SELECT request_hash,source_identity,source_sha256,source_size_bytes,status,managed_path,result_json FROM blackboard_interface_requests WHERE project_id=? AND idempotency_scope=? AND request_kind='retain_evidence' AND idempotency_key=?`, projectID, scope, key).
		Scan(&row.requestHash, &row.sourceIdentity, &row.sha256, &row.size, &row.status, &row.managedPath, &row.resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return evidenceRequestRow{}, false, nil
	}
	if err != nil {
		return evidenceRequestRow{}, false, InternalError("read Evidence request: " + err.Error())
	}
	return row, true, nil
}

func (s *Service) publishEvidenceSource(source evidenceSource, principal Principal) (string, error) {
	if !evidenceSourceStillSame(source) {
		return "", ValidationError(ErrCodeEvidenceSourceChanged, "Evidence source was replaced during retention", "source_path")
	}
	taskArtifacts := filepath.Join(s.runtimeRoot, principal.Grant.TaskID, "artifacts")
	if !principal.isRuntime() {
		taskArtifacts = filepath.Join(s.artifactRoot, "artifacts", "operator", principal.projectID())
	}
	taskArtifactsRelative, ok := relativeWithinRoot(s.artifactRoot, taskArtifacts)
	if !ok {
		return "", InternalError("Task Artifact Root escapes managed Artifact Root")
	}
	managedRoot, err := os.OpenRoot(s.artifactRoot)
	if err != nil {
		return "", InternalError("open managed Artifact Root: " + err.Error())
	}
	defer managedRoot.Close()
	taskRoot, err := openSecureDirectory(managedRoot, taskArtifactsRelative)
	if err != nil {
		return "", InternalError("open Task Artifact Root: " + err.Error())
	}
	defer taskRoot.Close()
	destinationDirRelative := filepath.Join("retained", source.sha256)
	destinationRoot, err := openSecureDirectory(taskRoot, destinationDirRelative)
	if err != nil {
		return "", InternalError("open managed Evidence directory: " + err.Error())
	}
	defer destinationRoot.Close()
	name := filepath.Base(source.relativePath)
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = "artifact"
	}
	existingInfo, lstatErr := destinationRoot.Lstat(name)
	if lstatErr == nil && existingInfo.Mode()&os.ModeSymlink != 0 {
		return "", InternalError("managed Evidence destination is a symbolic link")
	}
	if lstatErr != nil && !errors.Is(lstatErr, os.ErrNotExist) {
		return "", InternalError("inspect managed Evidence: " + lstatErr.Error())
	}
	if existing, err := destinationRoot.Open(name); err == nil {
		defer existing.Close()
		openedInfo, statErr := existing.Stat()
		if statErr != nil || existingInfo == nil || !os.SameFile(existingInfo, openedInfo) {
			return "", InternalError("managed Evidence changed while opening")
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, existing)
		if copyErr != nil || size != source.size || hex.EncodeToString(hash.Sum(nil)) != source.sha256 {
			return "", InternalError("managed Evidence content-address collision")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", InternalError("open managed Evidence: " + err.Error())
	} else {
		tempName := ".retain-" + newID()
		temp, err := destinationRoot.OpenFile(tempName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return "", InternalError("create managed Evidence temp file: " + err.Error())
		}
		defer destinationRoot.Remove(tempName)
		copiedHash := sha256.New()
		var copiedSize int64
		if err := temp.Chmod(0o600); err == nil {
			copiedSize, err = io.Copy(io.MultiWriter(temp, copiedHash), source.file)
		}
		if err == nil && (copiedSize != source.size || hex.EncodeToString(copiedHash.Sum(nil)) != source.sha256) {
			_ = temp.Close()
			return "", ValidationError(ErrCodeEvidenceSourceChanged, "Evidence source bytes changed during retention", "source_path")
		}
		if err == nil {
			err = temp.Sync()
		}
		closeErr := temp.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return "", InternalError("write managed Evidence: " + err.Error())
		}
		if !evidenceSourceStillSame(source) {
			return "", ValidationError(ErrCodeEvidenceSourceChanged, "Evidence source was replaced during retention", "source_path")
		}
		if err := destinationRoot.Rename(tempName, name); err != nil {
			return "", InternalError("publish managed Evidence: " + err.Error())
		}
	}
	directory, err := destinationRoot.Open(".")
	if err != nil {
		return "", InternalError("open managed Evidence directory for sync: " + err.Error())
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return "", InternalError("sync managed Evidence directory: " + err.Error())
	}
	if err := directory.Close(); err != nil {
		return "", InternalError("close managed Evidence directory: " + err.Error())
	}
	managedPath := filepath.Join(taskArtifactsRelative, destinationDirRelative, name)
	return filepath.ToSlash(managedPath), nil
}

func evidenceSourceStillSame(source evidenceSource) bool {
	current, err := source.root.Stat(source.relativePath)
	return err == nil && os.SameFile(source.info, current)
}

// openSecureDirectory creates and pins a directory beneath root without
// accepting symbolic-link path components. The final SameFile check closes the
// check/open race: subsequent operations use the returned directory handle.
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

func (s *Service) applyRetainedEvidence(ctx context.Context, principal Principal, req RetainEvidenceRequest, managedPath string, source evidenceSource) (blackboard.MutationResult, error) {
	if principal.isRuntime() {
		var attempt blackboard.NodeRecord
		if req.ProducedByAttempt.ID != "" {
			literal, err := s.graph.ReadLiteralNode(ctx, blackboard.ReadLiteralNodeRequest{ProjectID: principal.projectID(), NodeID: req.ProducedByAttempt.ID})
			if err != nil {
				return blackboard.MutationResult{}, mapGraphError(err)
			}
			attempt = literal.Node
		} else {
			resolved, err := s.graph.ReadNode(ctx, blackboard.ReadNodeRequest{ProjectID: principal.projectID(), NodeType: blackboard.NodeTypeAttempt, Key: req.ProducedByAttempt.StableKey})
			if err != nil {
				return blackboard.MutationResult{}, mapGraphError(err)
			}
			attempt = resolved.Node
		}
		if attempt.NodeType != blackboard.NodeTypeAttempt {
			return blackboard.MutationResult{}, ValidationError(ErrCodeInvalidRequest, "produced_by_attempt must reference an Attempt", "produced_by_attempt")
		}
		status, _ := attempt.PropertyMap["status"].(string)
		if status == "" {
			status = "open"
		}
		if status != "open" && status != "succeeded" && status != "failed" && status != "blocked" && status != "inconclusive" && status != "interrupted" {
			return blackboard.MutationResult{}, ValidationError(ErrCodeActorForbidden, "producing Attempt is not open or terminal", "produced_by_attempt")
		}
		provenance, err := s.graph.ReadNodeRuntimeProvenance(ctx, principal.projectID(), attempt.ID)
		if err != nil {
			return blackboard.MutationResult{}, mapGraphError(err)
		}
		if provenance.ActorType != blackboard.ActorTypeRuntime || provenance.TaskID != principal.Grant.TaskID || provenance.ContinuationID != principal.Grant.ContinuationID {
			return blackboard.MutationResult{}, ValidationError(ErrCodeSourceEventMismatch, "producing Attempt does not match the bound Task and Continuation", "produced_by_attempt")
		}
	}
	props := map[string]any{"artifact_type": req.ArtifactType, "source_path": req.SourcePath, "managed_path": managedPath, "sha256": source.sha256, "size_bytes": source.size, "summary": req.Summary, "status": "available"}
	if req.MediaType != "" {
		props["media_type"] = req.MediaType
	}
	if req.CapturedAt != "" {
		props["captured_at"] = req.CapturedAt
	}
	evidenceRef := blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: req.StableKey}
	operations := []blackboard.Operation{}
	if req.ExpectedVersion == nil {
		operations = append(operations, blackboard.Operation{OpID: "evidence", Kind: blackboard.OpCreateNode, Node: evidenceRef, Create: blackboard.CreateNodeInput{PropertyMap: props}})
		evidenceRef = blackboard.NodeRef{OpID: "evidence"}
	} else {
		delete(props, "status")
		current, err := s.graph.ReadNode(ctx, blackboard.ReadNodeRequest{ProjectID: principal.projectID(), NodeType: blackboard.NodeTypeEvidenceArtifact, Key: req.StableKey})
		if err != nil {
			return blackboard.MutationResult{}, mapGraphError(err)
		}
		operations = append(operations, blackboard.Operation{OpID: "evidence", Kind: blackboard.OpPatchNode, Node: evidenceRef, Patch: blackboard.PatchNodeInput{ExpectedVersion: *req.ExpectedVersion, Properties: props}})
		status, _ := current.Node.PropertyMap["status"].(string)
		if status == "" {
			status = "available"
		}
		if status != "available" {
			expectedTransitionVersion := *req.ExpectedVersion
			if evidencePatchChanges(current.Node.PropertyMap, props) {
				expectedTransitionVersion++
			}
			operations = append(operations, blackboard.Operation{
				OpID: "available", Kind: blackboard.OpTransitionNode, Node: evidenceRef,
				Transition: blackboard.TransitionNodeInput{ExpectedVersion: expectedTransitionVersion, Status: "available"},
			})
		}
	}
	if principal.isRuntime() {
		operations = append(operations, blackboard.Operation{OpID: "produced", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeProduced, From: req.ProducedByAttempt, To: evidenceRef}})
	}
	for i, link := range req.Links {
		operations = append(operations, blackboard.Operation{OpID: fmt.Sprintf("link-%d", i), Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: link.EdgeType, From: evidenceRef, To: link.To}})
	}
	projectKind, err := s.loadProjectKind(ctx, principal.projectID())
	if err != nil {
		return blackboard.MutationResult{}, err
	}
	execCtx := blackboard.ExecutionContext{ProjectID: principal.projectID(), ProjectKind: projectKind, ActorType: principal.ActorType, ActorID: principal.ActorID}
	if principal.isRuntime() {
		execCtx.TaskID, execCtx.ContinuationID, execCtx.RuntimeProfileID, execCtx.Runner = principal.Grant.TaskID, principal.Grant.ContinuationID, principal.Grant.RuntimeProfileID, principal.Grant.Runner
	}
	result, err := s.graph.Apply(ctx, blackboard.MutationBatch{SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "retain-evidence:" + req.IdempotencyKey, Context: execCtx, Operations: operations})
	if err != nil {
		return blackboard.MutationResult{}, mapGraphError(err)
	}
	return result, nil
}

func evidencePatchChanges(current, patch map[string]any) bool {
	merged := make(map[string]any, len(current)+len(patch))
	for key, value := range current {
		merged[key] = value
	}
	for key, value := range patch {
		merged[key] = value
	}
	currentJSON, currentErr := json.Marshal(current)
	mergedJSON, mergedErr := json.Marshal(merged)
	return currentErr != nil || mergedErr != nil || !bytes.Equal(currentJSON, mergedJSON)
}

func (s *Service) failEvidence(point EvidenceFailurePoint) error {
	if s.evidenceFailures == nil {
		return nil
	}
	return s.evidenceFailures.FailAfter(point)
}
