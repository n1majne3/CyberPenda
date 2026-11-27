package asset

import (
	"errors"
	"time"
)

type Type string

const (
	TypeRootDomain Type = "root_domain"
	TypeSubdomain  Type = "subdomain"
	TypeIP         Type = "ip"
	TypeService    Type = "service"
	TypeEndpoint   Type = "endpoint"
)

const (
	ScopeStatusInScope    = "in_scope"
	ScopeStatusOutOfScope = "out_of_scope"
	ScopeStatusExcluded   = "excluded"
)

var (
	ErrInvalidInput  = errors.New("invalid asset input")
	ErrNotFound      = errors.New("asset not found")
	ErrInvalidParent = errors.New("invalid asset parent")
	ErrTrustedOrigin = errors.New("asset Trusted Origin is not proven")
	ErrNodeNotFound  = errors.New("FGS node not found")
)

type Reference struct {
	Type  Type   `json:"type"`
	Value string `json:"value"`
}

type Input struct {
	Type       Type       `json:"type"`
	Value      string     `json:"value"`
	Parent     *Reference `json:"parent,omitempty"`
	ObservedAt string     `json:"observed_at,omitempty"`
}

type OriginKind string

const (
	OriginOperator OriginKind = "operator"
	OriginRuntime  OriginKind = "runtime"
)

type Origin struct {
	Kind           OriginKind
	ActorID        string
	TaskID         string
	ContinuationID string
}

type Asset struct {
	ID                    string    `json:"id"`
	ProjectID             string    `json:"project_id"`
	Type                  Type      `json:"type"`
	Value                 string    `json:"value"`
	NormalizedValue       string    `json:"normalized_value"`
	Scheme                string    `json:"scheme,omitempty"`
	Host                  string    `json:"host,omitempty"`
	Port                  int       `json:"port,omitempty"`
	Path                  string    `json:"path,omitempty"`
	ParentID              string    `json:"parent_id,omitempty"`
	ImportedFromProjectID string    `json:"imported_from_project_id,omitempty"`
	ImportedFromAssetID   string    `json:"imported_from_asset_id,omitempty"`
	FirstSeenAt           time.Time `json:"first_seen_at"`
	LastSeenAt            time.Time `json:"last_seen_at"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type Anchor struct {
	ProjectID      string    `json:"project_id"`
	AssetID        string    `json:"asset_id"`
	NodeKey        string    `json:"node_key"`
	NodeType       string    `json:"node_type"`
	TaskID         string    `json:"task_id,omitempty"`
	ContinuationID string    `json:"continuation_id,omitempty"`
	ActorID        string    `json:"actor_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type View struct {
	Asset
	ScopeStatus string   `json:"scope_status"`
	Covered     bool     `json:"covered"`
	GoalCount   int      `json:"goal_count"`
	StepCount   int      `json:"step_count"`
	FactCount   int      `json:"fact_count"`
	Anchors     []Anchor `json:"anchors"`
}

type BatchResult struct {
	Assets  []View `json:"assets"`
	Created int    `json:"created"`
	Updated int    `json:"updated"`
}

type Coverage struct {
	Schema                   string   `json:"schema"`
	ProjectID                string   `json:"project_id"`
	TotalAssets              int      `json:"total_assets"`
	InScopeAssets            int      `json:"in_scope_assets"`
	CoveredInScopeAssets     int      `json:"covered_in_scope_assets"`
	CoveragePercent          float64  `json:"coverage_percent"`
	OutOfScopeAssets         int      `json:"out_of_scope_assets"`
	ExcludedAssets           int      `json:"excluded_assets"`
	UncoveredInScopeAssetIDs []string `json:"uncovered_in_scope_asset_ids"`
}

const CoverageSchema = "project-asset-coverage/v1"

func validType(kind Type) bool {
	switch kind {
	case TypeRootDomain, TypeSubdomain, TypeIP, TypeService, TypeEndpoint:
		return true
	default:
		return false
	}
}
