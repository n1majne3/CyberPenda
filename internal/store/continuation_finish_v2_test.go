package store

import (
	"context"
	"testing"
)

func TestBlackboardV2FinishReceiptSchemaIsDurableAndLegacyIndependent(t *testing.T) {
	db, err := Open("")
	if err != nil {
		t.Fatalf("open Store: %v", err)
	}
	defer db.Close()

	var sqlText string
	if err := db.QueryRowContext(context.Background(), `
		SELECT sql FROM sqlite_master
		WHERE type='table' AND name='blackboard_v2_continuation_finishes'`,
	).Scan(&sqlText); err != nil {
		t.Fatalf("read Blackboard v2 Finish receipt schema: %v", err)
	}
	for _, forbidden := range []string{"task_summary", "objective_outcome", "mechanical_handoff", "goal"} {
		if containsFold(sqlText, forbidden) {
			t.Errorf("v2 Finish schema contains forbidden legacy state %q: %s", forbidden, sqlText)
		}
	}
}

func containsFold(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		match := true
		for j := range fragment {
			a, b := value[i+j], fragment[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
