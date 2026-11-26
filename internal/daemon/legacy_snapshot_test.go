package daemon

import (
	"errors"
	"testing"

	"pentest/internal/modelprovider"
	"pentest/internal/runtimeconfig"
)

func TestLegacySnapshotContinuationRequiresModelProvider(t *testing.T) {
	server, err := NewServer(Config{DBPath: ":memory:", RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	prior := runtimeconfig.RuntimeConfigurationSnapshot{
		SnapshotVersion: runtimeconfig.SnapshotVersion,
		RuntimePluginID: "codex",
		RuntimeProfile:  &runtimeconfig.ProfileProvenance{ID: "legacy-profile", Name: "Legacy"},
		TurnSelection:   runtimeconfig.RuntimeTurnSelection{Model: "gpt-test"},
	}
	if _, err := server.cloneSnapshotForTurn(prior, "", prior.TurnSelection); !errors.Is(err, modelprovider.ErrMissingProvider) {
		t.Fatalf("legacy Snapshot continuation error=%v, want missing Model Provider", err)
	}
}
