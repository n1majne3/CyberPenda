package blackboardv2_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"testing"
)

func BenchmarkProjectSemanticHealthEvidencePolling(b *testing.B) {
	for _, cold := range []bool{true, false} {
		name := "warm"
		if cold {
			name = "cold"
		}
		b.Run(name, func(b *testing.B) {
			fixture := newHealthFixture(b, project.KindPentest)
			ctx := context.Background()
			_, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-health-benchmark",
				Changes: []blackboardv2.Change{
					{Op: "create", Key: "objective:poll", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Poll Evidence health"}},
					{Op: "create", Key: "attempt:poll", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Retain Evidence for health polling"}},
					{Op: "relate", From: "attempt:poll", Relation: "tests", To: "objective:poll"},
				},
			})
			if err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fixture.workdir, "health-poll.bin"), bytes.Repeat([]byte("a"), 1<<20), 0600); err != nil {
				b.Fatal(err)
			}
			_, err = fixture.service.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: "retain-health-benchmark", Key: "evidence:poll", Attempt: "attempt:poll", SourcePath: "health-poll.bin", ArtifactType: "text", Summary: "One MiB health payload",
			})
			if err != nil {
				b.Fatal(err)
			}
			if _, err := fixture.service.ProjectSemanticHealth(ctx, fixture.projectID); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				service := fixture.service
				if cold {
					service = blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
				}
				health, err := service.ProjectSemanticHealth(ctx, fixture.projectID)
				if err != nil {
					b.Fatal(err)
				}
				for _, anomaly := range health.Anomalies {
					if anomaly.Code == "evidence_integrity" {
						b.Fatal(anomaly.Message)
					}
				}
			}
		})
	}
}
