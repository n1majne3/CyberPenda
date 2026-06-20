package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func ValidateMetadata(meta Metadata) error {
	if !idPattern.MatchString(strings.TrimSpace(meta.ID)) {
		return fmt.Errorf("%w: invalid skill id %q", ErrInvalidSkill, meta.ID)
	}
	if strings.TrimSpace(meta.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidSkill)
	}
	return nil
}

func ValidateBundle(root string, meta Metadata) error {
	if err := ValidateMetadata(meta); err != nil {
		return err
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return fmt.Errorf("%w: bundle root is required", ErrInvalidSkill)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("%w: inspect bundle root: %v", ErrInvalidSkill, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: bundle root must be a directory", ErrInvalidSkill)
	}
	instructionPath := filepath.Join(root, "SKILL.md")
	instructionInfo, err := os.Lstat(instructionPath)
	if err != nil {
		return fmt.Errorf("%w: SKILL.md instruction document is required", ErrInvalidSkill)
	}
	if instructionInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: SKILL.md must not be a symlink", ErrInvalidSkill)
	}
	if instructionInfo.IsDir() {
		return fmt.Errorf("%w: SKILL.md must be a file", ErrInvalidSkill)
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: bundle must not contain symlink %s", ErrInvalidSkill, path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		return ValidateRelativeBundlePath(filepath.ToSlash(rel))
	})
}

func ValidateRelativeBundlePath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return fmt.Errorf("%w: bundle path must be relative", ErrInvalidSkill)
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: bundle path must not escape root", ErrInvalidSkill)
		}
	}
	return nil
}
