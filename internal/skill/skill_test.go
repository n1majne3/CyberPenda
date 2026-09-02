package skill_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
	"pentest/internal/store"
)

func TestValidateSkillBundle(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	if err := os.MkdirAll(bundle, 0o700); err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "SKILL.md"), []byte("# Recon\n\nUse approved recon tools."), 0o600); err != nil {
		t.Fatalf("write skill doc: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bundle, "scripts"), 0o700); err != nil {
		t.Fatalf("create scripts dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "scripts", "probe.sh"), []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}

	if err := skill.ValidateBundle(bundle, skill.Metadata{ID: "recon-helper", Name: "Recon Helper"}); err != nil {
		t.Fatalf("expected valid bundle: %v", err)
	}

	if err := skill.ValidateBundle(bundle, skill.Metadata{ID: "../escape", Name: "Recon Helper"}); err == nil {
		t.Fatal("expected path-like skill id to be rejected")
	}

	if err := os.Symlink("/etc/passwd", filepath.Join(bundle, "scripts", "leak")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	err := skill.ValidateBundle(bundle, skill.Metadata{ID: "recon-helper", Name: "Recon Helper"})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestPublishSkillIsAtomic(t *testing.T) {
	db := openTestStore(t)
	svc := skill.NewService(db, filepath.Join(t.TempDir(), "skills"))
	ctx := context.Background()

	published, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{ID: "recon-helper", Name: "Recon Helper"},
		Files: map[string]string{
			"SKILL.md": "version one",
		},
	})
	if err != nil {
		t.Fatalf("publish initial skill: %v", err)
	}
	if published.ID != "recon-helper" {
		t.Fatalf("unexpected published skill: %#v", published)
	}
	livePath := filepath.Join(published.BundlePath, "SKILL.md")
	before, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatalf("read live skill doc: %v", err)
	}
	if string(before) != "version one" {
		t.Fatalf("unexpected live content before failed publish: %q", before)
	}

	_, err = svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{ID: "recon-helper", Name: "Recon Helper"},
		Files: map[string]string{
			"SKILL.md":  "version two",
			"../escape": "must be rejected",
		},
	})
	if err == nil {
		t.Fatal("expected invalid update to fail")
	}

	after, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatalf("read live skill doc after failed publish: %v", err)
	}
	if string(after) != "version one" {
		t.Fatalf("failed publish mutated live bundle: %q", after)
	}
}

func TestDefaultEnablementOptOutAndDeletionLifecycle(t *testing.T) {
	db := openTestStore(t)
	profiles := runtimeprofile.NewService(db)
	profileA, err := profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile A: %v", err)
	}
	profileB, err := profiles.Create("Claude", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile B: %v", err)
	}
	svc := skill.NewService(db, filepath.Join(t.TempDir(), "skills"))
	ctx := context.Background()

	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{ID: "recon-helper", Name: "Recon Helper"},
		Files:    map[string]string{"SKILL.md": "version one"},
	}); err != nil {
		t.Fatalf("publish skill: %v", err)
	}
	assertEnabledSkillIDs(t, svc, profileA.ID, []string{"recon-helper"})
	assertEnabledSkillIDs(t, svc, profileB.ID, []string{"recon-helper"})

	if err := svc.SetOptOut(profileA.ID, "recon-helper", true); err != nil {
		t.Fatalf("set opt-out: %v", err)
	}
	assertEnabledSkillIDs(t, svc, profileA.ID, []string{})
	assertEnabledSkillIDs(t, svc, profileB.ID, []string{"recon-helper"})

	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{ID: "recon-helper", Name: "Recon Helper Updated"},
		Files:    map[string]string{"SKILL.md": "version two"},
	}); err != nil {
		t.Fatalf("update skill: %v", err)
	}
	assertEnabledSkillIDs(t, svc, profileA.ID, []string{})
	assertEnabledSkillIDs(t, svc, profileB.ID, []string{"recon-helper"})

	if err := svc.Delete(ctx, "recon-helper", false); !errors.Is(err, skill.ErrEnabled) {
		t.Fatalf("expected guarded delete to return ErrEnabled, got %v", err)
	}
	if err := svc.Delete(ctx, "recon-helper", true); err != nil {
		t.Fatalf("force delete skill: %v", err)
	}
	if _, err := svc.Get("recon-helper"); !errors.Is(err, skill.ErrNotFound) {
		t.Fatalf("expected deleted skill to be missing, got %v", err)
	}

	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{ID: "recon-helper", Name: "Recon Helper Reimported"},
		Files:    map[string]string{"SKILL.md": "version three"},
	}); err != nil {
		t.Fatalf("reimport skill: %v", err)
	}
	assertEnabledSkillIDs(t, svc, profileA.ID, []string{"recon-helper"})
	assertEnabledSkillIDs(t, svc, profileB.ID, []string{"recon-helper"})
}

