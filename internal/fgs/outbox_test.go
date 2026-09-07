package fgs_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"pentest/internal/fgs"
	"sync"
	"testing"
)

func TestNewContinuationCanRepairEarlierRejectionAfterQueuedWork(t *testing.T) {
	s, c := fixture(t)
	first, err := fgs.Emit(t.Context(), c, "old", []fgs.Operation{{Op: "step.create", Key: "step:missing", Goal: "goal:missing", Action: "Check"}})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := s.Drain(t.Context(), c, "old"); err != nil || !result.Blocked {
		t.Fatalf("reject: %+v %v", result, err)
	}
	if _, err = fgs.Emit(t.Context(), c, "new", []fgs.Operation{{Op: "goal.create", Key: "goal:new", Title: "New", SuccessCriteria: "Checked"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = fgs.EmitResolution(t.Context(), c, "new", fgs.Identity{ContinuationID: "old", IntentID: first.ID}, nil, "No longer needed"); err != nil {
		t.Fatal(err)
	}
	result, err := s.Drain(t.Context(), c, "new")
	if err != nil || result.Blocked {
		t.Fatalf("repair: %+v %v", result, err)
	}
	graph, err := s.Read(t.Context(), c)
	if err != nil || len(graph.Nodes) != 1 {
		t.Fatalf("graph: %+v %v", graph, err)
	}
}

func TestOutboxPublishesOrderedUpdatesAndRepairsLostReceipt(t *testing.T) {
	s, c := fixture(t)
	first, err := fgs.Emit(t.Context(), c, "continuation-1", []fgs.Operation{{Op: "goal.create", Key: "goal:access", Title: "Access", SuccessCriteria: "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := fgs.Emit(t.Context(), c, "continuation-1", []fgs.Operation{{Op: "step.create", Key: "step:check", Goal: "goal:access", Action: "Check access"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "intent_00000001" || second.ID != "intent_00000002" {
		t.Fatalf("identities: %+v %+v", first, second)
	}
	g, err := s.Read(t.Context(), c)
	if err != nil || len(g.Nodes) != 0 {
		t.Fatalf("publish mutated graph: %+v %v", g, err)
	}
	result, err := s.Drain(t.Context(), c, "continuation-1")
	if err != nil || result.Blocked || len(result.Receipts) != 2 {
		t.Fatalf("drain: %+v %v", result, err)
	}
	// The parent path is created after publication to force receipt delivery to fail.
	// All files in this fixture are created by this test or Emit/Drain.
	receiptDir := filepath.Join(c.Workdir, "graph", "receipts", "continuation-2")
	if err = os.MkdirAll(filepath.Dir(receiptDir), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(receiptDir, []byte("blocked receipt directory"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = fgs.Emit(t.Context(), c, "continuation-2", []fgs.Operation{{Op: "step.transition", Key: "step:check", From: "open", To: "running"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Drain(t.Context(), c, "continuation-2"); err == nil {
		t.Fatal("receipt delivery should fail")
	}
	if err = os.Remove(receiptDir); err != nil {
		t.Fatal(err)
	}
	result, err = s.Drain(t.Context(), c, "continuation-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Receipts) != 1 || result.Receipts[0].State != "applied" {
		t.Fatalf("retry: %+v", result)
	}
	g, err = s.Read(t.Context(), c)
	if err != nil || g.Revision != 3 || len(g.Nodes) != 2 {
		t.Fatalf("replay changed graph: %+v %v", g, err)
	}
	raw, err := os.ReadFile(filepath.Join(receiptDir, "intent_00000001.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r fgs.Receipt
	if err = json.Unmarshal(raw, &r); err != nil || r.Revision != 3 {
		t.Fatalf("file receipt: %+v %v", r, err)
	}
}

func TestRejectedHeadCanBeReplacedWithoutEditingPublishedFiles(t *testing.T) {
	s, c := fixture(t)
	// The schema is valid, but the Step refers to a Goal that is not accepted.
	_, err := fgs.Emit(t.Context(), c, "continuation-1", []fgs.Operation{{Op: "step.create", Key: "step:check", Goal: "goal:access", Action: "Check access"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fgs.Emit(t.Context(), c, "continuation-1", []fgs.Operation{{Op: "step.transition", Key: "step:check", From: "open", To: "running"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Drain(t.Context(), c, "continuation-1")
	if err != nil || !result.Blocked {
		t.Fatalf("rejection: %+v %v", result, err)
	}
	original := filepath.Join(c.Workdir, "graph", "outbox", "continuation-1", "intent_00000001.json")
	before, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fgs.EmitResolution(t.Context(), c, "continuation-1", fgs.Identity{ContinuationID: "continuation-1", IntentID: "intent_00000001"}, []fgs.Operation{
		{Op: "goal.create", Key: "goal:access", Title: "Access", SuccessCriteria: "ok"},
		{Op: "step.create", Key: "step:check", Goal: "goal:access", Action: "Check access"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	// This update depends on the earlier queued open -> running transition.
	if _, err = fgs.Emit(t.Context(), c, "continuation-1", []fgs.Operation{{Op: "step.transition", Key: "step:check", From: "running", To: "blocked", Reason: "Need access"}}); err != nil {
		t.Fatal(err)
	}
	result, err = s.Drain(t.Context(), c, "continuation-1")
	if err != nil || result.Blocked {
		t.Fatalf("repair: %+v %v", result, err)
	}
	after, err := os.ReadFile(original)
	if err != nil || string(before) != string(after) {
		t.Fatal("published file changed")
	}
	old, err := s.Receipt(t.Context(), c, "continuation-1", "intent_00000001")
	if err != nil || old.State != "superseded" {
		t.Fatalf("old receipt: %+v %v", old, err)
	}
	g, err := s.Read(t.Context(), c)
	if err != nil || g.Revision != 3 || len(g.Nodes) != 2 {
		t.Fatalf("graph: %+v %v", g, err)
	}
	for _, n := range g.Nodes {
		if n.Type == "step" && n.State != "blocked" {
			t.Fatalf("later update did not resume: %+v", n)
		}
	}
}

func TestConcurrentEmitDoesNotReuseIdentity(t *testing.T) {
	_, c := fixture(t)
	const count = 12
	var wg sync.WaitGroup
	ids := make(chan string, count)
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, err := fgs.Emit(t.Context(), c, "continuation-1", []fgs.Operation{{Op: "goal.create", Key: "goal:access", Title: "Access", SuccessCriteria: "ok"}})
			if err != nil {
				errs <- err
			} else {
				ids <- u.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Errorf("reused %s", id)
		}
		seen[id] = true
	}
	if len(seen) != count {
		t.Fatalf("published %d updates", len(seen))
	}
}

func TestOutboxRejectsSymlinkAndMalformedJSON(t *testing.T) {
	for _, mode := range []string{"symlink", "oversized", "unknown_field", "trailing_json"} {
		t.Run(mode, func(t *testing.T) {
			s, c := fixture(t)
			_, err := fgs.Emit(t.Context(), c, "continuation-1", []fgs.Operation{{Op: "goal.create", Key: "goal:access", Title: "Access", SuccessCriteria: "ok"}})
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(c.Workdir, "graph", "outbox", "continuation-1", "intent_00000002.json")
			raw := `{"schema":"fgs-update/v1","id":"intent_00000002","sequence":2,"operations":[],"unknown":true}`
			if mode == "trailing_json" {
				raw = `{"schema":"fgs-update/v1","id":"intent_00000002","sequence":2,"operations":[]} {}`
			}
			if mode == "oversized" {
				raw = string(make([]byte, fgs.MaxUpdateSize+1))
			}
			if mode == "symlink" {
				err = os.Symlink(filepath.Join(t.TempDir(), "outside"), file)
			} else {
				err = os.WriteFile(file, []byte(raw), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			result, drainErr := s.Drain(t.Context(), c, "continuation-1")
			{
				if drainErr != nil || !result.Blocked {
					t.Fatalf("malformed update has no repair receipt: %+v %v", result, drainErr)
				}
				if _, err = fgs.EmitResolution(t.Context(), c, "continuation-1", fgs.Identity{ContinuationID: "continuation-1", IntentID: "intent_00000002"}, nil, "Withdraw malformed update"); err != nil {
					t.Fatal(err)
				}
				if result, err = s.Drain(t.Context(), c, "continuation-1"); err != nil || result.Blocked {
					t.Fatalf("withdraw malformed: %+v %v", result, err)
				}
			}
			if _, err = fgs.Emit(t.Context(), c, "../escape", []fgs.Operation{{Op: "goal.create", Key: "goal:x", Title: "x", SuccessCriteria: "x"}}); err == nil {
				t.Fatal("unsafe Continuation accepted")
			}
		})
	}
}

func TestFailedResolutionDoesNotBecomeAnotherQueueBlocker(t *testing.T) {
	s, c := fixture(t)
	target := fgs.Identity{ContinuationID: "continuation-1", IntentID: "intent_00000001"}
	_, err := fgs.Emit(t.Context(), c, "continuation-1", []fgs.Operation{{Op: "step.create", Key: "step:bad", Goal: "goal:missing", Action: "Missing goal"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Drain(t.Context(), c, "continuation-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = fgs.EmitResolution(t.Context(), c, "continuation-1", target, []fgs.Operation{{Op: "step.create", Key: "step:bad", Goal: "goal:still-missing", Action: "Still missing"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Drain(t.Context(), c, "continuation-1")
	if err != nil || !result.Blocked {
		t.Fatalf("failed repair: %+v %v", result, err)
	}
	_, err = fgs.EmitResolution(t.Context(), c, "continuation-1", target, nil, "Withdraw work with no valid Goal")
	if err != nil {
		t.Fatal(err)
	}
	result, err = s.Drain(t.Context(), c, "continuation-1")
	if err != nil || result.Blocked {
		t.Fatalf("withdrawal: %+v %v", result, err)
	}
	g, err := s.Read(t.Context(), c)
	if err != nil || g.Revision != 0 || len(g.Nodes) != 0 {
		t.Fatalf("withdrawal changed graph: %+v %v", g, err)
	}
}

func TestInvalidRepairTargetGetsReceiptAndDoesNotStopLaterRecovery(t *testing.T) {
	for _, mode := range []string{"drain", "paged"} {
		t.Run(mode, func(t *testing.T) {
			s, c := fixture(t)
			const continuation = "continuation-1"
			settle := func() (fgs.DrainResult, error) {
				if mode == "drain" {
					return s.Drain(t.Context(), c, continuation)
				}
				for i := 0; i < 20; i++ {
					result, err := s.ReceivePage(t.Context(), c, continuation, 2)
					if err != nil || result.Complete {
						return result.DrainResult, err
					}
				}
				t.Fatal("paged scan did not complete")
				return fgs.DrainResult{}, nil
			}
			emit := func(target string, ops []fgs.Operation, reason string) fgs.Update {
				t.Helper()
				var u fgs.Update
				var err error
				if target == "" {
					u, err = fgs.Emit(t.Context(), c, continuation, ops)
				} else {
					u, err = fgs.EmitResolution(t.Context(), c, continuation, fgs.Identity{ContinuationID: continuation, IntentID: target}, ops, reason)
				}
				if err != nil {
					t.Fatal(err)
				}
				return u
			}
			bad := []fgs.Operation{{Op: "step.create", Key: "step:missing", Goal: "goal:missing", Action: "Check"}}
			first := emit("", bad, "")
			failedRepair := emit(first.ID, bad, "")
			nested := emit(failedRepair.ID, nil, "Wrong repair target")
			result, err := settle()
			status, statusErr := s.Status(t.Context(), c)
			if err != nil || statusErr != nil || status.ActionRequired != 1 {
				t.Fatalf("blocked head: %+v %v", result, err)
			}
			receipt, err := s.Receipt(t.Context(), c, continuation, nested.ID)
			if err != nil || receipt.State != "action_required" || receipt.Code != "invalid_resolution" {
				t.Fatalf("wrong target needs a durable error, not a missing receipt: %+v %v", receipt, err)
			}
			emit(first.ID, nil, "Withdraw original batch")
			stale := emit(first.ID, nil, "Already withdrawn")
			next := emit("", []fgs.Operation{{Op: "goal.create", Key: "goal:next", Title: "Next", SuccessCriteria: "Checked"}}, "")
			result, err = settle()
			status, statusErr = s.Status(t.Context(), c)
			if err != nil || statusErr != nil || status.ActionRequired != 0 {
				t.Fatalf("recovery: %+v %v", result, err)
			}
			for _, id := range []string{nested.ID, stale.ID, next.ID} {
				raw, err := os.ReadFile(filepath.Join(c.Workdir, "graph", "receipts", continuation, id+".json"))
				if err != nil {
					t.Fatal(err)
				}
				var r fgs.Receipt
				if err := json.Unmarshal(raw, &r); err != nil {
					t.Fatal(err)
				}
				want := "action_required"
				if id == next.ID {
					want = "applied"
				}
				if r.State != want {
					t.Fatalf("receipt %s: %+v", id, r)
				}
			}
			graph, err := s.Read(t.Context(), c)
			if err != nil || len(graph.Nodes) != 1 || graph.Nodes[0].Key != "goal:next" {
				t.Fatalf("graph: %+v %v", graph, err)
			}
		})
	}
}
