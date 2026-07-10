package blackboard

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

type propertyRule struct {
	required bool
	enums    map[string]bool
}

func enum(values ...string) map[string]bool {
	m := map[string]bool{}
	for _, v := range values {
		m[v] = true
	}
	return m
}

var nodeSchemas = map[NodeType]map[string]propertyRule{
	NodeTypeGoal:                 {"task_id": {required: true}, "text": {required: true}, "task_status": {required: true, enums: enum("pending", "running", "paused", "completed", "failed", "stopped", "interrupted")}},
	NodeTypeEntity:               {"kind": {required: true, enums: enum("network", "host", "ip_address", "domain", "service", "endpoint", "application", "identity", "credential", "data_store", "file", "binary", "function", "challenge_component")}, "name": {required: true}, "locator": {}, "description": {}, "scope_status": {required: true, enums: enum("in_scope", "out_of_scope", "unknown")}, "credential_ref": {}, "status": {enums: enum("active", "retired", "superseded")}},
	NodeTypeExplorationObjective: {"objective": {required: true}, "status": {enums: enum("open", "resolved", "abandoned", "superseded")}, "resolution_summary": {}, "resolved_at": {}},
	NodeTypeAttempt:              {"status": {enums: enum("open", "succeeded", "failed", "blocked", "inconclusive", "interrupted")}, "summary": {}, "ended_at": {}},
	NodeTypeObservation:          {"summary": {required: true}, "detail": {}, "observed_at": {}, "scope_status": {required: true, enums: enum("in_scope", "out_of_scope", "unknown")}, "status": {enums: enum("recorded", "superseded")}},
	NodeTypeHypothesis:           {"statement": {required: true}, "rationale": {}, "status": {enums: enum("open", "supported", "contradicted", "inconclusive", "superseded")}},
	NodeTypeProjectFact:          {"category": {required: true}, "summary": {required: true}, "body": {}, "confidence": {enums: enum("tentative", "confirmed", "deprecated")}, "scope_status": {required: true, enums: enum("in_scope", "out_of_scope", "unknown")}},
	NodeTypeFinding:              {"title": {required: true}, "description": {}, "status": {enums: enum("unconfirmed", "confirmed", "false_positive")}, "target": {}, "proof": {}, "impact": {}, "recommendation": {}, "cvss_version": {enums: enum("4.0", "3.1")}, "cvss_vector": {}},
	NodeTypeSolution:             {"kind": {required: true, enums: enum("flag", "answer", "procedure")}, "summary": {required: true}, "value": {}, "status": {enums: enum("candidate", "verified", "rejected", "superseded")}, "verification_summary": {}},
	NodeTypeEvidenceArtifact:     {"artifact_type": {required: true, enums: enum("http_exchange", "screenshot", "terminal_capture", "log", "pcap", "file", "binary", "source_code", "structured_data", "report", "other")}, "media_type": {}, "source_path": {}, "managed_path": {required: true}, "sha256": {}, "size_bytes": {}, "summary": {required: true}, "status": {enums: enum("available", "missing", "superseded")}, "captured_at": {}},
	NodeTypeProjectDirective:     {"directive": {required: true}, "rationale": {}, "status": {required: true, enums: enum("proposed", "active", "retired", "superseded")}},
}

func operationProperties(op Operation) map[string]any {
	if op.Create.PropertyMap != nil {
		return op.Create.PropertyMap
	}
	p := op.Create.Properties
	return map[string]any{"category": p.Category, "summary": p.Summary, "body": p.Body, "confidence": string(p.Confidence), "scope_status": string(p.ScopeStatus)}
}

func validateNodeProperties(t NodeType, props map[string]any) *ValidationError {
	schema, ok := nodeSchemas[t]
	if !ok {
		return validationError(ErrCodeUnknownNodeType, fmt.Sprintf("unknown node type %q", t), -1, "", "properties")
	}
	for k := range props {
		if _, ok := schema[k]; !ok {
			return validationError(ErrCodeUnknownProperty, fmt.Sprintf("unknown %s property %q", t, k), -1, "", "properties."+k)
		}
	}
	for k, r := range schema {
		v, exists := props[k]
		s, isString := v.(string)
		if r.required && (!exists || (isString && strings.TrimSpace(s) == "")) {
			return validationError(ErrCodeMissingProperty, fmt.Sprintf("%s.%s is required", t, k), -1, "", "properties."+k)
		}
		if exists && r.enums != nil && (!isString || (s != "" && !r.enums[s])) {
			return validationError(ErrCodeInvalidProperty, fmt.Sprintf("invalid %s.%s", t, k), -1, "", "properties."+k)
		}
	}
	if t == NodeTypeEntity {
		if e := validateEntity(props); e != nil {
			return e
		}
	}
	if t == NodeTypeEvidenceArtifact && props["status"] != "missing" && props["status"] != "superseded" {
		if s, _ := props["sha256"].(string); !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(s) {
			return validationError(ErrCodeInvalidProperty, "available evidence requires lowercase sha256", -1, "", "properties.sha256")
		}
	}
	if t == NodeTypeSolution {
		k, _ := props["kind"].(string)
		if k == "flag" || k == "answer" {
			if v, _ := props["value"].(string); strings.TrimSpace(v) == "" {
				return validationError(ErrCodeMissingProperty, "solution.value is required", -1, "", "properties.value")
			}
		}
	}
	return nil
}

