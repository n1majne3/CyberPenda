// Package workinggraph prepares the protected filesystem layout used by FGS.
// Legacy Intent publication, compilation, and settlement are retired.
package workinggraph

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"pentest/internal/owner"
	"strings"
)

type OwnerContext struct {
	Owner          owner.Contract
	ContinuationID string
	Workdir        string
}

type Projection struct {
	Root     string `json:"root"`
	State    string `json:"state"`
	Facts    string `json:"facts"`
	Data     string `json:"data"`
	Steps    string `json:"steps"`
	Goals    string `json:"goals"`
	Outbox   string `json:"outbox"`
	Receipts string `json:"receipts"`
}

type Service struct{}

func NewService() *Service { return &Service{} }

func (s *Service) Prepare(_ context.Context, request OwnerContext) (Projection, error) {
	if err := request.Owner.Validate(); err != nil {
		return Projection{}, fmt.Errorf("validate Working Graph owner: %w", err)
	}
	if strings.TrimSpace(request.ContinuationID) == "" {
		return Projection{}, errors.New("Working Graph continuation id is required")
	}
	root, err := filepath.Abs(strings.TrimSpace(request.Workdir))
	if err != nil || root == "" {
		return Projection{}, errors.New("Working Graph workdir is invalid")
	}
	if err := ensureDirectoryTree(root); err != nil {
		return Projection{}, err
	}
	graph := filepath.Join(root, "graph")
	projection := Projection{
		Root: root, State: filepath.Join(root, "state.md"),
		Facts: filepath.Join(graph, "facts"), Data: filepath.Join(graph, "data"),
		Steps: filepath.Join(graph, "steps.yaml"), Goals: filepath.Join(graph, "goals.yaml"),
		Outbox:   filepath.Join(graph, "outbox", request.ContinuationID),
		Receipts: filepath.Join(graph, "receipts", request.ContinuationID),
	}
	for _, directory := range []string{graph, projection.Facts, projection.Data, filepath.Dir(projection.Outbox), projection.Outbox, filepath.Dir(projection.Receipts), projection.Receipts} {
		if err := ensureDirectory(directory); err != nil {
			return Projection{}, err
		}
	}
	for path, body := range map[string][]byte{
		projection.State: []byte("# Working Graph state\n"),
		projection.Steps: []byte("steps: []\n"),
		projection.Goals: []byte("goals: []\n"),
	} {
		if err := writeFileIfAbsent(path, body); err != nil {
			return Projection{}, err
		}
	}
	return projection, nil
}

func ensureDirectory(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("Working Graph path is not a regular directory: %s", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return fmt.Errorf("create Working Graph directory %s: %w", path, err)
	}
	return nil
}

func ensureDirectoryTree(path string) error {
	path = filepath.Clean(path)
	missing := make([]string, 0)
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("Working Graph path is not a regular directory: %s", current)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("Working Graph directory has no existing ancestor: %s", path)
		}
	}
	for index := len(missing) - 1; index >= 0; index-- {
		if err := ensureDirectory(missing[index]); err != nil {
			return err
		}
	}
	return nil
}

func writeFileIfAbsent(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		info, inspectErr := os.Lstat(path)
		if inspectErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("Working Graph path is not a regular file: %s", path)
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(body); err != nil {
		return err
	}
	return file.Sync()
}
