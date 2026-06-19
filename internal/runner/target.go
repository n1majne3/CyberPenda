package runner

import (
	"net"
	"net/url"
	"strings"
)

const sandboxHostAlias = "host.docker.internal"

var loopbackHosts = map[string]bool{
	"127.0.0.1": true,
	"localhost": true,
}

// RewriteLoopbackTargets rewrites loopback hosts (127.0.0.1, localhost) to
// host.docker.internal when launching into a sandbox, so a runtime running
// inside the container can reach services on the user's host. It is a no-op
// when sandbox is false. Parsing failures leave the input untouched.
func RewriteLoopbackTargets(in string, sandbox bool) string {
	if !sandbox || strings.TrimSpace(in) == "" {
		return in
	}
	rewritten := rewriteURLs(in)
	rewritten = rewriteBareHostTokens(rewritten)
	return rewritten
}

// rewriteURLs replaces loopback hosts inside http:// and https:// URLs while
// preserving scheme, port, path, query, and fragment. Multiple URLs per string
// are supported. A URL is only considered up to the next whitespace boundary so
// trailing prose is not swallowed into url.Parse.
func rewriteURLs(in string) string {
	var b strings.Builder
	b.Grow(len(in))
	i := 0
	for i < len(in) {
		scheme := matchScheme(in, i)
		if scheme == "" {
			b.WriteByte(in[i])
			i++
			continue
		}
		run := urlRun(in, i)
		parsed, err := url.Parse(run)
		if err == nil && parsed.Host != "" && loopbackHosts[parsed.Hostname()] {
			port := parsed.Port()
			parsed.Host = sandboxHostAlias
			if port != "" {
				parsed.Host = net.JoinHostPort(sandboxHostAlias, port)
			}
			b.WriteString(parsed.String())
		} else {
			b.WriteString(run)
		}
		i += len(run)
	}
	return b.String()
}

// matchScheme returns the http:// or https:// scheme starting at index i, or ""
// if the input does not start with one there.
func matchScheme(in string, i int) string {
	for _, scheme := range []string{"http://", "https://"} {
		if strings.HasPrefix(in[i:], scheme) {
			return scheme
		}
	}
	return ""
}

// urlRun returns the substring of in starting at start that looks like a URL,
// up to the first whitespace boundary. This is a cheap heuristic that avoids
// swallowing trailing prose into url.Parse.
func urlRun(in string, start int) string {
	end := start
	for end < len(in) && !isURLBoundary(in[end]) {
		end++
	}
	return in[start:end]
}

func isURLBoundary(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// rewriteBareHostTokens replaces bare loopback host[:port] tokens that are not
// part of a URL, e.g. "127.0.0.1:3000" or a standalone "localhost". It avoids
// rewriting substrings like "localhost-evil.com" by requiring word boundaries.
func rewriteBareHostTokens(in string) string {
	out := in
	for host := range loopbackHosts {
		out = replaceWord(out, host, sandboxHostAlias)
	}
	return out
}

// replaceWord replaces whole-word occurrences of target in s with repl. A match
// requires that the characters immediately before and after it are not
// hostname-continuation characters (letter, digit, '.', '-').
func replaceWord(s, target, repl string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		idx := strings.Index(s[i:], target)
		if idx < 0 {
			b.WriteString(s[i:])
			break
		}
		absIdx := i + idx
		beforeOK := absIdx == 0 || !isHostChar(s[absIdx-1])
		afterIdx := absIdx + len(target)
		afterOK := afterIdx >= len(s) || !isHostChar(s[afterIdx])
		if beforeOK && afterOK {
			b.WriteString(s[i:absIdx])
			b.WriteString(repl)
			i = afterIdx
		} else {
			b.WriteString(s[i : absIdx+1])
			i = absIdx + 1
		}
	}
	return b.String()
}

func isHostChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '.' || b == '-'
}
