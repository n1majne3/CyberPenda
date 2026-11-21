package blackboardv2

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"pentest/internal/project"
)

func TestHealthRedirectsReuseLoadedRecordTypes(t *testing.T) {
	for _, withRedirects := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "redirects"}[withRedirects], func(t *testing.T) {
			db, recorder := newAuditQueryStore(t)
			projects := project.NewService(db)
			owner, err := projects.Create("Health owner", "", project.Scope{}, project.Defaults{})
			if err != nil {
				t.Fatal(err)
			}
			foreign, err := projects.Create("Other owner", "", project.Scope{}, project.Defaults{})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			service := NewService(db)
			for projectID, keys := range map[string][]string{
				owner.ID:   {"entity:canonical", "entity:source-current", "entity:middle", "entity:missing-source"},
				foreign.ID: {"entity:foreign"},
			} {
				changes := make([]Change, 0, len(keys))
				for _, key := range keys {
					changes = append(changes, Change{Op: "create", Key: key, Type: "entity", Record: EntityRecord{Status: "active", Kind: "host", Name: key, ScopeStatus: "in_scope"}})
				}
				if _, err := service.Apply(ctx, projectID, ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "seed", Changes: changes}); err != nil {
					t.Fatal(err)
				}
			}
			if withRedirects {
				for _, pair := range [][2]string{
					{"entity:ok", "entity:canonical"},
					{"entity:missing", "entity:absent"},
					{"entity:foreign-target", "entity:foreign"},
					{"entity:source-current", "entity:canonical"},
					{"entity:chain", "entity:middle"},
					{"entity:middle", "entity:canonical"},
					{"entity:missing-source", "entity:absent"},
				} {
					if _, err := db.Exec(`INSERT INTO blackboard_v2_key_redirects(project_id,source_key,canonical_key,created_at) VALUES(?,?,?,'2026-01-01T00:00:00Z')`, owner.ID, pair[0], pair[1]); err != nil {
						t.Fatal(err)
					}
				}
			}
			recorder.mu.Lock()
			recorder.queries = nil
			recorder.mu.Unlock()
			health, err := service.ProjectSemanticHealth(ctx, owner.ID)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]HealthAnomaly, 0)
			for _, anomaly := range health.Anomalies {
				if anomaly.Code == "redirect_integrity" {
					got = append(got, anomaly)
				}
			}
			want := make([]HealthAnomaly, 0)
			if withRedirects {
				for _, item := range [][3]string{
					{"entity:chain", "entity:middle", "Blackboard Key Redirect forms a chain instead of a single hop."},
					{"entity:foreign-target", "entity:foreign", "Blackboard Key Redirect points at a missing canonical record."},
					{"entity:middle", "entity:canonical", "Blackboard Key Redirect source still has a current record."},
					{"entity:missing", "entity:absent", "Blackboard Key Redirect points at a missing canonical record."},
					{"entity:missing-source", "entity:absent", "Blackboard Key Redirect points at a missing canonical record."},
					{"entity:source-current", "entity:canonical", "Blackboard Key Redirect source still has a current record."},
				} {
					want = append(want, HealthAnomaly{Code: "redirect_integrity", Severity: HealthSeverityCritical, SubjectKey: item[0], RelatedKeys: []string{item[1]}, Message: item[2]})
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("redirect anomalies = %#v; want %#v", got, want)
			}
			recorder.mu.Lock()
			queries := append([]conclusionQuery(nil), recorder.queries...)
			recorder.mu.Unlock()
			redirectReads := 0
			for _, query := range queries {
				flat := strings.Join(strings.Fields(query.sql), " ")
				if strings.Contains(flat, "FROM blackboard_v2_key_redirects") {
					redirectReads++
				}
				if strings.Contains(flat, "blackboard_v2_records WHERE project_id = ? AND key = ?") {
					t.Errorf("health queried a redirect endpoint instead of its loaded map: %s", flat)
				}
			}
			if redirectReads != 1 {
				t.Fatalf("redirect reads = %d; want 1", redirectReads)
			}
		})
	}
}
