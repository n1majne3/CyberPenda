package fgs_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/fgs"
	"pentest/internal/owner"
	"pentest/internal/store"
)

func fixture(t *testing.T) (*fgs.Service, owner.Contract) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return fgs.NewService(db), owner.NewSessionContract("session-a", t.TempDir())
}

func submit(t *testing.T, s *fgs.Service, c owner.Contract, sequence int, ops ...fgs.Operation) fgs.Receipt {
	t.Helper()
	r, err := s.Apply(t.Context(), c, "continuation-1", fgs.Update{Schema: fgs.Schema, ID: fmt.Sprintf("intent_%08d", sequence), Sequence: sequence, Operations: ops})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDescriptionsAndGoalReopenKeepHistoryButStepRetryNeedsNewNode(t *testing.T) {
	s, c := fixture(t)
	r := submit(t, s, c, 1,
		fgs.Operation{Op: "goal.create", Key: "goal:access", Title: "Check access", SuccessCriteria: "Receive ok"},
		fgs.Operation{Op: "step.create", Key: "step:check", Goal: "goal:access", Action: "Read health endpoint"},
		fgs.Operation{Op: "step.transition", Key: "step:check", From: "open", To: "running", Executor: "worker-a"})
	if r.State != "applied" {
		t.Fatalf("start: %+v", r)
	}
	r = submit(t, s, c, 2, fgs.Operation{Op: "step.describe", Key: "step:check", Action: "Read the HTTP health endpoint", Expected: map[string]string{"action": "Read health endpoint"}},
		fgs.Operation{Op: "fact.append", Key: "fact:ok", Step: "step:check", Summary: "Health returned ok"},
		fgs.Operation{Op: "step.transition", Key: "step:check", From: "running", To: "done", Outputs: []string{"fact:ok"}},
		fgs.Operation{Op: "goal.transition", Key: "goal:access", From: "open", To: "done", Facts: []string{"fact:ok"}, Summary: "Health verified"})
	if r.State != "applied" {
		t.Fatalf("complete: %+v", r)
	}
	r = submit(t, s, c, 3, fgs.Operation{Op: "goal.transition", Key: "goal:access", From: "done", To: "open", Reason: "Check after network change"},
		fgs.Operation{Op: "step.create", Key: "step:recheck", Goal: "goal:access", Action: "Check the new response", Inputs: []string{"fact:ok"}})
	if r.State != "applied" {
		t.Fatalf("reopen: %+v", r)
	}
	history, err := s.History(t.Context(), c, "step:check")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 4 || history[0].Action != "Read health endpoint" || history[3].Action != "Read the HTTP health endpoint" || history[3].State != "done" {
		t.Fatalf("history: %+v", history)
	}
	r = submit(t, s, c, 4, fgs.Operation{Op: "step.transition", Key: "step:check", From: "done", To: "running"})
	if r.State != "action_required" {
		t.Fatalf("terminal Step reopened: %+v", r)
	}
}

func TestAcceptedGraphAndReceiptSurviveRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	contract := owner.NewSessionContract("session-a", t.TempDir())
	service := fgs.NewService(db)
	update := fgs.Update{Schema: fgs.Schema, ID: "intent_00000001", Sequence: 1, Operations: []fgs.Operation{
		{Op: "goal.create", Key: "goal:access", Title: "Check access", SuccessCriteria: "Health response is ok"},
		{Op: "step.create", Key: "step:check", Goal: "goal:access", Action: "Read health endpoint"},
		{Op: "fact.append", Key: "fact:ok", Step: "step:check", Summary: "Health response is ok"},
		{Op: "step.transition", Key: "step:check", From: "open", To: "done", Outputs: []string{"fact:ok"}},
		{Op: "goal.transition", Key: "goal:access", From: "open", To: "done", Facts: []string{"fact:ok"}, Summary: "Access confirmed"},
	}}
	receipt, err := service.Apply(ctx, contract, "continuation-1", update)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != "applied" || receipt.Revision != 1 {
		t.Fatalf("receipt = %+v", receipt)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service = fgs.NewService(db)
	replay, err := service.Apply(ctx, contract, "continuation-1", update)
	if err != nil {
		t.Fatal(err)
	}
	if replay != receipt {
		t.Fatalf("replay = %+v, want %+v", replay, receipt)
	}
	graph, err := service.Read(ctx, contract)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Revision != 1 || len(graph.Nodes) != 3 || len(graph.Edges) != 3 {
		t.Fatalf("graph = %+v", graph)
	}
	for _, n := range graph.Nodes {
		if n.Key == "step:check" && n.State != "done" {
			t.Fatalf("step = %+v", n)
		}
	}
	got, err := service.Receipt(ctx, contract, "continuation-1", update.ID)
	if err != nil || got != receipt {
		t.Fatalf("receipt read = %+v, %v", got, err)
	}
}

func TestInvalidBatchLeavesNoPartialGraph(t *testing.T) {
	for _, bad := range []fgs.Operation{
		{Op: "fact.append", Key: "fact:bad", Step: "step:missing", Summary: "Unknown producer"},
		{Op: "goal.create", Key: "goal:other", Title: "Other", SuccessCriteria: "ok", Action: "wrong field"},
		{Op: "entity.create", Key: "entity:host", Title: "Legacy type"},
		{Op: "goal.create", Key: "../escape", Title: "Bad key", SuccessCriteria: "ok"},
	} {
		t.Run(bad.Op+bad.Key, func(t *testing.T) {
			s, c := fixture(t)
			r := submit(t, s, c, 1, fgs.Operation{Op: "goal.create", Key: "goal:access", Title: "Access", SuccessCriteria: "ok"}, bad)
			if r.State != "action_required" || r.Operation != 1 {
				t.Fatalf("receipt: %+v", r)
			}
			g, err := s.Read(t.Context(), c)
			if err != nil {
				t.Fatal(err)
			}
			if len(g.Nodes) != 0 || g.Revision != 0 {
				t.Fatalf("partial graph: %+v", g)
			}
		})
	}
	s, c := fixture(t)
	r := submit(t, s, c, 1)
	if r.State != "action_required" {
		t.Fatalf("empty update accepted: %+v", r)
	}
}

func TestProjectSharingSessionIsolationAndContinuationIdentity(t *testing.T) {
	s, c := fixture(t)
	a := owner.NewTaskContract("task-a", "project-a", c.Workdir)
	b := owner.NewTaskContract("task-b", "project-a", c.Workdir)
	r := submit(t, s, a, 1, fgs.Operation{Op: "goal.create", Key: "goal:shared", Title: "Shared", SuccessCriteria: "ok"})
	if r.State != "applied" {
		t.Fatal(r)
	}
	g, err := s.Read(t.Context(), b)
	if err != nil || len(g.Nodes) != 1 {
		t.Fatalf("shared: %+v %v", g, err)
	}
	for _, other := range []owner.Contract{c, owner.NewTaskContract("task-c", "project-b", c.Workdir), owner.NewSessionContract("project-a", c.Workdir)} {
		g, err = s.Read(t.Context(), other)
		if err != nil || len(g.Nodes) != 0 {
			t.Fatalf("isolated: %+v %v", g, err)
		}
	}
	u := fgs.Update{Schema: fgs.Schema, ID: "intent_00000001", Sequence: 1, Operations: []fgs.Operation{{Op: "goal.create", Key: "goal:next", Title: "Next", SuccessCriteria: "ok"}}}
	r, err = s.Apply(t.Context(), a, "continuation-2", u)
	if err != nil || r.State != "applied" {
		t.Fatalf("new Continuation: %+v %v", r, err)
	}
	u.Operations[0].Title = "changed"
	if _, err = s.Apply(t.Context(), a, "continuation-2", u); err == nil {
		t.Fatal("changed replay accepted")
	}
	r = submit(t, s, b, 1, fgs.Operation{Op: "goal.transition", Key: "goal:shared", From: "open", To: "active"})
	if r.State != "applied" {
		t.Fatal(r)
	}
	r = submit(t, s, a, 2, fgs.Operation{Op: "goal.transition", Key: "goal:shared", From: "open", To: "abandoned", Reason: "stale"})
	if r.State != "action_required" {
		t.Fatalf("stale report accepted: %+v", r)
	}
}

func TestFactCorrectionAndGoalHierarchyKeepCausalLinks(t *testing.T) {
	s, c := fixture(t)
	r := submit(t, s, c, 1,
		fgs.Operation{Op: "goal.create", Key: "goal:access", Title: "Access", SuccessCriteria: "ok"},
		fgs.Operation{Op: "goal.create", Key: "goal:health", Title: "Health", SuccessCriteria: "valid response", ParentGoal: "goal:access"},
		fgs.Operation{Op: "step.create", Key: "step:check", Goal: "goal:health", Action: "Check response"},
		fgs.Operation{Op: "fact.append", Key: "fact:failure", Step: "step:check", Summary: "No response"},
		fgs.Operation{Op: "step.transition", Key: "step:check", From: "open", To: "done", Outputs: []string{"fact:failure"}},
		fgs.Operation{Op: "step.create", Key: "step:recheck", Goal: "goal:health", Action: "Check response again", Inputs: []string{"fact:failure"}, After: []string{"step:check"}},
		fgs.Operation{Op: "fact.append", Key: "fact:correction", Step: "step:recheck", Summary: "Earlier request used the wrong endpoint", Corrects: "fact:failure"},
		fgs.Operation{Op: "step.transition", Key: "step:recheck", From: "open", To: "done", Outputs: []string{"fact:correction"}},
		fgs.Operation{Op: "goal.transition", Key: "goal:health", From: "open", To: "done", Facts: []string{"fact:correction"}, Summary: "Response checked"},
		fgs.Operation{Op: "goal.transition", Key: "goal:access", From: "open", To: "done", Facts: []string{"fact:correction"}, Summary: "Access checked"})
	if r.State != "applied" {
		t.Fatalf("graph rejected: %+v", r)
	}
	g, err := s.Read(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	want := []fgs.Edge{{From: "goal:health", Relation: "part_of", To: "goal:access"}, {From: "step:recheck", Relation: "depends_on", To: "step:check"}, {From: "fact:correction", Relation: "corrects", To: "fact:failure"}}
	for _, edge := range want {
		found := false
		for _, got := range g.Edges {
			if got == edge {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %+v in %+v", edge, g.Edges)
		}
	}
	r = submit(t, s, c, 2, fgs.Operation{Op: "goal.transition", Key: "goal:health", From: "done", To: "open", Reason: "more checks"})
	if r.State != "action_required" {
		t.Fatalf("child reopened under done parent: %+v", r)
	}
}

func TestRejectedUpdatesExplainHowToRepair(t *testing.T) {
	cases := []struct {
		name string
		op   fgs.Operation
		want []string
	}{
		{"missing output", fgs.Operation{Op: "step.transition", Key: "step:check", From: "open", To: "done", Outputs: []string{"fact:missing"}}, []string{"fact:missing", "does not exist", "before"}},
		{"wrong Step state", fgs.Operation{Op: "step.transition", Key: "step:check", From: "running", To: "done", Outputs: []string{"fact:missing"}}, []string{"step:check", "open", "running", "from"}},
		{"wrong Goal state", fgs.Operation{Op: "goal.transition", Key: "goal:check", From: "active", To: "done"}, []string{"goal:check", "open", "active", "from"}},
		{"missing new description", fgs.Operation{Op: "step.describe", Key: "step:check", Expected: map[string]string{"action": "New action"}}, []string{"top-level", "expected", "current"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, c := fixture(t)
			submit(t, s, c, 1, fgs.Operation{Op: "goal.create", Key: "goal:check", Title: "Check", SuccessCriteria: "Checked"}, fgs.Operation{Op: "step.create", Key: "step:check", Goal: "goal:check", Action: "Check response"})
			r := submit(t, s, c, 2, tc.op)
			if r.State != "action_required" {
				t.Fatalf("receipt: %+v", r)
			}
			for _, part := range tc.want {
				if !strings.Contains(r.Message, part) {
					t.Errorf("message %q missing %q", r.Message, part)
				}
			}
		})
	}
}
