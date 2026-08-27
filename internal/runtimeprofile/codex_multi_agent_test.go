package runtimeprofile_test

import (
	"strings"
	"testing"

	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

func newMultiAgentTestService(t *testing.T) *runtimeprofile.Service {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/pentest.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return runtimeprofile.NewService(db)
}

func TestCodexMultiAgentDefaultsToInheritWithoutRewritingExistingProfiles(t *testing.T) {
	svc := newMultiAgentTestService(t)

	created, err := svc.Create("codex-inherit", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Fields.CodexMultiAgent != nil {
		t.Fatalf("expected no codex_multi_agent storage, got %#v", created.Fields.CodexMultiAgent)
	}

	loaded, err := svc.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.Fields.CodexMultiAgent != nil {
		t.Fatalf("expected stored profile to keep the inherit default")
	}
}

func TestCreatePersistsCodexMultiAgent(t *testing.T) {
	svc := newMultiAgentTestService(t)
	enabled := true

	created, err := svc.Create("codex-on", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		CodexMultiAgent: &runtimeprofile.CodexMultiAgent{
			Enabled:                        &enabled,
			MaxConcurrentThreadsPerSession: 4,
			MaxDepth:                       2,
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stored := created.Fields.CodexMultiAgent
	if stored == nil || stored.Enabled == nil || !*stored.Enabled {
		t.Fatalf("expected explicit on, got %#v", stored)
	}
	if stored.MaxConcurrentThreadsPerSession != 4 || stored.MaxDepth != 2 {
		t.Fatalf("stored caps = %#v", stored)
	}

	loaded, err := svc.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := loaded.Fields.CodexMultiAgent; got == nil || got.Enabled == nil || !*got.Enabled ||
		got.MaxConcurrentThreadsPerSession != 4 || got.MaxDepth != 2 {
		t.Fatalf("persisted control = %#v", got)
	}
}

func TestCodexMultiAgentExplicitFalseStaysStored(t *testing.T) {
	svc := newMultiAgentTestService(t)
	disabled := false

	created, err := svc.Create("codex-explicit-off", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		CodexMultiAgent: &runtimeprofile.CodexMultiAgent{Enabled: &disabled},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stored := created.Fields.CodexMultiAgent
	if stored == nil || stored.Enabled == nil || *stored.Enabled {
		t.Fatalf("explicit false must stay stored as off, got %#v", stored)
	}
}

func TestNormalizeRejectsNegativeCodexMultiAgentCaps(t *testing.T) {
	svc := newMultiAgentTestService(t)
	enabled := true

	if _, err := svc.Create("bad-threads", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		CodexMultiAgent: &runtimeprofile.CodexMultiAgent{Enabled: &enabled, MaxConcurrentThreadsPerSession: -1},
	}); err == nil {
		t.Fatalf("expected negative max threads to be rejected")
	}
	if _, err := svc.Create("bad-depth", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		CodexMultiAgent: &runtimeprofile.CodexMultiAgent{Enabled: &enabled, MaxDepth: -3},
	}); err == nil {
		t.Fatalf("expected negative max depth to be rejected")
	}
}

func TestNormalizeRejectsCapsWithoutEnabledChoice(t *testing.T) {
	svc := newMultiAgentTestService(t)
	disabled := false

	// Caps project only in the on state; storing them under inherit or off
	// would leave them silently inert.
	if _, err := svc.Create("caps-inherit", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		CodexMultiAgent: &runtimeprofile.CodexMultiAgent{MaxConcurrentThreadsPerSession: 4},
	}); err == nil {
		t.Fatalf("expected caps without an enabled choice to be rejected")
	}
	if _, err := svc.Create("caps-off", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		CodexMultiAgent: &runtimeprofile.CodexMultiAgent{Enabled: &disabled, MaxDepth: 2},
	}); err == nil {
		t.Fatalf("expected caps under explicit off to be rejected")
	}
}

func TestGeneratedConfigPreviewsCodexMultiAgent(t *testing.T) {
	enabled := true
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderCodex,
		Fields: runtimeprofile.Fields{
			CodexMultiAgent: &runtimeprofile.CodexMultiAgent{
				Enabled:                        &enabled,
				MaxConcurrentThreadsPerSession: 4,
				MaxDepth:                       2,
			},
		},
	}
	cfg := runtimeprofile.GeneratedConfig(profile)
	entry, ok := cfg["codex_multi_agent"].(map[string]any)
	if !ok {
		t.Fatalf("expected codex_multi_agent preview, got %#v", cfg["codex_multi_agent"])
	}
	if entry["enabled"] != true || entry["max_concurrent_threads_per_session"] != 4 || entry["max_depth"] != 2 {
		t.Fatalf("preview entry = %#v", entry)
	}

	offCfg := runtimeprofile.GeneratedConfig(runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderCodex,
	})
	if _, present := offCfg["codex_multi_agent"]; present {
		t.Fatalf("expected no codex_multi_agent preview when unset, got %#v", offCfg["codex_multi_agent"])
	}
}

