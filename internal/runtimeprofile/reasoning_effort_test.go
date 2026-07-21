package runtimeprofile_test

import (
	"errors"
	"testing"

	"pentest/internal/runtimeprofile"
)

func TestNormalizeReasoningEffortDefaultsMissingToHigh(t *testing.T) {
	got, err := runtimeprofile.NormalizeReasoningEffort("")
	if err != nil {
		t.Fatalf("normalize empty: %v", err)
	}
	if got != runtimeprofile.ReasoningEffortHigh {
		t.Fatalf("got %q, want high", got)
	}

	got, err = runtimeprofile.NormalizeReasoningEffort("  ")
	if err != nil {
		t.Fatalf("normalize blank: %v", err)
	}
	if got != runtimeprofile.ReasoningEffortHigh {
		t.Fatalf("got %q, want high", got)
	}
}

func TestNormalizeReasoningEffortAcceptsExactlyFiveValues(t *testing.T) {
	for _, value := range []string{"low", "medium", "high", "xhigh", "max"} {
		got, err := runtimeprofile.NormalizeReasoningEffort(value)
		if err != nil {
			t.Fatalf("normalize %q: %v", value, err)
		}
		if string(got) != value {
			t.Fatalf("normalize %q = %q", value, got)
		}
	}
}

func TestNormalizeReasoningEffortRejectsUnknownValues(t *testing.T) {
	for _, value := range []string{"auto", "default", "minimal", "ultra", "HIGH"} {
		_, err := runtimeprofile.NormalizeReasoningEffort(value)
		if !errors.Is(err, runtimeprofile.ErrInvalidReasoningEffort) {
			t.Fatalf("normalize %q err = %v, want ErrInvalidReasoningEffort", value, err)
		}
	}
}

func TestResolveRequestedReasoningEffortPrecedence(t *testing.T) {
	// Precedence: turn selection → launch override → profile default → high.
	got, err := runtimeprofile.ResolveRequestedReasoningEffort("max", "low", "medium")
	if err != nil || got != runtimeprofile.ReasoningEffortMax {
		t.Fatalf("turn selection should win, got %q err=%v", got, err)
	}
	got, err = runtimeprofile.ResolveRequestedReasoningEffort("", "low", "medium")
	if err != nil || got != runtimeprofile.ReasoningEffortLow {
		t.Fatalf("launch override should win over profile, got %q err=%v", got, err)
	}
	got, err = runtimeprofile.ResolveRequestedReasoningEffort("", "", "medium")
	if err != nil || got != runtimeprofile.ReasoningEffortMedium {
		t.Fatalf("profile default should win over high, got %q err=%v", got, err)
	}
	got, err = runtimeprofile.ResolveRequestedReasoningEffort("", "", "")
	if err != nil || got != runtimeprofile.ReasoningEffortHigh {
		t.Fatalf("missing values should resolve to high, got %q err=%v", got, err)
	}
}

func TestResolveRequestedReasoningEffortRejectsInvalidNonEmptyCandidates(t *testing.T) {
	// A non-empty invalid value must fail clearly, not fall through to a
	// lower-precedence valid value or the high default.
	cases := []struct {
		name                         string
		turn, launch, profileDefault string
	}{
		{"invalid turn selection", "auto", "low", "medium"},
		{"invalid launch override", "", "auto", "medium"},
		{"invalid profile default", "", "", "auto"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runtimeprofile.ResolveRequestedReasoningEffort(tc.turn, tc.launch, tc.profileDefault)
			if !errors.Is(err, runtimeprofile.ErrInvalidReasoningEffort) {
				t.Fatalf("err = %v, want ErrInvalidReasoningEffort", err)
			}
		})
	}
}

func TestCreateRejectsInvalidReasoningEffort(t *testing.T) {
	service := newTestService(t)
	_, err := service.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ReasoningEffort: "auto",
	})
	if !errors.Is(err, runtimeprofile.ErrInvalidReasoningEffort) {
		t.Fatalf("create err = %v, want ErrInvalidReasoningEffort", err)
	}
}

func TestCreatePersistsReasoningEffortWithoutRewritingMissingAsHigh(t *testing.T) {
	service := newTestService(t)

	without, err := service.Create("No Effort", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create without: %v", err)
	}
	if without.Fields.ReasoningEffort != "" {
		t.Fatalf("missing effort should stay empty in storage, got %q", without.Fields.ReasoningEffort)
	}
	fetched, err := service.Get(without.ID)
	if err != nil {
		t.Fatalf("get without: %v", err)
	}
	if fetched.Fields.ReasoningEffort != "" {
		t.Fatalf("persisted missing effort should stay empty, got %q", fetched.Fields.ReasoningEffort)
	}

	with, err := service.Create("With Effort", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ReasoningEffort: "xhigh",
	})
	if err != nil {
		t.Fatalf("create with: %v", err)
	}
	if with.Fields.ReasoningEffort != "xhigh" {
		t.Fatalf("stored effort = %q, want xhigh", with.Fields.ReasoningEffort)
	}
}
