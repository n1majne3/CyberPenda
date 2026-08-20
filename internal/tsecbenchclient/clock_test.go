package tsecbenchclient_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/tsecbenchclient"
)

func TestBudgetMinutesUsesFixedFirstPassLimits(t *testing.T) {
	if got := tsecbenchclient.BudgetMinutes("easy"); got != 12 {
		t.Fatalf("easy = %d", got)
	}
	if got := tsecbenchclient.BudgetMinutes("MEDIUM"); got != 25 {
		t.Fatalf("medium = %d", got)
	}
	if got := tsecbenchclient.BudgetMinutes("hard"); got != 40 {
		t.Fatalf("hard = %d", got)
	}
	if got := tsecbenchclient.BudgetMinutes("unknown"); got != 25 {
		t.Fatalf("unknown = %d", got)
	}
}

func TestAnnotateMarksAnActiveChallengeOverBudgetAfterTheFirstPass(t *testing.T) {
	path := filepath.Join(t.TempDir(), tsecbenchclient.ClockFileName)
	start := time.Date(2026, 8, 16, 15, 10, 0, 0, time.UTC)
	store := tsecbenchclient.ClockStore{Path: path, Now: func() time.Time { return start }}
	if err := store.RecordStart("bctf-08", "hard", 1); err != nil {
		t.Fatal(err)
	}
	store.Now = func() time.Time { return start.Add(41 * time.Minute) }
	out := store.Annotate([]tsecbenchclient.Challenge{{
		UniqueCode: "bctf-08", Difficulty: "hard", ContainerStatus: "available",
	}})
	if out[0].BudgetMin == nil || *out[0].BudgetMin != 40 {
		t.Fatalf("budget = %#v", out[0].BudgetMin)
	}
	if out[0].ElapsedMin == nil || *out[0].ElapsedMin != 41 {
		t.Fatalf("elapsed = %#v", out[0].ElapsedMin)
	}
	if out[0].OverBudget == nil || !*out[0].OverBudget {
		t.Fatalf("over_budget = %#v", out[0].OverBudget)
	}
	if out[0].AttemptN == nil || *out[0].AttemptN != 1 {
		t.Fatalf("attempt_n = %#v", out[0].AttemptN)
	}
}

func TestAnnotateDoesNotMarkAStoppedChallengeOverBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), tsecbenchclient.ClockFileName)
	start := time.Date(2026, 8, 16, 15, 10, 0, 0, time.UTC)
	store := tsecbenchclient.ClockStore{Path: path, Now: func() time.Time { return start }}
	if err := store.RecordStart("bctf-08", "hard", 1); err != nil {
		t.Fatal(err)
	}
	store.Now = func() time.Time { return start.Add(41 * time.Minute) }
	out := store.Annotate([]tsecbenchclient.Challenge{{
		UniqueCode: "bctf-08", Difficulty: "hard", ContainerStatus: "stopped",
	}})
	if out[0].OverBudget != nil && *out[0].OverBudget {
		t.Fatalf("stopped challenge marked over_budget: %#v", out[0])
	}
}

func TestAnnotateSeedsStartedAtNowWhenAnActiveChallengeHasNoClockEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), tsecbenchclient.ClockFileName)
	now := time.Date(2026, 8, 16, 16, 0, 0, 0, time.UTC)
	store := tsecbenchclient.ClockStore{Path: path, Now: func() time.Time { return now }}
	out := store.Annotate([]tsecbenchclient.Challenge{{
		UniqueCode: "bctf-12", Difficulty: "easy", ContainerStatus: "available",
	}})
	if out[0].OverBudget == nil || *out[0].OverBudget {
		t.Fatalf("seeded challenge over_budget = %#v", out[0].OverBudget)
	}
	if out[0].ElapsedMin == nil || *out[0].ElapsedMin != 0 {
		t.Fatalf("seeded elapsed = %#v", out[0].ElapsedMin)
	}
	if out[0].BudgetMin == nil || *out[0].BudgetMin != 12 {
		t.Fatalf("seeded budget = %#v", out[0].BudgetMin)
	}
}

func TestClearRemovesOnlyTheNamedEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), tsecbenchclient.ClockFileName)
	store := tsecbenchclient.ClockStore{Path: path, Now: time.Now}
	if err := store.RecordStart("keep", "easy", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordStart("drop", "easy", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear("drop"); err != nil {
		t.Fatal(err)
	}
	out := store.Annotate([]tsecbenchclient.Challenge{
		{UniqueCode: "keep", ContainerStatus: "available", Difficulty: "easy"},
		{UniqueCode: "drop", ContainerStatus: "available", Difficulty: "easy"},
	})
	if out[0].AttemptN == nil || *out[0].AttemptN != 1 {
		t.Fatalf("keep attempt = %#v", out[0].AttemptN)
	}
	if out[1].ElapsedMin == nil || *out[1].ElapsedMin != 0 {
		t.Fatalf("cleared entry should reseed, elapsed=%#v", out[1].ElapsedMin)
	}
}

func TestNextAttemptIncrementsAfterClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), tsecbenchclient.ClockFileName)
	store := tsecbenchclient.ClockStore{Path: path, Now: time.Now}
	if err := store.RecordStart("bctf-08", "hard", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear("bctf-08"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordStart("bctf-08", "hard", 0); err != nil {
		t.Fatal(err)
	}
	out := store.Annotate([]tsecbenchclient.Challenge{{
		UniqueCode: "bctf-08", Difficulty: "hard", ContainerStatus: "available",
	}})
	if out[0].AttemptN == nil || *out[0].AttemptN != 2 {
		t.Fatalf("second start attempt_n = %#v", out[0].AttemptN)
	}
}

func TestAnnotateLeavesChallengesUnchangedWhenClockFileIsUnreadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blocked", tsecbenchclient.ClockFileName)
	if err := os.WriteFile(filepath.Join(dir, "blocked"), []byte("not-a-dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := tsecbenchclient.ClockStore{Path: path, Now: time.Now}
	in := []tsecbenchclient.Challenge{{UniqueCode: "one", ContainerStatus: "available"}}
	out := store.Annotate(in)
	if out[0].OverBudget != nil {
		t.Fatalf("unreadable clock annotated %#v", out[0])
	}
}
