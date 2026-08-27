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
// multi-agent tools. When enabled, Config Projection writes the Codex-native
// `[features] multi_agent` flag and `[agents]` caps into the task-local Codex
// config so a Work Runtime Turn can receive spawn tools. It stays off when
// unset, and it never creates a Harness-owned subagent scheduling surface.
type CodexMultiAgent struct {
	// Enabled turns the in-turn multi-agent tools on. Nil means off.
	Enabled *bool `json:"enabled,omitempty"`
	// MaxConcurrentThreadsPerSession caps concurrently open spawned agent
	// threads per session. Zero keeps the Codex default.
	MaxConcurrentThreadsPerSession int `json:"max_concurrent_threads_per_session,omitempty"`
	// MaxDepth caps nesting depth for V1 agent threads. Zero keeps the Codex
	// default.
	MaxDepth int `json:"max_depth,omitempty"`
}

// CodexMultiAgentEnabled reports whether the profile turns Codex in-turn
// multi-agent tools on. A missing control means off.
func CodexMultiAgentEnabled(profile Profile) bool {
	settings := profile.Fields.CodexMultiAgent
	return settings != nil && settings.Enabled != nil && *settings.Enabled
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
	if settings.Enabled == nil && settings.MaxConcurrentThreadsPerSession == 0 && settings.MaxDepth == 0 {
		return nil, nil
	}
	return settings, nil
}
