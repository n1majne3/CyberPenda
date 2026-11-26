package preflight_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/modeskill"
	"pentest/internal/preflight"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
)

func TestRunFailsWhenProfileMissing(t *testing.T) {
	svc := newTestServices(t)

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: "missing",
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail when profile is missing")
	}
	if !checkFailed(result, "runtime_configuration") {
		t.Fatalf("expected runtime_configuration check to fail, got %#v", result.Checks)
	}
}

func TestRunPassesWhenProfileHasNoCredentials(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if !result.Pass {
		t.Fatalf("expected preflight to pass, got %#v", result.Checks)
	}
	if !checkPassed(result, "credentials") {
		t.Fatalf("expected credentials check to pass with no refs, got %#v", result.Checks)
	}
}

func TestRunPassesWithInlineProfileAPIKeys(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex-inline",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{
			Model: "gpt-5",
			APIKeys: map[string]string{
				"OPENAI_API_KEY": "sk-inline",
			},
		},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if !result.Pass {
		t.Fatalf("expected preflight to pass with inline api keys, got %#v", result.Checks)
	}
	if !checkPassed(result, "credentials") {
		t.Fatalf("expected credentials check to pass, got %#v", result.Checks)
	}
}

func TestRunFailsWhenCredentialReferenceUnresolved(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{CredentialRefs: []string{"codex-api-key"}},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail when credential has no binding")
	}
	if !checkFailed(result, "credentials") {
		t.Fatalf("expected credentials check to fail, got %#v", result.Checks)
	}
}

func TestRunPassesWhenCredentialResolvedGlobally(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{CredentialRefs: []string{"codex-api-key"}},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	t.Setenv("CODEX_API_KEY", "configured-secret")
	if _, err := svc.creds.Upsert("codex-api-key", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceEnv, Value: "CODEX_API_KEY"}, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if !result.Pass {
		t.Fatalf("expected preflight to pass with global binding, got %#v", result.Checks)
	}
}

func TestRunFailsWhenEnvCredentialNotSet(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{CredentialRefs: []string{"codex-api-key"}},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	os.Unsetenv("PREFLIGHT_MISSING_API_KEY")
	if _, err := svc.creds.Upsert("codex-api-key", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceEnv, Value: "PREFLIGHT_MISSING_API_KEY"}, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail when env credential is not set")
	}
	if !checkFailed(result, "credentials") {
		t.Fatalf("expected credentials check to fail, got %#v", result.Checks)
	}
}

func TestRunFailsWhenEnvCredentialUnset(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{CredentialRefs: []string{"codex-api-key"}},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	source := credential.Source{Kind: credential.SourceEnv, Value: "PENTEST_UNSET_PREFLIGHT_KEY"}
	if _, err := svc.creds.Upsert("codex-api-key", credential.ScopeGlobal, "", source, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail when env credential is unset")
	}
	if !checkFailed(result, "credentials") {
		t.Fatalf("expected credentials check to fail, got %#v", result.Checks)
	}
}

func TestRunFailsWhenGlobalEnvBindingUnmaterializable(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	broken := credential.Source{Kind: credential.SourceEnv, Value: "PENTEST_UNSET_GLOBAL_VAR", DestinationEnv: "GLOBAL_BROKEN_VAR"}
	if _, err := svc.creds.Upsert("GLOBAL_BROKEN", credential.ScopeGlobal, "", broken, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail when a global env binding is unmaterializable")
	}
	detail := checkDetail(result, "credentials")
	if detail == "" {
		t.Fatalf("expected credentials check to fail with detail, got %#v", result.Checks)
	}
	if !strings.Contains(detail, "GLOBAL_BROKEN") {
		t.Fatalf("expected failure detail to name the global credential reference, got %q", detail)
	}
}

func TestRunPassesWithMaterializableGlobalEnvBinding(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	good := credential.Source{Kind: credential.SourceLiteral, Value: "ok", DestinationEnv: "GLOBAL_OK_VAR"}
	if _, err := svc.creds.Upsert("GLOBAL_OK", credential.ScopeGlobal, "", good, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if !result.Pass {
		t.Fatalf("expected preflight to pass with a healthy global binding, got %#v", result.Checks)
	}
	if !checkPassed(result, "credentials") {
		t.Fatalf("expected credentials check to pass, got %#v", result.Checks)
	}
}

func TestRunPassesWhenAllCredentialsMaterialize(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{CredentialRefs: []string{"codex-api-key", "extra-key"}},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	t.Setenv("CODEX_API_KEY", "configured-secret")
	if _, err := svc.creds.Upsert("codex-api-key", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceEnv, Value: "CODEX_API_KEY"}, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}
	if _, err := svc.creds.Upsert("extra-key", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceLiteral, Value: "extra-secret", DestinationEnv: "EXTRA_TOKEN"}, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if !result.Pass {
		t.Fatalf("expected preflight to pass when all credentials materialize, got %#v", result.Checks)
	}
	if !checkPassed(result, "credentials") {
		t.Fatalf("expected credentials check to pass, got %#v", result.Checks)
	}
}

