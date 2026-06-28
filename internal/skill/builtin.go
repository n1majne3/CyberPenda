package skill

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	builtinAssetsRoot = "builtins/assets"
)

//go:embed builtins/assets
var builtinAssetFS embed.FS

// BuiltinBundles returns the packaged Skill bundles shipped with the daemon.
func BuiltinBundles() ([]ImportedBundle, error) {
	return builtinBundlesFromFS(builtinAssetFS, builtinAssetsRoot)
}

var prunedBuiltinSuccessors = map[string]string{
	"business-logic-testing":    "vulnerabilities-business-logic",
	"sql-injection-testing":     "vulnerabilities-sql-injection",
	"xss-testing":               "vulnerabilities-xss",
	"ssrf-testing":              "vulnerabilities-ssrf",
	"csrf-testing":              "vulnerabilities-csrf",
	"idor-testing":              "vulnerabilities-idor",
	"file-upload-testing":       "vulnerabilities-insecure-file-uploads",
	"command-injection-testing": "vulnerabilities-rce",
}

var retiredPrunedBuiltinIDs = []string{
	"api-security-testing",
	"cloud-kubernetes",
	"cloud-security-audit",
	"container-security-testing",
	"cyberstrike-eino-demo",
	"deserialization-testing",
	"security-awareness-training",
	"incident-response",
	"security-automation",
	"secure-code-review",
	"vulnerability-assessment",
	"ldap-injection-testing",
	"mobile-app-security-testing",
	"network-penetration-testing",
	"coordination-root-agent",
	"scan-modes-quick",
	"scan-modes-standard",
	"scan-modes-deep",
	"tooling-agent-browser",
	"tooling-python",
	"xpath-injection-testing",
	"xxe-testing",
}

