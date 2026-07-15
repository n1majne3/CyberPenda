// Package blackboardv2contract exposes the frozen Blackboard v2 wire contract
// and its reusable conformance fixtures. It deliberately has no dependency on
// the v1 Blackboard runtime implementation.
package blackboardv2contract

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"

	"github.com/google/jsonschema-go/jsonschema"
)

//go:embed contractdata
var contractFiles embed.FS

type fixtureDefinition struct {
	Name   string          `json:"name"`
	Path   string          `json:"path,omitempty"`
	Schema string          `json:"schema"`
	Value  json.RawMessage `json:"value,omitempty"`
}

type manifest struct {
	Schema   string              `json:"schema"`
	Fixtures []fixtureDefinition `json:"fixtures"`
}

type relationshipTable struct {
	Schema      string                   `json:"schema"`
	RecordTypes []string                 `json:"record_types"`
	Relations   []relationshipDefinition `json:"relations"`
}

type relationshipDefinition struct {
	Relation       string   `json:"relation"`
	ReasonPolicy   string   `json:"reason_policy"`
	SelfLinkPolicy string   `json:"self_link_policy"`
	CyclePolicy    string   `json:"cycle_policy"`
	Matrix         [][]bool `json:"matrix"`
}

// RelationshipCase is one explicit source-type/target-type conformance case.
// Policies are repeated on every expanded case so table-driven consumers do
// not need a second source of relationship semantics.
type RelationshipCase struct {
	Relation       string
	From           string
	To             string
	Allowed        bool
	ReasonPolicy   string
	SelfLinkPolicy string
	CyclePolicy    string
}

type trustedToolsDocument struct {
	Schema         string        `json:"schema"`
	Authentication string        `json:"authentication"`
	Authority      string        `json:"authority"`
	Tools          []TrustedTool `json:"tools"`
}

// TrustedTool is one frozen Runtime tool name and its schema bindings.
type TrustedTool struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	Authentication string `json:"-"`
	Authority      string `json:"-"`
	InputSchema    string `json:"input_schema"`
	ResultSchema   string `json:"result_schema"`
}

// Baseline captures the observed pre-implementation suite separately from the
// first intentional v2 red test.
type Baseline struct {
	Schema              string            `json:"schema"`
	FixedPoint          string            `json:"fixed_point"`
	Command             string            `json:"command"`
	Passed              int               `json:"passed"`
	Failed              int               `json:"failed"`
	PreExistingFailures []BaselineFailure `json:"pre_existing_failures"`
	IntentionalV2Red    IntentionalRed    `json:"intentional_v2_red"`
}

// BaselineFailure names one failure observed before #98's first red test.
type BaselineFailure struct {
	Package string `json:"package"`
	Test    string `json:"test"`
	Failure string `json:"failure"`
}

// IntentionalRed records the first behavior test's expected red result.
type IntentionalRed struct {
	Command string `json:"command"`
	Test    string `json:"test"`
	Failure string `json:"failure"`
}

// Harness validates producer or adapter output against the same contract
// artifacts used by the golden corpus.
type Harness struct {
	manifest    manifest
	definitions map[string]fixtureDefinition
	schemaBytes []byte
	resolved    map[string]*jsonschema.Resolved
}

