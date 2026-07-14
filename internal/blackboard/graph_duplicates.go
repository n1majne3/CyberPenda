package blackboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

type duplicateRecord struct {
	id, stableKey, fingerprint string
	nodeType                   NodeType
}

// DuplicateCandidates returns deterministic advisory fingerprint collisions.
// It never writes aliases or merge state; identity consolidation remains an
// explicit merge_nodes mutation.
func (s *GraphService) DuplicateCandidates(ctx context.Context, projectID string) ([]DuplicateCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT h.node_id,h.node_type,n.original_stable_key,v.properties_json FROM blackboard_node_heads h JOIN blackboard_nodes n ON n.project_id=h.project_id AND n.id=h.node_id JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version WHERE h.project_id=? AND h.disposition='main'`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[string][]duplicateRecord{}
	for rows.Next() {
		var record duplicateRecord
		var propertiesJSON string
		if err := rows.Scan(&record.id, &record.nodeType, &record.stableKey, &propertiesJSON); err != nil {
			return nil, err
		}
		var properties map[string]any
		if err := json.Unmarshal([]byte(propertiesJSON), &properties); err != nil {
			return nil, err
		}
		record.fingerprint = duplicateFingerprint(record.nodeType, properties)
		if record.fingerprint != "" {
			key := string(record.nodeType) + "\x00" + record.fingerprint
			groups[key] = append(groups[key], record)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var candidates []DuplicateCandidate
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool {
			if group[i].stableKey != group[j].stableKey {
				return group[i].stableKey < group[j].stableKey
			}
			return group[i].id < group[j].id
		})
		ids := make([]string, len(group))
		for i := range group {
			ids[i] = group[i].id
		}
		candidates = append(candidates, DuplicateCandidate{NodeType: group[0].nodeType, Fingerprint: group[0].fingerprint, NodeIDs: ids})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].NodeType != candidates[j].NodeType {
			return nodeTypeOrdinal(candidates[i].NodeType) < nodeTypeOrdinal(candidates[j].NodeType)
		}
		return candidates[i].Fingerprint < candidates[j].Fingerprint
	})
	return candidates, nil
}

func duplicateFingerprint(nodeType NodeType, properties map[string]any) string {
	value := func(key string) string { text, _ := properties[key].(string); return text }
	var parts []string
	switch nodeType {
	case NodeTypeEvidenceArtifact:
		if value("sha256") == "" {
			return ""
		}
		parts = []string{value("sha256")}
	case NodeTypeEntity:
		if value("locator") == "" {
			return ""
		}
		parts = []string{value("kind"), normalizeEntityLocator(value("kind"), value("locator"))}
	case NodeTypeSolution:
		if value("value") == "" {
			return ""
		}
		parts = []string{value("kind"), value("value")}
	case NodeTypeProjectFact:
		parts = []string{value("category"), normalizeDuplicateText(value("summary"))}
	case NodeTypeFinding:
		if value("target") == "" || value("title") == "" {
			return ""
		}
		parts = []string{normalizeDuplicateText(value("target")), normalizeDuplicateText(value("title"))}
	default:
		return ""
	}
	for _, part := range parts {
		if part == "" {
			return ""
		}
	}
	hash := sha256.Sum256([]byte("CyberPenda.Blackboard.Duplicate.v1\x00" + string(nodeType) + "\x00" + strings.Join(parts, "\x00")))
	return hex.EncodeToString(hash[:])
}

func normalizeDuplicateText(value string) string {
	value = strings.ToLower(norm.NFKC.String(value))
	var out strings.Builder
	space := true
	for _, r := range value {
		if unicode.IsPunct(r) || unicode.IsSpace(r) || unicode.Is(unicode.Z, r) {
			if !space {
				out.WriteByte(' ')
				space = true
			}
			continue
		}
		out.WriteRune(r)
		space = false
	}
	return strings.TrimSpace(out.String())
}

func normalizeEntityLocator(kind, locator string) string {
	locator = strings.TrimSpace(norm.NFKC.String(locator))
	switch kind {
	case "ip_address":
		if address, err := netip.ParseAddr(locator); err == nil {
			return address.Unmap().String()
		}
	case "network":
		if prefix, err := netip.ParsePrefix(locator); err == nil {
			return prefix.Masked().String()
		}
	case "host", "domain":
		return strings.TrimSuffix(strings.ToLower(locator), ".")
	case "endpoint":
		if parsed, err := url.Parse(locator); err == nil && parsed.IsAbs() {
			parsed.Scheme = strings.ToLower(parsed.Scheme)
			parsed.Host = strings.ToLower(parsed.Host)
			return parsed.String()
		}
	default:
		return locator
	}
	return locator
}
