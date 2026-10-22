package blackboardv2

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"reflect"
	"strings"
)

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

// internalEvidencePath builds a slash-form relative Evidence path. Journal
// columns and path comparisons use forward slashes on every platform; using
// filepath.Join directly would persist backslashes in rows on Windows and
// break the "/retained/" and "/.evidence-staging/" contract.
func internalEvidencePath(elements ...string) string {
	return filepath.ToSlash(filepath.Join(elements...))
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
	path := internalEvidencePath("projects", hex.EncodeToString(projectDigest[:]), "retained", digest, name)
	if clean := pathpkg.Clean(path); clean != path || pathpkg.IsAbs(clean) || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("planned Evidence path escapes managed storage")
	}
	return path, nil
}

func plannedEvidenceTempPath(internalPath, continuationID, key, requestHash string) (string, error) {
	if len(requestHash) != 64 || continuationID == "" || key == "" {
		return "", fmt.Errorf("invalid Evidence request hash")
	}
	internalPath = filepath.ToSlash(internalPath)
	const marker = "/retained/"
	index := strings.Index(internalPath, marker)
	if index <= 0 {
		return "", fmt.Errorf("planned Evidence path lacks its retained namespace")
	}
	projectRoot := internalPath[:index]
	scope := sha256.Sum256([]byte(continuationID + "\x00" + key))
	path := internalEvidencePath(projectRoot, ".evidence-staging", hex.EncodeToString(scope[:]), requestHash)
	if clean := pathpkg.Clean(path); clean != path || pathpkg.IsAbs(clean) || strings.HasPrefix(clean, "../") || strings.Contains(clean, marker) {
		return "", fmt.Errorf("planned Evidence temp path escapes private staging")
	}
	return path, nil
}

func filesystemSafeEvidencePath(path string) bool {
	clean := pathpkg.Clean(path)
	if clean != path || clean == "." || clean == ".." || pathpkg.IsAbs(clean) || strings.HasPrefix(clean, "../") {
		return false
	}
	for _, component := range strings.Split(clean, "/") {
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
	if pathpkg.Clean(filepath.ToSlash(internalPath)) != planned {
		return "", fmt.Errorf("stored Evidence path does not match its Project and digest")
	}
	return filepath.ToSlash(filepath.Join("artifacts", "retained", digest, filepath.Base(internalPath))), nil
}
