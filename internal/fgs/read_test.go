package fgs_test

import (
	"pentest/internal/fgs"
	"testing"
)

func TestGraphPagesKeepEdgesAndBoundNodePayloads(t *testing.T) {
	s, c := fixture(t)
	submit(t, s, c, 1, fgs.Operation{Op: "goal.create", Key: "goal:a", Title: "A", SuccessCriteria: "Checked"}, fgs.Operation{Op: "step.create", Key: "step:b", Goal: "goal:a", Action: "Check"})
	first, err := s.ReadPage(t.Context(), c, "", 1)
	if err != nil || len(first.Nodes) != 1 || first.NextCursor != "goal:a" {
		t.Fatalf("first: %+v %v", first, err)
	}
	next, err := s.ReadPage(t.Context(), c, first.NextCursor, 1)
	if err != nil || len(next.Nodes) != 1 || len(next.Edges) != 1 || next.Edges[0].To != "goal:a" || next.NextCursor != "" {
		t.Fatalf("next: %+v %v", next, err)
	}
}

func TestDeliveryStatusTracksRejectionAndWithdrawal(t *testing.T) {
 s,c:=fixture(t)
 submit(t,s,c,1,fgs.Operation{Op:"step.create",Key:"step:a",Goal:"goal:missing",Action:"Check"})
 status,err:=s.Status(t.Context(),c)
 if err!=nil || status.ActionRequired!=1 {t.Fatalf("rejected: %+v %v",status,err)}
 _,err=s.Apply(t.Context(),c,"continuation-1",fgs.Update{Schema:fgs.Schema,ID:"intent_00000002",Sequence:2,Resolves:&fgs.Identity{ContinuationID:"continuation-1",IntentID:"intent_00000001"},WithdrawalReason:"Not needed"})
 if err!=nil {t.Fatal(err)}
 status,err=s.Status(t.Context(),c)
 if err!=nil || status.ActionRequired!=0 || len(status.Receipts)!=2 || status.LastAcceptedAt=="" {t.Fatalf("withdrawn: %+v %v",status,err)}
}

func TestStepPriorityAndFactDataReferencesSurviveAcceptance(t *testing.T) {
 s,c:=fixture(t)
 r:=submit(t,s,c,1,fgs.Operation{Op:"goal.create",Key:"goal:a",Title:"A",SuccessCriteria:"Checked"},fgs.Operation{Op:"step.create",Key:"step:b",Goal:"goal:a",Action:"Check",Priority:"high",Executor:"execute-1"},fgs.Operation{Op:"fact.append",Key:"fact:c",Step:"step:b",Summary:"Checked",DataRefs:[]string{"graph/data/response.txt"}})
 if r.State!="applied" {t.Fatalf("accept: %+v",r)}
 history,err:=s.History(t.Context(),c,"step:b");if err!=nil || history[0].Priority!="high" || history[0].Executor!="execute-1" {t.Fatalf("step: %+v %v",history,err)}
 history,err=s.History(t.Context(),c,"fact:c");if err!=nil || len(history[0].DataRefs)!=1 || history[0].AcceptedAt=="" {t.Fatalf("fact: %+v %v",history,err)}
}

func TestHistoryPagesReturnNewestVersionsFirst(t *testing.T) {
 s,c:=fixture(t)
 submit(t,s,c,1,fgs.Operation{Op:"goal.create",Key:"goal:a",Title:"A",SuccessCriteria:"Checked"})
 submit(t,s,c,2,fgs.Operation{Op:"goal.describe",Key:"goal:a",Title:"Updated",Expected:map[string]string{"title":"A"}})
 page,err:=s.HistoryPage(t.Context(),c,"goal:a",0,1)
 if err!=nil || len(page)!=1 || page[0].Version!=2 {t.Fatalf("newest: %+v %v",page,err)}
 page,err=s.HistoryPage(t.Context(),c,"goal:a",2,1)
 if err!=nil || len(page)!=1 || page[0].Version!=1 {t.Fatalf("older: %+v %v",page,err)}
}
