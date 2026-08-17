package tsecbenchclient

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ClockFileName is the Challenge Pass Clock file under Runtime Workdir/.pentest.
const ClockFileName = "challenge-clock.json"

// ClockStore is the file-backed wall clock for one Hosted Evaluation Run.
type ClockStore struct {
	Path string
	Now  func() time.Time
}

type ClockEntry struct {
	UniqueCode string    `json:"unique_code"`
	StartedAt  time.Time `json:"started_at"`
	BudgetMin  int       `json:"budget_min"`
	Difficulty string    `json:"difficulty,omitempty"`
	AttemptN   int       `json:"attempt_n"`
}

type clockFile struct {
	Entries     map[string]ClockEntry `json:"entries"`
	LastAttempt map[string]int        `json:"last_attempt,omitempty"`
}

// BudgetMinutes is the first-pass wall-clock limit for one difficulty.
func BudgetMinutes(difficulty any) int {
	switch strings.ToLower(strings.TrimSpace(stringifyDifficulty(difficulty))) {
	case "easy":
		return 12
	case "hard":
		return 40
	default:
		return 25
	}
}

func stringifyDifficulty(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func (store ClockStore) now() time.Time {
	if store.Now != nil {
		return store.Now()
	}
	return time.Now().UTC()
}

func (store ClockStore) load() clockFile {
	empty := clockFile{Entries: map[string]ClockEntry{}, LastAttempt: map[string]int{}}
	raw, err := os.ReadFile(store.Path)
	if err != nil {
		return empty
	}
	var file clockFile
	if err := json.Unmarshal(raw, &file); err != nil || file.Entries == nil {
		return empty
	}
	if file.LastAttempt == nil {
		file.LastAttempt = map[string]int{}
	}
	return file
}

func (store ClockStore) save(file clockFile) error {
	if strings.TrimSpace(store.Path) == "" {
		return nil
	}
	if file.Entries == nil {
		file.Entries = map[string]ClockEntry{}
	}
	if file.LastAttempt == nil {
		file.LastAttempt = map[string]int{}
	}
	if err := os.MkdirAll(filepath.Dir(store.Path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(store.Path, append(raw, '\n'), 0o600)
}

// RecordStart records a first-seen start. attemptN 0 means increment last_attempt.
func (store ClockStore) RecordStart(code, difficulty string, attemptN int) error {
	code = strings.TrimSpace(code)
	if code == "" || strings.TrimSpace(store.Path) == "" {
		return nil
	}
	file := store.load()
	if _, exists := file.Entries[code]; exists {
		return nil
	}
	if attemptN <= 0 {
		attemptN = file.LastAttempt[code] + 1
	}
	if attemptN <= 0 {
		attemptN = 1
	}
	file.Entries[code] = ClockEntry{
		UniqueCode: code,
		StartedAt:  store.now(),
		BudgetMin:  BudgetMinutes(difficulty),
		Difficulty: strings.ToLower(strings.TrimSpace(difficulty)),
		AttemptN:   attemptN,
	}
	file.LastAttempt[code] = attemptN
	return store.save(file)
}

// Clear removes the open entry but keeps last_attempt for the next start.
func (store ClockStore) Clear(code string) error {
	code = strings.TrimSpace(code)
	if code == "" || strings.TrimSpace(store.Path) == "" {
		return nil
	}
	file := store.load()
	if _, ok := file.Entries[code]; !ok {
		return nil
	}
	delete(file.Entries, code)
	return store.save(file)
}

func challengeIsActive(challenge Challenge) bool {
	status := strings.ToLower(strings.TrimSpace(challenge.ContainerStatus))
	return status == "available" || status == "pending" || len(challenge.ContainerAddr) > 0
}

// Annotate projects elapsed_min, budget_min, over_budget, and attempt_n.
func (store ClockStore) Annotate(challenges []Challenge) []Challenge {
	if strings.TrimSpace(store.Path) == "" || len(challenges) == 0 {
		return challenges
	}
	file := store.load()
	now := store.now()
	dirty := false
	out := make([]Challenge, len(challenges))
	copy(out, challenges)
	for index := range out {
		if !challengeIsActive(out[index]) {
			continue
		}
		code := strings.TrimSpace(out[index].UniqueCode)
		entry, ok := file.Entries[code]
		if !ok {
			if err := store.RecordStart(code, stringifyDifficulty(out[index].Difficulty), 0); err != nil {
				return challenges
			}
			file = store.load()
			entry, ok = file.Entries[code]
			if !ok {
				return challenges
			}
		}
		if entry.Difficulty == "" {
			entry.Difficulty = strings.ToLower(strings.TrimSpace(stringifyDifficulty(out[index].Difficulty)))
			entry.BudgetMin = BudgetMinutes(out[index].Difficulty)
			file.Entries[code] = entry
			dirty = true
		}
		elapsed := int(now.Sub(entry.StartedAt) / time.Minute)
		if elapsed < 0 {
			elapsed = 0
		}
		budget := entry.BudgetMin
		if budget <= 0 {
			budget = BudgetMinutes(out[index].Difficulty)
		}
		over := elapsed > budget
		out[index].ElapsedMin = &elapsed
		out[index].BudgetMin = &budget
		out[index].OverBudget = &over
		attempt := entry.AttemptN
		out[index].AttemptN = &attempt
	}
	if dirty {
		_ = store.save(file)
	}
	return out
}