// NewHarness loads and validates the embedded contract manifest.
func NewHarness() (*Harness, error) {
	manifestBytes, err := contractFiles.ReadFile("contractdata/manifest.json")
	if err != nil {
		return nil, fmt.Errorf("read contract manifest: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return nil, fmt.Errorf("decode contract manifest: %w", err)
	}
	if m.Schema != "blackboard-v2-conformance-corpus/v1" {
		return nil, fmt.Errorf("unsupported contract manifest schema %q", m.Schema)
	}
	schemaBytes, err := contractFiles.ReadFile("contractdata/schemas/blackboard-v2.schema.json")
	if err != nil {
		return nil, fmt.Errorf("read Blackboard v2 schema: %w", err)
	}
	h := &Harness{
		manifest:    m,
		definitions: make(map[string]fixtureDefinition, len(m.Fixtures)),
		schemaBytes: schemaBytes,
		resolved:    make(map[string]*jsonschema.Resolved),
	}
	for _, definition := range m.Fixtures {
		if definition.Name == "" || definition.Schema == "" || (definition.Path == "" && len(definition.Value) == 0) {
			return nil, fmt.Errorf("contract manifest contains an incomplete fixture definition")
		}
		if definition.Path != "" && len(definition.Value) != 0 {
			return nil, fmt.Errorf("fixture %q has both path and inline value", definition.Name)
		}
		if _, duplicate := h.definitions[definition.Name]; duplicate {
			return nil, fmt.Errorf("contract manifest repeats fixture %q", definition.Name)
		}
		h.definitions[definition.Name] = definition
	}
	return h, nil
}

// Files returns a read-only view of the embedded machine-readable artifacts.
func (h *Harness) Files() fs.FS {
	return contractFiles
}

// OpenAPI returns the frozen OpenAPI 3.1 document for Blackboard HTTP v2.
func (h *Harness) OpenAPI() ([]byte, error) {
	raw, err := contractFiles.ReadFile("contractdata/openapi.json")
	if err != nil {
		return nil, fmt.Errorf("read Blackboard v2 OpenAPI: %w", err)
	}
	return bytes.TrimSpace(raw), nil
}

// Baseline returns the frozen test observation captured before #98's first
// intentional red test.
func (h *Harness) Baseline() (Baseline, error) {
	raw, err := contractFiles.ReadFile("contractdata/baseline.json")
	if err != nil {
		return Baseline{}, fmt.Errorf("read implementation baseline: %w", err)
	}
	var baseline Baseline
	if err := json.Unmarshal(raw, &baseline); err != nil {
		return Baseline{}, fmt.Errorf("decode implementation baseline: %w", err)
	}
	if baseline.Schema != "blackboard-v2-implementation-baseline/v1" {
		return Baseline{}, fmt.Errorf("unsupported implementation baseline schema %q", baseline.Schema)
	}
	if baseline.FixedPoint == "" || baseline.Command == "" || baseline.Failed != len(baseline.PreExistingFailures) {
		return Baseline{}, fmt.Errorf("implementation baseline is internally inconsistent")
	}
	if baseline.IntentionalV2Red.Command == "" || baseline.IntentionalV2Red.Test == "" || baseline.IntentionalV2Red.Failure == "" {
		return Baseline{}, fmt.Errorf("implementation baseline omitted the intentional v2 red")
	}
	return baseline, nil
}

// TrustedTools returns the six trusted Runtime tools in canonical order.
func (h *Harness) TrustedTools() ([]TrustedTool, error) {
	raw, err := contractFiles.ReadFile("contractdata/trusted-tools.json")
	if err != nil {
		return nil, fmt.Errorf("read trusted-tool contract: %w", err)
	}
	var document trustedToolsDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode trusted-tool contract: %w", err)
	}
	if document.Schema != "trusted-blackboard-tools/v2" {
		return nil, fmt.Errorf("unsupported trusted-tool schema %q", document.Schema)
	}
	if document.Authentication != "continuation_interface_grant" || document.Authority != "server_owned" {
		return nil, fmt.Errorf("trusted-tool contract has invalid authentication or authority")
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(h.schemaBytes, &schema); err != nil {
		return nil, fmt.Errorf("decode Blackboard v2 schema: %w", err)
	}
	seen := make(map[string]bool, len(document.Tools))
	tools := append([]TrustedTool(nil), document.Tools...)
	for index := range tools {
		tool := &tools[index]
		if tool.Name == "" || tool.Description == "" || tool.InputSchema == "" || tool.ResultSchema == "" {
			return nil, fmt.Errorf("trusted-tool contract contains an incomplete tool")
		}
		if seen[tool.Name] {
			return nil, fmt.Errorf("trusted-tool contract repeats %q", tool.Name)
		}
		seen[tool.Name] = true
		if schema.Defs[tool.InputSchema] == nil || schema.Defs[tool.ResultSchema] == nil {
			return nil, fmt.Errorf("trusted tool %q references an unknown schema", tool.Name)
		}
		tool.Authentication = document.Authentication
		tool.Authority = document.Authority
	}
	return tools, nil
}

// FixtureNames returns the corpus fixture names in lexical order.
func (h *Harness) FixtureNames() []string {
	names := make([]string, 0, len(h.definitions))
	for name := range h.definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Fixture returns the exact JSON value bytes for a named golden fixture.
func (h *Harness) Fixture(name string) ([]byte, error) {
	definition, ok := h.definitions[name]
	if !ok {
		return nil, fmt.Errorf("unknown Blackboard v2 fixture %q", name)
	}
	if len(definition.Value) != 0 {
		return compactJSON(definition.Value, name)
	}
	raw, err := contractFiles.ReadFile("contractdata/" + definition.Path)
	if err != nil {
		return nil, fmt.Errorf("read fixture %q: %w", name, err)
	}
	return compactJSON(raw, name)
}

func compactJSON(raw []byte, name string) ([]byte, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, bytes.TrimSpace(raw)); err != nil {
		return nil, fmt.Errorf("fixture %q is invalid JSON: %w", name, err)
	}
	return compact.Bytes(), nil
}

// ValidateFixture validates a named golden fixture through its declared schema.
func (h *Harness) ValidateFixture(name string) error {
	definition, ok := h.definitions[name]
	if !ok {
		return fmt.Errorf("unknown Blackboard v2 fixture %q", name)
	}
	raw, err := h.Fixture(name)
	if err != nil {
		return err
	}
	return h.Validate(definition.Schema, raw)
}

// Validate validates raw JSON against a named definition from the frozen
// Blackboard v2 schema bundle.
func (h *Harness) Validate(schemaName string, raw []byte) error {
	resolved, ok := h.resolved[schemaName]
	if !ok {
		var schema jsonschema.Schema
		if err := json.Unmarshal(h.schemaBytes, &schema); err != nil {
			return fmt.Errorf("decode Blackboard v2 schema: %w", err)
		}
		if _, exists := schema.Defs[schemaName]; !exists {
			return fmt.Errorf("unknown Blackboard v2 schema %q", schemaName)
		}
		schema.Ref = "#/$defs/" + schemaName
		var err error
		resolved, err = schema.Resolve(nil)
		if err != nil {
			return fmt.Errorf("resolve Blackboard v2 schema %q: %w", schemaName, err)
		}
		h.resolved[schemaName] = resolved
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	if err := resolved.Validate(value); err != nil {
		return fmt.Errorf("validate %s: %w", schemaName, err)
	}
	if err := validateUTF8ByteLimits(value, "$", ""); err != nil {
		return fmt.Errorf("validate %s: %w", schemaName, err)
	}
	return nil
}

func validateUTF8ByteLimits(value any, path, recordType string) error {
	switch typed := value.(type) {
	case map[string]any:
		if declaredType, ok := typed["type"].(string); ok {
			switch declaredType {
			case "entity", "objective", "attempt", "fact", "finding", "solution", "evidence":
				recordType = declaredType
			}
		}
		for name, child := range typed {
			childType := recordType
			switch name {
			case "entities":
				childType = "entity"
			case "objectives":
				childType = "objective"
			case "attempts":
				childType = "attempt"
			case "facts":
				childType = "fact"
			case "findings":
				childType = "finding"
			case "solutions":
				childType = "solution"
			case "evidence":
				childType = "evidence"
			}
			if text, ok := child.(string); ok {
				limit := 0
				switch name {
				case "objective", "summary":
					limit = 1024
				case "description":
					limit = 1024
					if recordType == "entity" {
						limit = 512
					}
				case "reason", "rationale", "explanation", "resolution_summary", "verification_summary", "kind", "name", "locator", "category", "title", "target", "credential_ref", "artifact_type", "media_type":
					limit = 512
				}
				if limit > 0 && len([]byte(text)) > limit {
					return fmt.Errorf("%s.%s is %d UTF-8 bytes, maximum %d", path, name, len([]byte(text)), limit)
				}
			}
			if err := validateUTF8ByteLimits(child, path+"."+name, childType); err != nil {
				return err
			}
		}
	case []any:
		if len(typed) == 4 {
			if relation, ok := typed[1].(string); ok && (relation == "supports" || relation == "contradicts" || relation == "depends_on") {
				if reason, ok := typed[3].(string); ok && len([]byte(reason)) > 512 {
					return fmt.Errorf("%s[3] is %d UTF-8 bytes, maximum 512", path, len([]byte(reason)))
				}
			}
		}
		for index, child := range typed {
			if err := validateUTF8ByteLimits(child, fmt.Sprintf("%s[%d]", path, index), recordType); err != nil {
				return err
			}
		}
	}
	return nil
}

// RelationshipCases expands the frozen 11-by-7-by-7 endpoint matrices into
// explicit allowed and rejected conformance cases.
func (h *Harness) RelationshipCases() ([]RelationshipCase, error) {
	raw, err := contractFiles.ReadFile("contractdata/relationships.json")
	if err != nil {
		return nil, fmt.Errorf("read relationship table: %w", err)
	}
	var table relationshipTable
	if err := json.Unmarshal(raw, &table); err != nil {
		return nil, fmt.Errorf("decode relationship table: %w", err)
	}
	if table.Schema != "blackboard-v2-relationship-matrix/v1" {
		return nil, fmt.Errorf("unsupported relationship table schema %q", table.Schema)
	}
	if len(table.RecordTypes) != 7 {
		return nil, fmt.Errorf("relationship table has %d record types, want 7", len(table.RecordTypes))
	}
	if len(table.Relations) != 11 {
		return nil, fmt.Errorf("relationship table has %d relations, want 11", len(table.Relations))
	}

	cases := make([]RelationshipCase, 0, len(table.Relations)*len(table.RecordTypes)*len(table.RecordTypes))
	seenRelations := make(map[string]bool, len(table.Relations))
	for _, relation := range table.Relations {
		if relation.Relation == "" || relation.ReasonPolicy == "" || relation.SelfLinkPolicy == "" || relation.CyclePolicy == "" {
			return nil, fmt.Errorf("relationship table contains an incomplete policy")
		}
		if seenRelations[relation.Relation] {
			return nil, fmt.Errorf("relationship table repeats %q", relation.Relation)
		}
		seenRelations[relation.Relation] = true
		if len(relation.Matrix) != len(table.RecordTypes) {
			return nil, fmt.Errorf("relationship %q has %d source rows, want %d", relation.Relation, len(relation.Matrix), len(table.RecordTypes))
		}
		for fromIndex, row := range relation.Matrix {
			if len(row) != len(table.RecordTypes) {
				return nil, fmt.Errorf("relationship %q source %q has %d target columns, want %d", relation.Relation, table.RecordTypes[fromIndex], len(row), len(table.RecordTypes))
			}
			for toIndex, allowed := range row {
				cases = append(cases, RelationshipCase{
					Relation:       relation.Relation,
					From:           table.RecordTypes[fromIndex],
					To:             table.RecordTypes[toIndex],
					Allowed:        allowed,
					ReasonPolicy:   relation.ReasonPolicy,
					SelfLinkPolicy: relation.SelfLinkPolicy,
					CyclePolicy:    relation.CyclePolicy,
				})
			}
		}
	}
	return cases, nil
}
