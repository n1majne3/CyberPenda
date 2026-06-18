package preflight_test

import (
	"context"
	"path/filepath"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/preflight"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

type services struct {
	preflight *preflight.Service
	profiles  *runtimeprofile.Service
	creds     *credential.Service
}

func newTestServices(t *testing.T) services {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	profiles := runtimeprofile.NewService(db)
	creds := credential.NewService(db)
	return services{
		preflight: preflight.NewService(profiles, creds),
		profiles:  profiles,
		creds:     creds,
	}
}

func TestRunFailsWhenProfileMissing(t *testing.T) {
	svc := newTestServices(t)

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: "missing",
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail when profile is missing")
	}
	if !checkFailed(result, "runtime_profile") {
		t.Fatalf("expected runtime_profile check to fail, got %#v", result.Checks)
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
		result := svc.preflight.Run(context.Background(), preflight.Request{
			RuntimeProfileID: profile.ID,
			ProjectID:        "p1",
			Runner:           runner,
		})
		if !result.Pass {
			t.Fatalf("expected preflight to pass for runner %q, got %#v", runner, result.Checks)
		}
	}
}

func TestRunIncludesExtraRefsFromRequest(t *testing.T) {
	svc := newTestServices(t)
	// Profile declares no credential refs; the launch request adds one.
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

func checkPassed(result preflight.Result, name string) bool {
	for _, check := range result.Checks {
		if check.Name == name {
			return check.Status == preflight.CheckPass
		}
	}
	return false
}

func checkFailed(result preflight.Result, name string) bool {
	for _, check := range result.Checks {
		if check.Name == name {
			return check.Status == preflight.CheckFail
		}
	}
	return false
}
