package runtimeoutput

import (
	"regexp"
	"strings"
)

// ansiPattern matches ANSI escape sequences (CSI such as SGR color codes and
// OSC hyperlinks) that model providers sometimes embed in reasoning text.
var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;:?!]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(\x07|\x1b\\\\)")

// stripANSI removes ANSI escape sequences from provider-supplied text.
func stripANSI(text string) string {
	if !strings.ContainsRune(text, '\x1b') {
		return text
	}
	return ansiPattern.ReplaceAllString(text, "")
}