func validateEntity(p map[string]any) *ValidationError {
	k, _ := p["kind"].(string)
	loc, _ := p["locator"].(string)
	cred, _ := p["credential_ref"].(string)
	required := map[string]bool{"network": true, "ip_address": true, "domain": true, "service": true, "endpoint": true, "identity": true, "file": true, "binary": true, "function": true}
	if required[k] && strings.TrimSpace(loc) == "" {
		return validationError(ErrCodeMissingProperty, "entity.locator is required for kind "+k, -1, "", "properties.locator")
	}
	if k == "credential" && strings.TrimSpace(cred) == "" {
		return validationError(ErrCodeMissingProperty, "credential_ref is required", -1, "", "properties.credential_ref")
	}
	if k != "credential" && cred != "" {
		return validationError(ErrCodeInvalidProperty, "credential_ref is credential-only", -1, "", "properties.credential_ref")
	}
	valid := true
	switch k {
	case "ip_address":
		valid = net.ParseIP(loc) != nil
	case "network":
		_, _, err := net.ParseCIDR(loc)
		valid = err == nil || regexp.MustCompile(`^[a-z0-9._:/-]+$`).MatchString(loc)
	case "domain":
		valid = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`).MatchString(loc)
	case "endpoint":
		u, err := url.Parse(loc)
		valid = err == nil && (u.IsAbs() || strings.HasPrefix(loc, "/") || strings.Contains(loc, "."))
	}
	if !valid {
		return validationError(ErrCodeInvalidProperty, "invalid locator for entity kind "+k, -1, "", "properties.locator")
	}
	return nil
}

var edgeEndpoints = map[EdgeType]func(NodeType, NodeType) bool{
	EdgeTypeAbout: func(a, b NodeType) bool { return b == NodeTypeEntity && a != NodeTypeEntity },
	EdgeTypePartOf: func(a, b NodeType) bool {
		return (a == NodeTypeEntity && b == NodeTypeEntity) || (a == NodeTypeExplorationObjective && b == NodeTypeGoal)
	},
	EdgeTypeTests: func(a, b NodeType) bool {
		return a == NodeTypeAttempt && (b == NodeTypeExplorationObjective || b == NodeTypeHypothesis || b == NodeTypeEntity)
	},
	EdgeTypeProduced: func(a, b NodeType) bool {
		return a == NodeTypeAttempt && oneOf(b, NodeTypeObservation, NodeTypeHypothesis, NodeTypeProjectFact, NodeTypeFinding, NodeTypeSolution, NodeTypeEvidenceArtifact)
	},
	EdgeTypeEvidences: func(a, b NodeType) bool {
		return a == NodeTypeEvidenceArtifact && oneOf(b, NodeTypeObservation, NodeTypeHypothesis, NodeTypeProjectFact, NodeTypeFinding, NodeTypeSolution)
	},
	EdgeTypeSupports: func(a, b NodeType) bool {
		return oneOf(a, NodeTypeObservation, NodeTypeHypothesis, NodeTypeProjectFact) && oneOf(b, NodeTypeHypothesis, NodeTypeProjectFact, NodeTypeFinding, NodeTypeSolution)
	},
	EdgeTypeContradicts: func(a, b NodeType) bool {
		return oneOf(a, NodeTypeObservation, NodeTypeHypothesis, NodeTypeProjectFact) && oneOf(b, NodeTypeObservation, NodeTypeHypothesis, NodeTypeProjectFact, NodeTypeFinding, NodeTypeSolution)
	},
	EdgeTypeDependsOn: func(a, b NodeType) bool { return a == NodeTypeExplorationObjective && b == a }, EdgeTypeBlocks: func(a, b NodeType) bool { return a == NodeTypeExplorationObjective && b == a },
	EdgeTypeSatisfies: func(a, b NodeType) bool {
		return oneOf(a, NodeTypeProjectFact, NodeTypeFinding, NodeTypeSolution) && oneOf(b, NodeTypeExplorationObjective, NodeTypeGoal)
	},
	EdgeTypeSupersedes: func(a, b NodeType) bool {
		return a == b && oneOf(a, NodeTypeEntity, NodeTypeExplorationObjective, NodeTypeObservation, NodeTypeHypothesis, NodeTypeProjectFact, NodeTypeFinding, NodeTypeSolution, NodeTypeEvidenceArtifact, NodeTypeProjectDirective)
	},
	EdgeTypeDerivedFrom: func(a, b NodeType) bool { return a != NodeTypeGoal && a != NodeTypeAttempt }, EdgeTypeLeadsTo: func(a, b NodeType) bool {
		return oneOf(a, NodeTypeExplorationObjective, NodeTypeAttempt, NodeTypeObservation, NodeTypeHypothesis, NodeTypeProjectFact, NodeTypeFinding) && oneOf(b, NodeTypeExplorationObjective, NodeTypeAttempt, NodeTypeObservation, NodeTypeHypothesis, NodeTypeProjectFact, NodeTypeFinding, NodeTypeSolution)
	},
}

func oneOf(v NodeType, xs ...NodeType) bool {
	for _, x := range xs {
		if v == x {
			return true
		}
	}
	return false
}

var entityParentKinds = map[string]map[string]bool{
	"host": enum("network"), "network": enum("network"), "ip_address": enum("host", "network"), "domain": enum("host", "application"), "service": enum("host", "ip_address"), "endpoint": enum("service", "application"), "application": enum("host", "service"), "identity": enum("application", "service"), "credential": enum("identity", "application", "service"), "data_store": enum("application", "service", "host"), "file": enum("host", "application", "binary", "challenge_component"), "binary": enum("host", "application", "challenge_component"), "function": enum("binary", "application", "challenge_component"), "challenge_component": enum("challenge_component"),
}

func validateEntityPartOfKinds(a, b string) *ValidationError {
	if !entityParentKinds[a][b] {
		return validationError(ErrCodeEdgeEndpointType, fmt.Sprintf("entity part_of cannot connect %s to %s", a, b), -1, "", "from")
	}
	return nil
}
