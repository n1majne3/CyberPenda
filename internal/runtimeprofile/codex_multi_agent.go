package runtimeprofile

import (
	"errors"
	"fmt"
)

// ErrInvalidCodexMultiAgent reports Codex multi-agent settings that Codex
// cannot express, such as negative agent caps.
var ErrInvalidCodexMultiAgent = errors.New("invalid codex multi-agent settings")

func errInvalidCodexMultiAgent(detail string) error {
	return fmt.Errorf("%w: %s", ErrInvalidCodexMultiAgent, detail)
}

// CodexMultiAgent is the Codex-only structured control for in-turn
// multi-agent tools. It is tri-state: nil inherits Codex's own feature
// default (Config Projection writes no multi-agent keys), Enabled true
// projects the Codex-native `[features] multi_agent` flag and `[agents]`
// caps so a Work Runtime Turn can receive spawn tools, and Enabled false
// projects the off keys. It never creates a Harness-owned subagent
// scheduling surface.
type CodexMultiAgent struct {
	// Enabled carries the explicit on or off choice. Nil means inherit.
	Enabled *bool `json:"enabled,omitempty"`
	// MaxConcurrentThreadsPerSession caps concurrently open spawned agent
	// threads per session. Zero keeps the Codex default.
	MaxConcurrentThreadsPerSession int `json:"max_concurrent_threads_per_session,omitempty"`
	// MaxDepth caps nesting depth for V1 agent threads. Zero keeps the Codex
	// default.
	MaxDepth int `json:"max_depth,omitempty"`
}

// normalizeCodexMultiAgent validates Codex multi-agent settings and collapses
// an all-empty control to nil so unset storage stays unset. The control is
// Codex-only: storing it on another runtime family is rejected, mirroring the
// provider-scoped Custom Args conflict rules.
func normalizeCodexMultiAgent(provider Provider, settings *CodexMultiAgent) (*CodexMultiAgent, error) {
	if settings == nil {
		return nil, nil
	}
	if provider != ProviderCodex {
		return nil, errInvalidCodexMultiAgent("codex_multi_agent requires the codex provider")
	}
	if settings.MaxConcurrentThreadsPerSession < 0 {
		return nil, errInvalidCodexMultiAgent("max_concurrent_threads_per_session must not be negative")
	}
	if settings.MaxDepth < 0 {
		return nil, errInvalidCodexMultiAgent("max_depth must not be negative")
	}
	// Caps project only in the on state; requiring the explicit choice keeps
	// a stored cap from sitting inert under the inherit default.
	if (settings.MaxConcurrentThreadsPerSession > 0 || settings.MaxDepth > 0) &&
		(settings.Enabled == nil || !*settings.Enabled) {
		return nil, errInvalidCodexMultiAgent("agent caps require the multi-agent control to be enabled")
	}
	if settings.Enabled == nil && settings.MaxConcurrentThreadsPerSession == 0 && settings.MaxDepth == 0 {
		return nil, nil
	}
	return settings, nil
}