// InstallBuiltinSkills publishes packaged built-in Skills that are not already
// present. Existing skills are left untouched so user edits survive daemon
// restarts.
func (s *Service) InstallBuiltinSkills(ctx context.Context) error {
	if err := s.purgeRetiredLegacyBuiltins(ctx); err != nil {
		return err
	}
	bundles, err := BuiltinBundles()
	if err != nil {
		return err
	}
	for _, bundle := range bundles {
		if err := ctx.Err(); err != nil {
			return err
		}
		existing, err := s.Get(bundle.Metadata.ID)
		if errors.Is(err, ErrNotFound) {
			existing, err = s.migrateLegacyBuiltinSkillID(bundle.Metadata.ID)
		}
		if err == nil {
			if existing.Source.Kind == "builtin" {
				if err := s.sanitizeBuiltinSkill(existing, bundle.Metadata); err != nil {
					return fmt.Errorf("sanitize builtin skill %q: %w", bundle.Metadata.ID, err)
				}
				if err := s.repairMissingBuiltinBundle(ctx, existing, bundle); err != nil {
					return err
				}
			}
			continue
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if _, err := s.Publish(ctx, PublishRequest{Metadata: bundle.Metadata, Files: bundle.Files}); err != nil {
			return fmt.Errorf("install builtin skill %q: %w", bundle.Metadata.ID, err)
		}
	}
	return nil
}

func (s *Service) migrateLegacyBuiltinSkillID(newID string) (Skill, error) {
	for _, legacyID := range legacyBuiltinIDs(newID) {
		existing, err := s.Get(legacyID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return Skill{}, err
		}
		if existing.Source.Kind != "builtin" {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return Skill{}, fmt.Errorf("begin builtin skill id migration: %w", err)
		}
		if _, err := tx.Exec(`UPDATE skills SET id = ?, updated_at = ? WHERE id = ?`, newID, time.Now().UTC().Format(time.RFC3339Nano), legacyID); err != nil {
			_ = tx.Rollback()
			return Skill{}, fmt.Errorf("migrate builtin skill metadata id: %w", err)
		}
		if _, err := tx.Exec(`UPDATE skill_profile_opt_outs SET skill_id = ? WHERE skill_id = ?`, newID, legacyID); err != nil {
			_ = tx.Rollback()
			return Skill{}, fmt.Errorf("migrate builtin skill opt-outs: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return Skill{}, fmt.Errorf("commit builtin skill id migration: %w", err)
		}
		oldPath := s.bundlePath(legacyID)
		newPath := s.bundlePath(newID)
		if _, err := os.Lstat(oldPath); err == nil {
			if _, newErr := os.Lstat(newPath); os.IsNotExist(newErr) {
				if err := os.Rename(oldPath, newPath); err != nil {
					return Skill{}, fmt.Errorf("migrate builtin skill bundle path: %w", err)
				}
			}
		} else if !os.IsNotExist(err) {
			return Skill{}, fmt.Errorf("inspect legacy builtin skill bundle path: %w", err)
		}
		return s.Get(newID)
	}
	return Skill{}, ErrNotFound
}

func legacyBuiltinIDs(id string) []string {
	return []string{"cyberstrikeai-" + id, "strix-" + id}
}

func supersededLegacyBuiltinIDs(newID string) []string {
	var ids []string
	for prunedID, successorID := range prunedBuiltinSuccessors {
		if successorID != newID {
			continue
		}
		ids = append(ids, legacyBuiltinIDs(prunedID)...)
		ids = append(ids, prunedID)
	}
	return ids
}

func (s *Service) purgeRetiredLegacyBuiltins(ctx context.Context) error {
	bundles, err := BuiltinBundles()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	purge := func(legacyID, successorID string) error {
		legacyID = strings.TrimSpace(legacyID)
		if legacyID == "" || seen[legacyID] {
			return nil
		}
		seen[legacyID] = true
		return s.purgeLegacyBuiltinIfPresent(ctx, legacyID, successorID)
	}
	for _, bundle := range bundles {
		for _, legacyID := range supersededLegacyBuiltinIDs(bundle.Metadata.ID) {
			if err := purge(legacyID, bundle.Metadata.ID); err != nil {
				return err
			}
		}
	}
	for _, prunedID := range retiredPrunedBuiltinIDs {
		for _, legacyID := range append(legacyBuiltinIDs(prunedID), prunedID) {
			if err := purge(legacyID, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) purgeLegacyBuiltinIfPresent(ctx context.Context, legacyID, successorID string) error {
	existing, err := s.Get(legacyID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if existing.Source.Kind != "builtin" {
		return nil
	}
	if strings.TrimSpace(successorID) != "" {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := s.db.Exec(
			`INSERT INTO skill_profile_opt_outs (profile_id, skill_id, created_at)
			 SELECT profile_id, ?, ?
			 FROM skill_profile_opt_outs
			 WHERE skill_id = ?
			 ON CONFLICT(profile_id, skill_id) DO NOTHING`,
			successorID, now, legacyID,
		); err != nil {
			return fmt.Errorf("migrate legacy skill opt-outs %q -> %q: %w", legacyID, successorID, err)
		}
	}
	return s.Delete(ctx, legacyID, true)
}

func (s *Service) repairMissingBuiltinBundle(ctx context.Context, existing Skill, bundle ImportedBundle) error {
	if _, err := s.Files(existing.ID); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	meta := bundle.Metadata
	meta.Source = SourceProvenance{Kind: "builtin"}
	if _, err := s.Publish(ctx, PublishRequest{Metadata: meta, Files: bundle.Files}); err != nil {
		return fmt.Errorf("repair builtin skill %q: %w", existing.ID, err)
	}
	return nil
}

func (s *Service) sanitizeBuiltinSkill(existing Skill, embedded Metadata) error {
	needsSourceRepair := existing.Source.Package != "" || existing.Source.Ref != "" || existing.Source.SourceURL != "" || existing.Source.LastImportedAt != nil || existing.Source.LocalModified
	needsMetadataRepair := existing.Name != embedded.Name || existing.Description != embedded.Description
	if !needsSourceRepair && !needsMetadataRepair {
		return removeObsoleteBuiltinSourceFile(existing.BundlePath)
	}
	sourceJSON, err := json.Marshal(SourceProvenance{Kind: "builtin"})
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`UPDATE skills SET name = ?, description = ?, source_provenance_json = ?, updated_at = ? WHERE id = ?`,
		embedded.Name, embedded.Description, string(sourceJSON), time.Now().UTC().Format(time.RFC3339Nano), existing.ID,
	)
	if err != nil {
		return err
	}
	return removeObsoleteBuiltinSourceFile(existing.BundlePath)
}

func removeObsoleteBuiltinSourceFile(bundlePath string) error {
	if strings.TrimSpace(bundlePath) == "" {
		return nil
	}
	err := os.Remove(filepath.Join(bundlePath, "UPSTREAM.md"))
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	return err
}

func builtinBundlesFromFS(fsys fs.FS, root string) ([]ImportedBundle, error) {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, fmt.Errorf("read builtin skills: %w", err)
	}
	var bundles []ImportedBundle
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		bundleRoot := path.Join(root, id)
		files, err := builtinBundleFiles(fsys, bundleRoot)
		if err != nil {
			return nil, err
		}
		instruction := files["SKILL.md"]
		if strings.TrimSpace(instruction) == "" {
			return nil, fmt.Errorf("%w: builtin skill %q is missing SKILL.md", ErrInvalidSkill, id)
		}
		name, description := parseSkillFrontMatter(instruction)
		if strings.TrimSpace(name) == "" {
			name = id
		}
		bundles = append(bundles, ImportedBundle{
			Metadata: Metadata{
				ID:          id,
				Name:        name,
				Description: description,
				Source:      builtinSource(id),
			},
			Files: files,
		})
	}
	sort.Slice(bundles, func(i, j int) bool {
		return bundles[i].Metadata.ID < bundles[j].Metadata.ID
	})
	return bundles, nil
}

func builtinBundleFiles(fsys fs.FS, root string) (map[string]string, error) {
	files := map[string]string{}
	err := fs.WalkDir(fsys, root, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(filePath, root+"/")
		if err := ValidateRelativeBundlePath(rel); err != nil {
			return err
		}
		raw, err := fs.ReadFile(fsys, filePath)
		if err != nil {
			return err
		}
		files[rel] = string(raw)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read builtin skill bundle %q: %w", root, err)
	}
	return files, nil
}

func parseSkillFrontMatter(markdown string) (string, string) {
	lines := strings.Split(markdown, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", ""
	}
	values := map[string]string{}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "---" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.ToLower(key))
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key != "" {
			values[key] = value
		}
	}
	return values["name"], values["description"]
}

func builtinSource(_ string) SourceProvenance {
	return SourceProvenance{Kind: "builtin"}
}
