package asset

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

type normalizedAsset struct {
	Type            Type
	Value           string
	NormalizedValue string
	Scheme          string
	Host            string
	Port            int
	Path            string
}

func normalizeReference(ref Reference) (normalizedAsset, error) {
	if !validType(ref.Type) {
		return normalizedAsset{}, ErrInvalidInput
	}
	switch ref.Type {
	case TypeRootDomain:
		host, err := normalizeDomain(ref.Value, true)
		if err != nil {
			return normalizedAsset{}, err
		}
		return normalizedAsset{Type: ref.Type, Value: host, NormalizedValue: host, Host: host}, nil
	case TypeSubdomain:
		host, err := normalizeDomain(ref.Value, false)
		if err != nil {
			return normalizedAsset{}, err
		}
		return normalizedAsset{Type: ref.Type, Value: host, NormalizedValue: host, Host: host}, nil
	case TypeIP:
		ip, err := netip.ParseAddr(strings.TrimSpace(ref.Value))
		if err != nil {
			return normalizedAsset{}, ErrInvalidInput
		}
		host := ip.String()
		return normalizedAsset{Type: ref.Type, Value: host, NormalizedValue: host, Host: host}, nil
	case TypeService:
		return normalizeService(ref.Value)
	case TypeEndpoint:
		return normalizeEndpoint(ref.Value)
	default:
		return normalizedAsset{}, ErrInvalidInput
	}
}

func normalizeDomain(value string, root bool) (string, error) {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if host == "" || len(host) > 253 || strings.ContainsAny(host, " \t\r\n/:@") {
		return "", ErrInvalidInput
	}
	labels := strings.Split(host, ".")
	if root && len(labels) < 2 {
		return "", ErrInvalidInput
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", ErrInvalidInput
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return "", ErrInvalidInput
		}
	}
	return host, nil
}

func normalizeHost(value string) (string, error) {
	value = strings.TrimSpace(value)
	if ip, err := netip.ParseAddr(value); err == nil {
		return ip.String(), nil
	}
	return normalizeDomain(value, false)
}

func hostPortValue(host string, port int) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + strconv.Itoa(port)
	}
	return host + ":" + strconv.Itoa(port)
}

func validPort(port int) bool {
	return port >= 1 && port <= 65535
}

func normalizeService(value string) (normalizedAsset, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return normalizedAsset{}, ErrInvalidInput
	}
	scheme := "tcp"
	hostPort := raw
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return normalizedAsset{}, ErrInvalidInput
		}
		scheme = strings.ToLower(parsed.Scheme)
		if !validScheme(scheme) {
			return normalizedAsset{}, ErrInvalidInput
		}
		hostPort = parsed.Host
	}
	host, portText, err := net.SplitHostPort(hostPort)
	if err != nil {
		index := strings.LastIndex(hostPort, ":")
		if index <= 0 || index == len(hostPort)-1 || strings.Count(hostPort, ":") != 1 {
			return normalizedAsset{}, ErrInvalidInput
		}
		host, portText = hostPort[:index], hostPort[index+1:]
	}
	port, err := strconv.Atoi(portText)
	if err != nil || !validPort(port) {
		return normalizedAsset{}, ErrInvalidInput
	}
	host, err = normalizeHost(host)
	if err != nil {
		return normalizedAsset{}, err
	}
	normalized := scheme + "://" + hostPortValue(host, port)
	return normalizedAsset{Type: TypeService, Value: normalized, NormalizedValue: normalized, Scheme: scheme, Host: host, Port: port}, nil
}

func validScheme(scheme string) bool {
	if scheme == "" {
		return false
	}
	for i, r := range scheme {
		if (r >= 'a' && r <= 'z') || (i > 0 && ((r >= '0' && r <= '9') || r == '+' || r == '-' || r == '.')) {
			continue
		}
		return false
	}
	return true
}