func TestImportProfileConfigRefusesMultiAgentManagedKeyChanges(t *testing.T) {
	service := newTestService(t)
	disabled := false
	created, err := service.Create("Codex Import", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		CodexMultiAgent: &runtimeprofile.CodexMultiAgent{Enabled: &disabled},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The explicit off control owns the projected V1 and V2 off keys.
	seed := "approval_policy = \"never\"\nsandbox_mode = \"danger-full-access\"\n\n[features]\nmulti_agent = false\nmulti_agent_v2 = false\n\n[agents]\nenabled = false\n"
	service.SetImportBaseline(func(runtimeprofile.Profile) (string, error) { return seed, nil })

	edited := "approval_policy = \"never\"\nsandbox_mode = \"danger-full-access\"\n\n[features]\nmulti_agent = true\nmulti_agent_v2 = true\n\n[agents]\nenabled = false\n\n[agents.researcher]\ndescription = \"Role.\"\n"
	_, err = service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: edited})
	if err == nil {
		t.Fatal("expected multi-agent managed key refusal")
	}
	var refusal *runtimeprofile.ImportConfigError
	if !asImportConfigError(err, &refusal) {
		t.Fatalf("error = %v (%T), want ImportConfigError", err, err)
	}
	found := false
	foundV2 := false
	for _, keyErr := range refusal.Errors {
		if keyErr.Key == "features.multi_agent" {
			found = true
			if keyErr.Field != "codex_multi_agent" {
				t.Fatalf("refusal field = %q, want codex_multi_agent", keyErr.Field)
			}
		}
		if keyErr.Key == "features.multi_agent_v2" {
			foundV2 = true
			if keyErr.Field != "codex_multi_agent" {
				t.Fatalf("V2 refusal field = %q, want codex_multi_agent", keyErr.Field)
			}
		}
		if strings.HasPrefix(keyErr.Key, "agents.researcher") {
			t.Fatalf("agent role tables are not managed, got %#v", keyErr)
		}
	}
	if !found {
		t.Fatalf("expected features.multi_agent refusal, got %#v", refusal.Errors)
	}
	if !foundV2 {
		t.Fatalf("expected features.multi_agent_v2 refusal, got %#v", refusal.Errors)
	}

	// An unchanged multi-agent block imports; unmanaged sibling keys survive in
	// the remainder. A managed leaf that lands inside a table with surviving
	// siblings may linger in the stored overlay text — the projection deep
	// merge keeps structured values authoritative, so the line is inert.
	unchanged := "approval_policy = \"never\"\nsandbox_mode = \"danger-full-access\"\n\n[features]\nmulti_agent = false\nmulti_agent_v2 = false\nweb_search_cached = true\n\n[agents]\nenabled = false\n"
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: unchanged})
	if err != nil {
		t.Fatalf("unchanged multi-agent keys must import, got %v", err)
	}
	if !strings.Contains(result.Profile.Fields.CustomConfigFile, "web_search_cached") {
		t.Fatalf("unmanaged remainder must stay, got %q", result.Profile.Fields.CustomConfigFile)
	}
}

func TestProviderOnlyUpdateClearsCodexMultiAgent(t *testing.T) {
	svc := newMultiAgentTestService(t)
	enabled := true
	created, err := svc.Create("codex-switch", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		CodexMultiAgent: &runtimeprofile.CodexMultiAgent{Enabled: &enabled},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updated, err := svc.Update(created.ID, "", runtimeprofile.ProviderPi, runtimeprofile.Fields{}, false, false)
	if err != nil {
		t.Fatalf("provider-only update: %v", err)
	}
	if updated.Provider != runtimeprofile.ProviderPi {
		t.Fatalf("provider = %q, want pi", updated.Provider)
	}
	if updated.Fields.CodexMultiAgent != nil {
		t.Fatalf("provider switch must clear Codex-only control, got %#v", updated.Fields.CodexMultiAgent)
	}
}

func TestImportProfileConfigPreservesLegacyMultiAgentOverlayWhileInheriting(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Codex Legacy Overlay", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		CustomConfigFile: "[features]\nmulti_agent = true\n\n[agents]\nenabled = true\nmax_depth = 3\n",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	service.SetImportBaseline(func(runtimeprofile.Profile) (string, error) {
		return "approval_policy = \"never\"\nsandbox_mode = \"danger-full-access\"\n", nil
	})

	edited := "approval_policy = \"never\"\nsandbox_mode = \"danger-full-access\"\n\n[features]\nmulti_agent = true\n\n[agents]\nenabled = true\nmax_depth = 3\n"
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: edited})
	if err != nil {
		t.Fatalf("unchanged legacy overlay must import: %v", err)
	}
	if result.Profile.Fields.CodexMultiAgent != nil {
		t.Fatalf("legacy overlay must not become an explicit structured choice, got %#v", result.Profile.Fields.CodexMultiAgent)
	}
	for _, want := range []string{"multi_agent = true", "enabled = true", "max_depth = 3"} {
		if !strings.Contains(result.Profile.Fields.CustomConfigFile, want) {
			t.Fatalf("legacy overlay lost %q: %s", want, result.Profile.Fields.CustomConfigFile)
		}
	}
}