func TestRunPassesWhenLiteralCredentialBackfillsDestinationEnv(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{CredentialRefs: []string{"codex-api-key"}},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	// Literal source with no DestinationEnv: the scan-time backfill maps it to
	// the credential_ref, so projection succeeds.
	source := credential.Source{Kind: credential.SourceLiteral, Value: "legacy-secret"}
	if _, err := svc.creds.Upsert("codex-api-key", credential.ScopeGlobal, "", source, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if !result.Pass {
		t.Fatalf("expected preflight to pass after backfill, got %#v", result.Checks)
	}
	if !checkPassed(result, "credentials") {
		t.Fatalf("expected credentials check to pass, got %#v", result.Checks)
	}
}

func TestRunFailsWhenCredentialDisabledForProject(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{CredentialRefs: []string{"codex-api-key"}},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := svc.creds.Upsert("codex-api-key", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceEnv, Value: "CODEX_API_KEY"}, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}
	if _, err := svc.creds.Upsert("codex-api-key", credential.ScopeProject, "p1", credential.Source{}, true); err != nil {
		t.Fatalf("upsert project disabled binding: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail when project disabled the binding")
	}
	if !checkFailed(result, "credentials") {
		t.Fatalf("expected credentials check to fail, got %#v", result.Checks)
	}
}

func TestRunRejectsUnsupportedRunner(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
		Runner:           "kali-magic",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail for unsupported runner")
	}
	if !checkFailed(result, "runner") {
		t.Fatalf("expected runner check to fail, got %#v", result.Checks)
	}
}

func TestRunAcceptsSandboxAndHostRunners(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	for _, runner := range []string{"sandbox", "host", ""} {
		req := preflight.Request{
			RuntimeProfileID: profile.ID,
			ProjectID:        "p1",
			Runner:           runner,
		}
		if runner == "host" {
			req.HostActivated = true
		}
		result := svc.preflight.Run(context.Background(), req)
		if !result.Pass {
			t.Fatalf("expected preflight to pass for runner %q, got %#v", runner, result.Checks)
		}
	}
}

func TestRunIncludesExtraRefsFromRequest(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID:        profile.ID,
		ProjectID:               "p1",
		CredentialRefsToResolve: []string{"extra-key"},
	})

	if result.Pass {
		t.Fatal("expected preflight to fail for unresolved extra ref")
	}
	if !checkFailed(result, "credentials") {
		t.Fatalf("expected credentials check to fail, got %#v", result.Checks)
	}
}

func TestRunFailsWhenEnabledSkillBundleIsMissing(t *testing.T) {
	svc := newTestServices(t)
	skillsRoot := filepath.Join(t.TempDir(), "skills")
	skills := skill.NewService(svc.db, skillsRoot)
	svc.preflight = svc.newPreflight(skills)
	profile, err := svc.profiles.Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	published, err := skills.Publish(context.Background(), skill.PublishRequest{
		Metadata: skill.Metadata{
			ID:   "recon-helper",
			Name: "Recon Helper",
		},
		Files: map[string]string{"SKILL.md": "# Recon"},
	})
	if err != nil {
		t.Fatalf("publish skill: %v", err)
	}
	if err := os.RemoveAll(published.BundlePath); err != nil {
		t.Fatalf("remove bundle: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})
	if result.Pass {
		t.Fatal("expected preflight to fail when enabled skill bundle is missing")
	}
	if !checkFailed(result, "skills") {
		t.Fatalf("expected skills check to fail, got %#v", result.Checks)
	}
}

func TestRunFGSOmitsLegacyModeSkillAndRejectsDisabledOnlySkill(t *testing.T) {
	svc := newTestServices(t)
	skills := skill.NewService(svc.db, filepath.Join(t.TempDir(), "skills"))
	svc.preflight = svc.newPreflight(skills)
	profile, err := svc.profiles.Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := skills.Publish(context.Background(), skill.PublishRequest{
		Metadata: skill.Metadata{ID: "disabled-only", Name: "Disabled Only"},
		Files:    map[string]string{"SKILL.md": "---\nname: disabled-only\nblackboard_modes: [disabled]\n---\n# Disabled only\n"},
	}); err != nil {
		t.Fatal(err)
	}
	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID, ProjectID: "p1", BlackboardMode: modeskill.ModeWorkingGraph,
	})
	if result.ModeSkill != nil {
		t.Fatalf("Mode Skill preview = %#v", result.ModeSkill)
	}
	if !checkFailed(result, "skills") {
		t.Fatalf("incompatible Skill did not fail Preflight: %#v", result.Checks)
	}
}