func normalizeEndpoint(value string) (normalizedAsset, error) {
	raw := strings.TrimSpace(value)
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return normalizedAsset{}, ErrInvalidInput
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return normalizedAsset{}, ErrInvalidInput
	}
	host, err := normalizeHost(parsed.Hostname())
	if err != nil {
		return normalizedAsset{}, err
	}
	port := 0
	if text := parsed.Port(); text != "" {
		port, err = strconv.Atoi(text)
		if err != nil || !validPort(port) {
			return normalizedAsset{}, ErrInvalidInput
		}
	}
	defaultPort := 80
	if scheme == "https" {
		defaultPort = 443
	}
	if port == 0 {
		port = defaultPort
	}
	decodedPath := parsed.Path
	if decodedPath == "" {
		decodedPath = "/"
	}
	path := canonicalEscapedPath(parsed.EscapedPath())
	query := parsed.Query()
	canonicalHost := host
	if strings.Contains(host, ":") {
		canonicalHost = "[" + host + "]"
	}
	if port != defaultPort {
		canonicalHost = net.JoinHostPort(strings.Trim(canonicalHost, "[]"), strconv.Itoa(port))
		if strings.Contains(host, ":") {
			canonicalHost = "[" + host + "]:" + strconv.Itoa(port)
		}
	}
	canonical := url.URL{Scheme: scheme, Host: canonicalHost, Path: decodedPath, RawPath: path, RawQuery: query.Encode()}
	normalized := canonical.String()
	return normalizedAsset{Type: TypeEndpoint, Value: normalized, NormalizedValue: normalized, Scheme: scheme, Host: host, Port: port, Path: path}, nil
}

func canonicalEscapedPath(path string) string {
	if path == "" {
		return "/"
	}
	var out strings.Builder
	out.Grow(len(path))
	for index := 0; index < len(path); index++ {
		if path[index] != '%' || index+2 >= len(path) {
			out.WriteByte(path[index])
			continue
		}
		high, low := path[index+1], path[index+2]
		if !isHexDigit(high) || !isHexDigit(low) {
			out.WriteByte(path[index])
			continue
		}
		value := hexValue(high)<<4 | hexValue(low)
		if isUnreservedByte(value) {
			out.WriteByte(value)
		} else {
			out.WriteByte('%')
			out.WriteByte(toUpperHex(high))
			out.WriteByte(toUpperHex(low))
		}
		index += 2
	}
	return out.String()
}

func isHexDigit(value byte) bool {
	return (value >= '0' && value <= '9') || (value >= 'a' && value <= 'f') || (value >= 'A' && value <= 'F')
}

func hexValue(value byte) byte {
	switch {
	case value >= '0' && value <= '9':
		return value - '0'
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10
	default:
		return value - 'A' + 10
	}
}

func toUpperHex(value byte) byte {
	if value >= 'a' && value <= 'f' {
		return value - 'a' + 'A'
	}
	return value
}

func isUnreservedByte(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') || value == '-' || value == '.' || value == '_' || value == '~'
}

func parentRank(kind Type) int {
	switch kind {
	case TypeRootDomain:
		return 1
	case TypeSubdomain, TypeIP:
		return 2
	case TypeService:
		return 3
	case TypeEndpoint:
		return 4
	default:
		return 0
	}
}

func compatibleParent(child, parent Type) bool {
	if parentRank(parent) > parentRank(child) {
		return false
	}
	if parentRank(parent) == parentRank(child) && !(child == TypeSubdomain && parent == TypeSubdomain) {
		return false
	}
	switch child {
	case TypeSubdomain:
		return parent == TypeRootDomain || parent == TypeSubdomain
	case TypeService:
		return parent == TypeRootDomain || parent == TypeSubdomain || parent == TypeIP
	case TypeEndpoint:
		return parent == TypeService || parent == TypeSubdomain || parent == TypeRootDomain || parent == TypeIP
	default:
		return false
	}
}

func endpointFromScopeURL(value string) (normalizedAsset, error) {
	return normalizeEndpoint(value)
}

func assetSummary(kind Type, value string) string {
	return fmt.Sprintf("%s:%s", kind, value)
}
