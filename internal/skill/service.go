package skill

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pentest/internal/store"
)

type PublishRequest struct {
	Metadata Metadata
	Files    map[string]string
}

type ImportRequest struct {
	SourceKind string `json:"source_kind"`
	Package    string `json:"package,omitempty"`
	Ref        string `json:"ref,omitempty"`
	SourceURL  string `json:"source_url,omitempty"`
}

type ImportedBundle struct {
	Metadata Metadata
	Files    map[string]string
}

type Importer interface {
	ImportSkill(ctx context.Context, request ImportRequest) (ImportedBundle, error)
}

type Service struct {
	db          *store.DB
	libraryRoot string
	importer    Importer
}

func NewService(db *store.DB, libraryRoot string, importers ...Importer) *Service {
	if strings.TrimSpace(libraryRoot) == "" {
		libraryRoot = filepath.Join(".", "skills")
	}
	svc := &Service{db: db, libraryRoot: libraryRoot}
	if len(importers) > 0 {
		svc.importer = importers[0]
	}
	return svc
}

func (s *Service) Publish(ctx context.Context, req PublishRequest) (Skill, error) {
	if err := ctx.Err(); err != nil {
		return Skill{}, err
	}
	meta := normalizeMetadata(req.Metadata)
	if len(req.Files) == 0 {
		return Skill{}, fmt.Errorf("%w: bundle files are required", ErrInvalidSkill)
	}
	if err := ValidateMetadata(meta); err != nil {
		return Skill{}, err
	}
	staging, err := s.writeStagingBundle(meta, req.Files)
	if err != nil {
		return Skill{}, err
	}
	defer os.RemoveAll(staging)
	if err := ValidateBundle(staging, meta); err != nil {
		return Skill{}, err
	}

	live := s.bundlePath(meta.ID)
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		return Skill{}, fmt.Errorf("prepare skill library: %w", err)
	}
	backup := live + ".backup-" + newID()
	hadLive := false
	if _, err := os.Lstat(live); err == nil {
		hadLive = true
		if err := os.Rename(live, backup); err != nil {
			return Skill{}, fmt.Errorf("stage existing live skill: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return Skill{}, fmt.Errorf("inspect live skill: %w", err)
	}

	restore := func() {
		_ = os.RemoveAll(live)
		if hadLive {
			_ = os.Rename(backup, live)
		}
	}
	if err := os.Rename(staging, live); err != nil {
		restore()
		return Skill{}, fmt.Errorf("promote skill bundle: %w", err)
	}

	stored, err := s.upsertMetadata(meta)
	if err != nil {
		restore()
		return Skill{}, err
	}
	if hadLive {
		_ = os.RemoveAll(backup)
	}
	stored.BundlePath = live
	return stored, nil
}

func (s *Service) Import(ctx context.Context, req ImportRequest) (Skill, error) {
	if s.importer == nil {
		return Skill{}, fmt.Errorf("skill importer is not configured")
	}
	if strings.TrimSpace(req.SourceKind) == "" {
		return Skill{}, fmt.Errorf("%w: source_kind is required", ErrInvalidSkill)
	}
	imported, err := s.importer.ImportSkill(ctx, req)
	if err != nil {
		return Skill{}, err
	}
	meta := imported.Metadata
	if strings.TrimSpace(meta.Source.Kind) == "" {
		meta.Source.Kind = strings.TrimSpace(req.SourceKind)
	}
	if strings.TrimSpace(meta.Source.Package) == "" {
		meta.Source.Package = strings.TrimSpace(req.Package)
	}
	if strings.TrimSpace(meta.Source.Ref) == "" {
		meta.Source.Ref = strings.TrimSpace(req.Ref)
	}
	if strings.TrimSpace(meta.Source.SourceURL) == "" {
		meta.Source.SourceURL = strings.TrimSpace(req.SourceURL)
	}
	now := time.Now().UTC()
	meta.Source.LastImportedAt = &now
	return s.Publish(ctx, PublishRequest{Metadata: meta, Files: imported.Files})
}

func (s *Service) Get(id string) (Skill, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Skill{}, ErrNotFound
	}
	return s.scanSkill(s.db.QueryRow(
		`SELECT id, name, description, source_provenance_json, created_at, updated_at FROM skills WHERE id = ?`,
		id,
	))
}

