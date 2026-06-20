// Package skill owns global, runtime-agnostic Skill bundles.
package skill

import (
	"errors"
	"time"
)

type Metadata struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Source      SourceProvenance `json:"source_provenance,omitempty"`
}

type SourceProvenance struct {
	Kind           string     `json:"kind,omitempty"`
	Package        string     `json:"package,omitempty"`
	Ref            string     `json:"ref,omitempty"`
	SourceURL      string     `json:"source_url,omitempty"`
	LastImportedAt *time.Time `json:"last_imported_at,omitempty"`
	LocalModified  bool       `json:"local_modified,omitempty"`
}

type Skill struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Source      SourceProvenance `json:"source_provenance,omitempty"`
	BundlePath  string           `json:"-"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type Bundle struct {
	ID     string
	Name   string
	Source SourceProvenance
	Path   string
}

var (
	ErrInvalidSkill = errors.New("invalid skill")
	ErrNotFound     = errors.New("skill not found")
	ErrEnabled      = errors.New("skill is enabled")
)
