package blackboardv2_test

import (
	"context"
	"reflect"
	"testing"

	"pentest/internal/blackboardv2"
)

func TestRetainEvidenceRedirectsKeepInputLinksAndReplay(t *testing.T) {
	ctx := context.Background()
	fixture := newEvidenceV2Fixture(t, "Redirected Evidence links")
	if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "prepare-link-redirect",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:source", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Source", Locator: "same.example", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:canonical", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Canonical", Locator: "SAME.EXAMPLE.", ScopeStatus: "in_scope"}},
			{Op: "merge", Source: "entity:source", SourceVersion: 1, Canonical: "entity:canonical", CanonicalVersion: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	fixture.writeSource(t, "proof.txt", "proof\n")
	for _, invalid := range []bool{false, true} {
		name := "success"
		if invalid {
			name = "missing-target"
		}
		t.Run(name, func(t *testing.T) {
			request := blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: name, Key: "evidence:" + name, Attempt: "attempt:evidence",
				SourcePath: "proof.txt", ArtifactType: "text", Summary: "Proof about a redirected Entity",
				Links: []blackboardv2.EvidenceLink{{"about", "entity:source"}},
			}
			if invalid {
				request.Links = append(request.Links, blackboardv2.EvidenceLink{"about", "entity:missing"})
			}
			original := append([]blackboardv2.EvidenceLink(nil), request.Links...)
			result, err := fixture.service.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, request)
			if !reflect.DeepEqual(request.Links, original) {
				t.Fatalf("input links changed: got %#v, want %#v", request.Links, original)
			}
			if invalid {
				if !isSemanticCode(err, "not_found") {
					t.Fatalf("missing link target = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			replay, err := fixture.service.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, request)
			if err != nil || !reflect.DeepEqual(replay, result) {
				t.Fatalf("replay with the same request = %#v, %v; want %#v", replay, err, result)
			}
			detail, err := fixture.service.ReadCurrent(ctx, fixture.projectID, request.Key)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, relation := range detail.Relationships {
				if relation[0] == request.Key && relation[1] == "about" && relation[2] == "entity:canonical" {
					found = true
				}
			}
			if !found {
				t.Fatalf("canonical link missing: %#v", detail.Relationships)
			}
		})
	}
}