func (s *Service) List() ([]Skill, error) {
	rows, err := s.db.Query(`SELECT id, name, description, source_provenance_json, created_at, updated_at FROM skills ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	defer rows.Close()
	var out []Skill
	for rows.Next() {
		got, err := s.scanSkill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, got)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate skills: %w", err)
	}
	if out == nil {
		out = []Skill{}
	}
	return out, nil
}

func (s *Service) Files(id string) (map[string]string, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	root := s.bundlePath(strings.TrimSpace(id))
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: bundle must not contain symlink %s", ErrInvalidSkill, path)
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if err := ValidateRelativeBundlePath(rel); err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read skill file %q: %w", rel, err)
		}
		files[rel] = string(raw)
		return nil
	})
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (s *Service) SetOptOut(profileID, skillID string, optedOut bool) error {
	profileID = strings.TrimSpace(profileID)
	skillID = strings.TrimSpace(skillID)
	if profileID == "" || skillID == "" {
		return ErrNotFound
	}
	if _, err := s.Get(skillID); err != nil {
		return err
	}
	var profileCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM runtime_profiles WHERE id = ?`, profileID).Scan(&profileCount); err != nil {
		return fmt.Errorf("check runtime profile: %w", err)
	}
	if profileCount == 0 {
		return ErrNotFound
	}
	if optedOut {
		_, err := s.db.Exec(
			`INSERT INTO skill_profile_opt_outs (profile_id, skill_id, created_at) VALUES (?, ?, ?)
			 ON CONFLICT(profile_id, skill_id) DO NOTHING`,
			profileID, skillID, time.Now().UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("store skill opt-out: %w", err)
		}
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM skill_profile_opt_outs WHERE profile_id = ? AND skill_id = ?`, profileID, skillID)
	if err != nil {
		return fmt.Errorf("delete skill opt-out: %w", err)
	}
	return nil
}

func (s *Service) EnabledSkills(profileID string) ([]Skill, error) {
	profileID = strings.TrimSpace(profileID)
	rows, err := s.db.Query(
		`SELECT id, name, description, source_provenance_json, created_at, updated_at
		 FROM skills
		 WHERE NOT EXISTS (
			 SELECT 1 FROM skill_profile_opt_outs WHERE profile_id = ? AND skill_id = skills.id
		 )
		 ORDER BY id ASC`,
		profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("list enabled skills: %w", err)
	}
	defer rows.Close()
	var out []Skill
	for rows.Next() {
		got, err := s.scanSkill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, got)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate enabled skills: %w", err)
	}
	if out == nil {
		out = []Skill{}
	}
	return out, nil
}

func (s *Service) EnabledSkillBundles(profileID string) ([]Bundle, error) {
	skills, err := s.EnabledSkills(profileID)
	if err != nil {
		return nil, err
	}
	bundles := make([]Bundle, 0, len(skills))
	for _, got := range skills {
		bundles = append(bundles, Bundle{
			ID:     got.ID,
			Name:   got.Name,
			Source: got.Source,
			Path:   got.BundlePath,
		})
	}
	return bundles, nil
}

func (s *Service) Delete(ctx context.Context, id string, forceDisable bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrNotFound
	}
	if _, err := s.Get(id); err != nil {
		return err
	}
	if !forceDisable {
		var enabledProfiles int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM runtime_profiles
			 WHERE NOT EXISTS (
				 SELECT 1 FROM skill_profile_opt_outs WHERE profile_id = runtime_profiles.id AND skill_id = ?
			 )`,
			id,
		).Scan(&enabledProfiles); err != nil {
			return fmt.Errorf("check skill enablement: %w", err)
		}
		if enabledProfiles > 0 {
			return ErrEnabled
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin skill delete: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM skill_profile_opt_outs WHERE skill_id = ?`, id); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete skill opt-outs: %w", err)
	}
	result, err := tx.Exec(`DELETE FROM skills WHERE id = ?`, id)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete skill: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete skill: %w", err)
	}
	if affected == 0 {
		_ = tx.Rollback()
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit skill delete: %w", err)
	}
	if err := os.RemoveAll(s.bundlePath(id)); err != nil {
		return fmt.Errorf("remove skill bundle: %w", err)
	}
	return nil
}

func (s *Service) BundlePath(id string) string {
	return s.bundlePath(strings.TrimSpace(id))
}

func (s *Service) writeStagingBundle(meta Metadata, files map[string]string) (string, error) {
	staging := filepath.Join(s.libraryRoot, ".staging", meta.ID+"-"+newID())
	for rel, content := range files {
		if err := ValidateRelativeBundlePath(rel); err != nil {
			_ = os.RemoveAll(staging)
			return "", err
		}
		path := filepath.Join(staging, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			_ = os.RemoveAll(staging)
			return "", fmt.Errorf("prepare skill file: %w", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			_ = os.RemoveAll(staging)
			return "", fmt.Errorf("write skill file %q: %w", rel, err)
		}
	}
	return staging, nil
}

func (s *Service) upsertMetadata(meta Metadata) (Skill, error) {
	sourceJSON, err := json.Marshal(meta.Source)
	if err != nil {
		return Skill{}, fmt.Errorf("encode skill source provenance: %w", err)
	}
	now := time.Now().UTC()
	_, err = s.db.Exec(
		`INSERT INTO skills (id, name, description, source_provenance_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			 name = excluded.name,
			 description = excluded.description,
			 source_provenance_json = excluded.source_provenance_json,
			 updated_at = excluded.updated_at`,
		meta.ID, meta.Name, meta.Description, string(sourceJSON),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Skill{}, fmt.Errorf("store skill metadata: %w", err)
	}
	return s.Get(meta.ID)
}

func (s *Service) scanSkill(row rowScanner) (Skill, error) {
	var got Skill
	var sourceJSON, createdAt, updatedAt string
	err := row.Scan(&got.ID, &got.Name, &got.Description, &sourceJSON, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Skill{}, ErrNotFound
	}
	if err != nil {
		return Skill{}, fmt.Errorf("scan skill: %w", err)
	}
	if err := json.Unmarshal([]byte(sourceJSON), &got.Source); err != nil {
		return Skill{}, fmt.Errorf("decode skill source provenance: %w", err)
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Skill{}, fmt.Errorf("parse skill created_at: %w", err)
	}
	updated, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Skill{}, fmt.Errorf("parse skill updated_at: %w", err)
	}
	got.CreatedAt = created
	got.UpdatedAt = updated
	got.BundlePath = s.bundlePath(got.ID)
	return got, nil
}

func (s *Service) bundlePath(id string) string {
	return filepath.Join(s.libraryRoot, "bundles", id)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func normalizeMetadata(meta Metadata) Metadata {
	meta.ID = strings.TrimSpace(meta.ID)
	meta.Name = strings.TrimSpace(meta.Name)
	meta.Description = strings.TrimSpace(meta.Description)
	meta.Source.Kind = strings.TrimSpace(meta.Source.Kind)
	meta.Source.Package = strings.TrimSpace(meta.Source.Package)
	meta.Source.Ref = strings.TrimSpace(meta.Source.Ref)
	meta.Source.SourceURL = strings.TrimSpace(meta.Source.SourceURL)
	return meta
}

func newID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}
