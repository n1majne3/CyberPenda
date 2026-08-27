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

func TestCodexMultiAgentDefaultsOffWithoutRewritingExistingProfiles(t *testing.T) {
	svc := newMultiAgentTestService(t)

	created, err := svc.Create("codex-off", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Fields.CodexMultiAgent != nil {
		t.Fatalf("expected no codex_multi_agent storage, got %#v", created.Fields.CodexMultiAgent)
	}
	if runtimeprofile.CodexMultiAgentEnabled(created) {
		t.Fatalf("expected multi-agent tools to default off")
	}

	loaded, err := svc.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if runtimeprofile.CodexMultiAgentEnabled(loaded) {
		t.Fatalf("expected stored profile to keep multi-agent tools off")
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
	if !runtimeprofile.CodexMultiAgentEnabled(created) {
		t.Fatalf("expected multi-agent tools on")
	}
	stored := created.Fields.CodexMultiAgent
	if stored == nil || stored.MaxConcurrentThreadsPerSession != 4 || stored.MaxDepth != 2 {
		t.Fatalf("stored caps = %#v", stored)
	}

	loaded, err := svc.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !runtimeprofile.CodexMultiAgentEnabled(loaded) {
		t.Fatalf("expected persisted multi-agent tools on")
	}
	if got := loaded.Fields.CodexMultiAgent; got == nil || got.MaxConcurrentThreadsPerSession != 4 || got.MaxDepth != 2 {
		t.Fatalf("persisted caps = %#v", got)
	}
}

func TestCodexMultiAgentExplicitFalseStaysOff(t *testing.T) {
	svc := newMultiAgentTestService(t)
	disabled := false

	created, err := svc.Create("codex-explicit-off", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		CodexMultiAgent: &runtimeprofile.CodexMultiAgent{Enabled: &disabled},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if runtimeprofile.CodexMultiAgentEnabled(created) {
		t.Fatalf("explicit false must stay off")
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
	created, err := service.Create("Codex Import", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The baseline mirrors the generated off projection.
	seed := "approval_policy = \"never\"\nsandbox_mode = \"danger-full-access\"\n\n[features]\nmulti_agent = false\n\n[agents]\nenabled = false\n"
	service.SetImportBaseline(func(runtimeprofile.Profile) (string, error) { return seed, nil })

	edited := "approval_policy = \"never\"\nsandbox_mode = \"danger-full-access\"\n\n[features]\nmulti_agent = true\n\n[agents]\nenabled = false\n\n[agents.researcher]\ndescription = \"Role.\"\n"
	_, err = service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: edited})
	if err == nil {
		t.Fatal("expected multi-agent managed key refusal")
	}
	var refusal *runtimeprofile.ImportConfigError
	if !asImportConfigError(err, &refusal) {
		t.Fatalf("error = %v (%T), want ImportConfigError", err, err)
	}
	found := false
	for _, keyErr := range refusal.Errors {
		if keyErr.Key == "features.multi_agent" {
			found = true
			if keyErr.Field != "codex_multi_agent" {
				t.Fatalf("refusal field = %q, want codex_multi_agent", keyErr.Field)
			}
		}
		if strings.HasPrefix(keyErr.Key, "agents.researcher") {
			t.Fatalf("agent role tables are not managed, got %#v", keyErr)
		}
	}
	if !found {
		t.Fatalf("expected features.multi_agent refusal, got %#v", refusal.Errors)
	}

	// An unchanged multi-agent block imports; unmanaged sibling keys survive in
	// the remainder. A managed leaf that lands inside a table with surviving
	// siblings may linger in the stored overlay text — the projection deep
	// merge keeps structured values authoritative, so the line is inert.
	unchanged := "approval_policy = \"never\"\nsandbox_mode = \"danger-full-access\"\n\n[features]\nmulti_agent = false\nweb_search_cached = true\n\n[agents]\nenabled = false\n"
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: unchanged})
	if err != nil {
		t.Fatalf("unchanged multi-agent keys must import, got %v", err)
	}
	if !strings.Contains(result.Profile.Fields.CustomConfigFile, "web_search_cached") {
		t.Fatalf("unmanaged remainder must stay, got %q", result.Profile.Fields.CustomConfigFile)
	}
}