func TestRunDisabledModeNeedsNoModeSkill(t *testing.T) {
	// Mode validation must not depend on tools installed on the test host.
	t.Setenv("PATH", t.TempDir())
	svc := newTestServices(t)
	skills := skill.NewService(svc.db, filepath.Join(t.TempDir(), "skills"))
	svc.preflight = svc.newPreflight(skills)
	profile, err := svc.profiles.Create("fake-disabled", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := skills.Publish(context.Background(), skill.PublishRequest{
		Metadata: skill.Metadata{ID: "disabled-only", Name: "Disabled Only"},
		Files:    map[string]string{"SKILL.md": "---\nname: disabled-only\nblackboard_modes: [disabled]\n---\n# Disabled only\n"},
	}); err != nil {
		t.Fatal(err)
	}
	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID, ProjectID: "p1", BlackboardMode: modeskill.ModeDisabled,
	})
	if !result.Pass {
		t.Fatalf("Disabled Preflight failed: %#v", result.Checks)
	}
	if result.ModeSkill != nil {
		t.Fatalf("Disabled Preflight kept a Mode Skill preview: %#v", result.ModeSkill)
	}
	for _, check := range result.Checks {
		if check.Name == "mode_skill" && check.Status != preflight.CheckPass {
			t.Fatalf("mode_skill check = %#v", check)
		}
	}
}

func TestRunSkipsUnavailableCapturedSkills(t *testing.T) {
	svc := newTestServices(t)
	skillsRoot := filepath.Join(t.TempDir(), "skills")
	skills := skill.NewService(svc.db, skillsRoot)
	svc.preflight = svc.newPreflight(skills)
	profile, err := svc.profiles.Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := skills.Publish(context.Background(), skill.PublishRequest{
		Metadata: skill.Metadata{
			ID:   "recon-helper",
			Name: "Recon Helper",
		},
		Files: map[string]string{"SKILL.md": "# Recon"},
	}); err != nil {
		t.Fatalf("publish skill: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
		CapturedSkillIDs: []string{"recon-helper", "reverse-skill-router", "vulnerabilities-xss"},
	})
	if !checkPassed(result, "skills") {
		t.Fatalf("expected skills check to pass with unavailable captured skills skipped, got %#v", result.Checks)
	}
	if len(result.Skills) != 1 || result.Skills[0].ID != "recon-helper" {
		t.Fatalf("expected only the surviving skill in the preview, got %#v", result.Skills)
	}
	for _, check := range result.Checks {
		if check.Name == "skills" && !strings.Contains(check.Detail, "2 captured skill(s) unavailable and skipped") {
			t.Fatalf("expected skip note in skills check detail, got %q", check.Detail)
		}
	}
}

func TestRunListsEnabledSkillsWithoutAddingCredentialRequirements(t *testing.T) {
	if _, err := runner.DetectEngine(context.Background(), "docker", nil); err != nil {
		// The test asserts an overall-passing preflight, which needs a live
		// container engine. Windows CI runners ship the docker CLI without a
		// running engine; GitHub's linux runners have one, so Linux CI still
		// exercises this assertion.
		t.Skip("test requires a live container engine (docker info failed)")
	}
	svc := newTestServices(t)
	skills := skill.NewService(svc.db, filepath.Join(t.TempDir(), "skills"))
	svc.preflight = svc.newPreflight(skills)
	profile, err := svc.profiles.Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := skills.Publish(context.Background(), skill.PublishRequest{
		Metadata: skill.Metadata{
			ID:   "recon-helper",
			Name: "Recon Helper",
		},
		Files: map[string]string{"SKILL.md": "# Recon"},
	}); err != nil {
		t.Fatalf("publish skill: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})
	if !result.Pass {
		t.Fatalf("expected enabled skill without profile credentials to pass preflight, got %#v", result.Checks)
	}
	if len(result.Skills) != 1 || result.Skills[0].ID != "recon-helper" {
		t.Fatalf("expected enabled skill preview, got %#v", result.Skills)
	}
}

func TestRunFailsWhenRequiredRuntimeLacksModelProvider(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create("codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail when required runtime has no model provider")
	}
	if !checkFailed(result, "model_provider") {
		t.Fatalf("expected model_provider check to fail, got %#v", result.Checks)
	}
}
