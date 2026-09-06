package fgs_test

import (
	"fmt"
	"testing"

	"pentest/internal/fgs"
)

func TestMailboxScanAcceptsBoundedPagesWithoutLosingUpdates(t *testing.T) {
	s, c := fixture(t)
	t.Cleanup(func() { s.CloseScans() })
	for i := 0; i < 7; i++ {
		_, err := fgs.Emit(t.Context(), c, "scan", []fgs.Operation{{Op: "goal.create", Key: fmt.Sprintf("goal:%d", i), Title: "Check", SuccessCriteria: "Checked"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	complete := false
	for i := 0; i < 30; i++ {
		page, err := s.ReceivePage(t.Context(), c, "scan", 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Receipts) > 2 {
			t.Fatalf("unbounded receipt page: %d", len(page.Receipts))
		}
		if page.Complete {
			complete = true
			break
		}
	}
	if !complete {
		t.Fatal("scan did not finish")
	}
	graph, err := s.Read(t.Context(), c)
	if err != nil || len(graph.Nodes) != 7 {
		t.Fatalf("graph: %+v %v", graph, err)
	}
}

func TestMailboxPagesFindRepairBeyondBlockedPageAfterScanRestart(t *testing.T) {
	s, c := fixture(t)
	t.Cleanup(func() { s.CloseScans() })
	first, err := fgs.Emit(t.Context(), c, "scan", []fgs.Operation{{Op: "step.create", Key: "step:missing", Goal: "goal:missing", Action: "Check"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err = fgs.Emit(t.Context(), c, "scan", []fgs.Operation{{Op: "goal.create", Key: fmt.Sprintf("goal:%d", i), Title: "Check", SuccessCriteria: "Checked"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = fgs.EmitResolution(t.Context(), c, "scan", fgs.Identity{ContinuationID: "scan", IntentID: first.ID}, nil, "Not needed"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReceivePage(t.Context(), c, "scan", 2); err != nil {
		t.Fatal(err)
	}
	s.CloseScans()
	for i := 0; i < 50; i++ {
		if _, err = s.ReceivePage(t.Context(), c, "scan", 2); err != nil {
			t.Fatal(err)
		}
		graph, err := s.Read(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		if len(graph.Nodes) == 5 {
			return
		}
	}
	t.Fatal("repair beyond the blocked page did not release queued updates")
}