func TestGlobalSkillOptOutOverridesProfilesAndDirectLaunches(t *testing.T) {
	db := openTestStore(t)
	profiles := runtimeprofile.NewService(db)
	profileA, err := profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile A: %v", err)
	}
	profileB, err := profiles.Create("Claude", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile B: %v", err)
	}
	svc := skill.NewService(db, filepath.Join(t.TempDir(), "skills"))
	ctx := context.Background()

	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{ID: "recon-helper", Name: "Recon Helper"},
		Files:    map[string]string{"SKILL.md": "version one"},
	}); err != nil {
		t.Fatalf("publish skill: %v", err)
	}
	if err := svc.SetOptOut(profileA.ID, "recon-helper", true); err != nil {
		t.Fatalf("set profile opt-out: %v", err)
	}
	if err := svc.SetGlobalOptOut("recon-helper", true); err != nil {
		t.Fatalf("set global opt-out: %v", err)
	}

	globallyOptedOut, err := svc.Get("recon-helper")
	if err != nil {
		t.Fatalf("get globally opted-out skill: %v", err)
	}
	if !globallyOptedOut.GloballyOptedOut {
		t.Fatalf("expected global opt-out to be stored, got %#v", globallyOptedOut)
	}
	assertEnabledSkillIDs(t, svc, "", []string{})
	assertEnabledSkillIDs(t, svc, profileA.ID, []string{})
	assertEnabledSkillIDs(t, svc, profileB.ID, []string{})

	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{ID: "recon-helper", Name: "Recon Helper Updated"},
		Files:    map[string]string{"SKILL.md": "version two"},
	}); err != nil {
		t.Fatalf("update skill: %v", err)
	}
	updated, err := svc.Get("recon-helper")
	if err != nil {
		t.Fatalf("get updated skill: %v", err)
	}
	if !updated.GloballyOptedOut {
		t.Fatal("skill update must preserve the global opt-out")
	}

	if err := svc.SetGlobalOptOut("recon-helper", false); err != nil {
		t.Fatalf("remove global opt-out: %v", err)
	}
	assertEnabledSkillIDs(t, svc, "", []string{"recon-helper"})
	assertEnabledSkillIDs(t, svc, profileA.ID, []string{})
	assertEnabledSkillIDs(t, svc, profileB.ID, []string{"recon-helper"})

	if err := svc.SetGlobalOptOut("recon-helper", true); err != nil {
		t.Fatalf("restore global opt-out: %v", err)
	}
	if err := svc.Delete(ctx, "recon-helper", false); err != nil {
		t.Fatalf("delete globally opted-out skill: %v", err)
	}
	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{ID: "recon-helper", Name: "Recon Helper Reimported"},
		Files:    map[string]string{"SKILL.md": "version three"},
	}); err != nil {
		t.Fatalf("reimport skill: %v", err)
	}
	reimported, err := svc.Get("recon-helper")
	if err != nil {
		t.Fatalf("get reimported skill: %v", err)
	}
	if reimported.GloballyOptedOut {
		t.Fatal("Skill Deletion and recreation must not restore the old global opt-out")
	}
	assertEnabledSkillIDs(t, svc, profileA.ID, []string{"recon-helper"})
	assertEnabledSkillIDs(t, svc, profileB.ID, []string{"recon-helper"})
}

func TestAllGlobalSkillOptOutsPreserveProfileChoicesAndKeepFutureSkillsDefaultOn(t *testing.T) {
	db := openTestStore(t)
	profiles := runtimeprofile.NewService(db)
	profile, err := profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	svc := skill.NewService(db, filepath.Join(t.TempDir(), "skills"))
	ctx := context.Background()

	for _, id := range []string{"recon-helper", "report-helper"} {
		if _, err := svc.Publish(ctx, skill.PublishRequest{
			Metadata: skill.Metadata{ID: id, Name: id},
			Files:    map[string]string{"SKILL.md": "# " + id},
		}); err != nil {
			t.Fatalf("publish %s: %v", id, err)
		}
	}
	if err := svc.SetOptOut(profile.ID, "recon-helper", true); err != nil {
		t.Fatalf("set Profile Skill Opt-Out: %v", err)
	}

	if err := svc.SetAllGlobalOptOut(true); err != nil {
		t.Fatalf("disable all Skills globally: %v", err)
	}
	assertEnabledSkillIDs(t, svc, "", []string{})
	assertEnabledSkillIDs(t, svc, profile.ID, []string{})
	for _, id := range []string{"recon-helper", "report-helper"} {
		got, err := svc.Get(id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if !got.GloballyOptedOut {
			t.Fatalf("expected %s to have a Global Skill Opt-Out", id)
		}
	}

	if _, err := svc.Publish(ctx, skill.PublishRequest{
		Metadata: skill.Metadata{ID: "future-helper", Name: "Future Helper"},
		Files:    map[string]string{"SKILL.md": "# Future Helper"},
	}); err != nil {
		t.Fatalf("publish future Skill: %v", err)
	}
	assertEnabledSkillIDs(t, svc, "", []string{"future-helper"})
	assertEnabledSkillIDs(t, svc, profile.ID, []string{"future-helper"})

	if err := svc.SetAllGlobalOptOut(false); err != nil {
		t.Fatalf("enable all Skills globally: %v", err)
	}
	assertEnabledSkillIDs(t, svc, "", []string{"future-helper", "recon-helper", "report-helper"})
	assertEnabledSkillIDs(t, svc, profile.ID, []string{"future-helper", "report-helper"})
}

func openTestStore(t *testing.T) *store.DB {
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
	return db
}

func assertEnabledSkillIDs(t *testing.T, svc *skill.Service, profileID string, want []string) {
	t.Helper()
	got, err := svc.EnabledSkills(profileID)
	if err != nil {
		t.Fatalf("enabled skills: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("enabled skills for %s: got %#v want %#v", profileID, got, want)
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("enabled skills for %s: got %#v want %#v", profileID, got, want)
		}
	}
}
