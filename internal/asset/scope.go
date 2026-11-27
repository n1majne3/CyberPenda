package asset

import (
	"net/netip"
	"strconv"
	"strings"

	"pentest/internal/project"
)

func ScopeStatusFor(item Asset, scope project.Scope) string {
	if matchesExcluded(item, scope.Excluded) {
		return ScopeStatusExcluded
	}
	if matchesScope(item, scope) {
		return ScopeStatusInScope
	}
	return ScopeStatusOutOfScope
}

func matchesScope(item Asset, scope project.Scope) bool {
	switch item.Type {
	case TypeRootDomain, TypeSubdomain:
		return hostMatchesScopeDomains(item.Host, scope.Domains)
	case TypeIP:
		return ipMatchesScope(item.Host, scope)
	case TypeService:
		if !portAllowed(item.Port, scope.Ports) {
			return false
		}
		return hostMatchesNetworkScope(item.Host, scope) || serviceMatchesScopeURLs(item, scope.URLs)
	case TypeEndpoint:
		if !portAllowed(item.Port, scope.Ports) {
			return false
		}
		return hostMatchesNetworkScope(item.Host, scope) || endpointMatchesScopeURLs(item, scope.URLs)
	default:
		return false
	}
}

func matchesExcluded(item Asset, excluded []string) bool {
	for _, raw := range excluded {
		rule := strings.TrimSpace(raw)
		if rule == "" {
			continue
		}
		if endpoint, err := endpointFromScopeURL(rule); err == nil {
			if item.Type == TypeEndpoint && endpointURLPrefixMatch(item, endpoint) {
				return true
			}
			if endpoint.Path != "/" {
				continue
			}
			if item.Type == TypeService && sameSchemeHostPort(item, endpoint) {
				return true
			}
			if (item.Type == TypeRootDomain || item.Type == TypeSubdomain || item.Type == TypeIP) && hostRuleMatch(item.Host, endpoint.Host) {
				return true
			}
			continue
		}
		if host, err := normalizeHost(rule); err == nil {
			if hostRuleMatch(item.Host, host) {
				return true
			}
			continue
		}
		if prefix, err := netip.ParsePrefix(rule); err == nil && item.Host != "" {
			if ip, ipErr := netip.ParseAddr(item.Host); ipErr == nil && prefix.Contains(ip) {
				return true
			}
		}
	}
	return false
}

func hostMatchesScopeDomains(host string, domains []string) bool {
	for _, raw := range domains {
		rule, err := normalizeHost(raw)
		if err == nil && hostRuleMatch(host, rule) {
			return true
		}
	}
	return false
}

func hostMatchesNetworkScope(host string, scope project.Scope) bool {
	if hostMatchesScopeDomains(host, scope.Domains) {
		return true
	}
	return ipMatchesScope(host, scope)
}

func ipMatchesScope(host string, scope project.Scope) bool {
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	for _, raw := range scope.IPs {
		rule, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err == nil && rule == ip {
			return true
		}
	}
	for _, raw := range scope.CIDRs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err == nil && prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func hostRuleMatch(host, rule string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	rule = strings.ToLower(strings.TrimSpace(rule))
	return host != "" && rule != "" && (host == rule || strings.HasSuffix(host, "."+rule))
}

func portAllowed(port int, rules []string) bool {
	if len(rules) == 0 {
		return true
	}
	for _, raw := range rules {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if strings.Contains(part, "-") {
				bounds := strings.SplitN(part, "-", 2)
				low, lowErr := strconv.Atoi(strings.TrimSpace(bounds[0]))
				high, highErr := strconv.Atoi(strings.TrimSpace(bounds[1]))
				if lowErr == nil && highErr == nil && low <= port && port <= high {
					return true
				}
				continue
			}
			exact, err := strconv.Atoi(part)
			if err == nil && exact == port {
				return true
			}
		}
	}
	return false
}

func serviceMatchesScopeURLs(item Asset, urls []string) bool {
	for _, raw := range urls {
		endpoint, err := endpointFromScopeURL(raw)
		if err == nil && sameSchemeHostPort(item, endpoint) {
			return true
		}
	}
	return false
}

func endpointMatchesScopeURLs(item Asset, urls []string) bool {
	for _, raw := range urls {
		endpoint, err := endpointFromScopeURL(raw)
		if err == nil && endpointURLPrefixMatch(item, endpoint) {
			return true
		}
	}
	return false
}

func sameSchemeHostPort(item Asset, endpoint normalizedAsset) bool {
	return item.Scheme == endpoint.Scheme && item.Host == endpoint.Host && item.Port == endpoint.Port
}

func endpointURLPrefixMatch(item Asset, endpoint normalizedAsset) bool {
	if !sameSchemeHostPort(item, endpoint) {
		return false
	}
	base := endpoint.Path
	if base == "" || base == "/" {
		return true
	}
	base = strings.TrimSuffix(base, "/")
	return item.Path == base || strings.HasPrefix(item.Path, base+"/")
}
