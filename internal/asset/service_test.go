package asset

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/project"
	"pentest/internal/store"
)

func newAssetTestServices(t *testing.T) (*Service, *project.Service, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	return NewService(db, projects), projects, db
}

func createAssetProject(t *testing.T, projects *project.Service, scope project.Scope) project.Project {
	t.Helper()
	created, err := projects.Create("Asset Project", "", scope, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return created
}

func operatorOrigin() Origin {
	return Origin{Kind: OriginOperator, ActorID: "test-operator"}
}

func insertFGSNode(t *testing.T, db *store.DB, projectID, key, nodeType string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO fgs_nodes (board_kind,board_id,node_key,body_json) VALUES('project',?,?,?)`,
		projectID, key, `{"key":"`+key+`","type":"`+nodeType+`","version":1,"state":"accepted","accepted_at":"2026-01-01T00:00:00Z"}`)
	if err != nil {
		t.Fatalf("insert FGS node: %v", err)
	}
}

func TestUpsertNormalizesDedupesAndInfersParents(t *testing.T) {
	service, projects, _ := newAssetTestServices(t)
	created := createAssetProject(t, projects, project.Scope{Domains: []string{"example.com"}, Ports: []string{"443"}})

	result, err := service.Upsert(context.Background(), created.ID, []Input{
		{Type: TypeRootDomain, Value: "EXAMPLE.com."},
		{Type: TypeSubdomain, Value: "API.EXAMPLE.COM"},
		{Type: TypeService, Value: "https://api.example.com:443"},
		{Type: TypeEndpoint, Value: "https://api.example.com:443/login?b=2&a=1#fragment"},
		{Type: TypeEndpoint, Value: "https://api.example.com/login?a=1&b=2"},
	}, operatorOrigin())
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if result.Created != 4 || result.Updated != 0 || len(result.Assets) != 4 {
		t.Fatalf("unexpected batch result: %#v", result)
	}
	byType := map[Type]View{}
	for _, item := range result.Assets {
		byType[item.Type] = item
		if item.ScopeStatus != ScopeStatusInScope {
			t.Fatalf("%s status = %q", item.NormalizedValue, item.ScopeStatus)
		}
	}
	if byType[TypeRootDomain].NormalizedValue != "example.com" {
		t.Fatalf("root normalization: %#v", byType[TypeRootDomain])
	}
	if byType[TypeSubdomain].ParentID != byType[TypeRootDomain].ID {
		t.Fatal("subdomain did not infer root parent")
	}
	if byType[TypeService].ParentID != byType[TypeSubdomain].ID {
		t.Fatalf("service did not infer host parent: service=%#v subdomain=%#v", byType[TypeService], byType[TypeSubdomain])
	}
	if byType[TypeEndpoint].ParentID != byType[TypeService].ID {
		t.Fatal("endpoint did not infer service parent")
	}
	if byType[TypeEndpoint].NormalizedValue != "https://api.example.com/login?a=1&b=2" {
		t.Fatalf("endpoint normalization: %q", byType[TypeEndpoint].NormalizedValue)
	}

	replay, err := service.Upsert(context.Background(), created.ID, []Input{
		{Type: TypeRootDomain, Value: "example.com"},
		{Type: TypeSubdomain, Value: "api.example.com"},
		{Type: TypeService, Value: "https://api.example.com:443"},
		{Type: TypeEndpoint, Value: "https://api.example.com/login?a=1&b=2"},
	}, operatorOrigin())
	if err != nil {
		t.Fatalf("replay upsert: %v", err)
	}
	if replay.Created != 0 || replay.Updated != 4 {
		t.Fatalf("idempotent replay counts = created %d updated %d", replay.Created, replay.Updated)
	}
}

func TestAnchorToFactCountsCoverageAndIsIdempotent(t *testing.T) {
	service, projects, db := newAssetTestServices(t)
	created := createAssetProject(t, projects, project.Scope{Domains: []string{"example.com"}, Ports: []string{"443"}})
	result, err := service.Upsert(context.Background(), created.ID, []Input{
		{Type: TypeRootDomain, Value: "example.com"},
		{Type: TypeEndpoint, Value: "https://example.com/"},
	}, operatorOrigin())
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	var endpoint View
	for _, item := range result.Assets {
		if item.Type == TypeEndpoint {
			endpoint = item
		}
	}
	if endpoint.ID == "" {
		t.Fatal("missing endpoint")
	}
	insertFGSNode(t, db, created.ID, "fact-1", "fact")
	first, err := service.AnchorNode(context.Background(), created.ID, endpoint.ID, "fact-1", operatorOrigin())
	if err != nil {
		t.Fatalf("anchor: %v", err)
	}
	second, err := service.AnchorNode(context.Background(), created.ID, endpoint.ID, "fact-1", operatorOrigin())
	if err != nil {
		t.Fatalf("anchor replay: %v", err)
	}
	if first.NodeKey != second.NodeKey || second.NodeType != "fact" {
		t.Fatalf("unexpected anchors: %#v %#v", first, second)
	}
	coverage, err := service.Coverage(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if coverage.TotalAssets != 2 || coverage.InScopeAssets != 2 || coverage.CoveredInScopeAssets != 1 || coverage.CoveragePercent != 50 {
		t.Fatalf("unexpected coverage: %#v", coverage)
	}
	if len(coverage.UncoveredInScopeAssetIDs) != 1 {
		t.Fatalf("uncovered ids: %#v", coverage.UncoveredInScopeAssetIDs)
	}
}

func TestAnchorRejectsUnknownAssetAndUnknownNode(t *testing.T) {
	service, projects, _ := newAssetTestServices(t)
	created := createAssetProject(t, projects, project.Scope{})
	if _, err := service.AnchorNode(context.Background(), created.ID, "missing", "fact-1", operatorOrigin()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing asset error = %v", err)
	}
	result, err := service.Upsert(context.Background(), created.ID, []Input{{Type: TypeRootDomain, Value: "example.com"}}, operatorOrigin())
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := service.AnchorNode(context.Background(), created.ID, result.Assets[0].ID, "missing", operatorOrigin()); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("missing node error = %v", err)
	}
}

func TestScopeStatusIsDerivedOnRead(t *testing.T) {
	service, projects, _ := newAssetTestServices(t)
	created := createAssetProject(t, projects, project.Scope{Domains: []string{"other.example"}})
	result, err := service.Upsert(context.Background(), created.ID, []Input{{Type: TypeSubdomain, Value: "api.example.com"}}, operatorOrigin())
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if result.Assets[0].ScopeStatus != ScopeStatusOutOfScope {
		t.Fatalf("initial status = %q", result.Assets[0].ScopeStatus)
	}
	if _, err := projects.Update(created.ID, created.Name, created.Description, project.Scope{Domains: []string{"example.com"}, Excluded: []string{"api.example.com"}}, true, created.Defaults, false); err != nil {
		t.Fatalf("update project: %v", err)
	}
	views, err := service.List(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if views[0].ScopeStatus != ScopeStatusExcluded {
		t.Fatalf("derived status after Scope update = %q", views[0].ScopeStatus)
	}
}

func TestImportCopiesAssetsWithoutAnchors(t *testing.T) {
	service, projects, db := newAssetTestServices(t)
	source := createAssetProject(t, projects, project.Scope{Domains: []string{"example.com"}})
	target := createAssetProject(t, projects, project.Scope{})
	result, err := service.Upsert(context.Background(), source.ID, []Input{
		{Type: TypeRootDomain, Value: "example.com"},
		{Type: TypeSubdomain, Value: "api.example.com"},
	}, operatorOrigin())
	if err != nil {
		t.Fatalf("upsert source: %v", err)
	}
	insertFGSNode(t, db, source.ID, "fact-1", "fact")
	if _, err := service.AnchorNode(context.Background(), source.ID, result.Assets[0].ID, "fact-1", operatorOrigin()); err != nil {
		t.Fatalf("anchor source: %v", err)
	}
	imported, err := service.Import(context.Background(), target.ID, source.ID, nil, operatorOrigin())
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imported.Created != 2 || len(imported.Assets) != 2 {
		t.Fatalf("import result: %#v", imported)
	}
	for _, item := range imported.Assets {
		if item.ImportedFromProjectID != source.ID || item.ImportedFromAssetID == "" {
			t.Fatalf("import provenance missing: %#v", item)
		}
		if len(item.Anchors) != 0 {
			t.Fatalf("anchors copied: %#v", item.Anchors)
		}
	}
	var subdomain View
	for _, item := range imported.Assets {
		if item.Type == TypeSubdomain {
			subdomain = item
		}
	}
	if subdomain.ParentID == "" {
		t.Fatal("imported hierarchy missing")
	}
	replay, err := service.Import(context.Background(), target.ID, source.ID, nil, operatorOrigin())
	if err != nil {
		t.Fatalf("import replay: %v", err)
	}
	if replay.Created != 0 || replay.Updated != 2 {
		t.Fatalf("import replay counts = created %d updated %d", replay.Created, replay.Updated)
	}
}

func TestUpsertRejectsInvalidInputAndParent(t *testing.T) {
	service, projects, _ := newAssetTestServices(t)
	created := createAssetProject(t, projects, project.Scope{})
	if _, err := service.Upsert(context.Background(), created.ID, []Input{{Type: TypeEndpoint, Value: "ftp://example.com/a"}}, operatorOrigin()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid endpoint error = %v", err)
	}
	_, err := service.Upsert(context.Background(), created.ID, []Input{
		{Type: TypeService, Value: "tcp://192.0.2.1:443"},
		{Type: TypeEndpoint, Value: "https://example.com/", Parent: &Reference{Type: TypeService, Value: "tcp://192.0.2.1:443"}},
	}, operatorOrigin())
	if !errors.Is(err, ErrInvalidParent) {
		t.Fatalf("invalid parent error = %v", err)
	}
}

func TestRuntimeOriginMustMatchProjectTaskAndContinuation(t *testing.T) {
	service, projects, db := newAssetTestServices(t)
	created := createAssetProject(t, projects, project.Scope{})
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO tasks (id,project_id,goal,status,runner,runtime_profile_id,run_controls_json,scope_snapshot_json,created_at,updated_at)
		VALUES('task-1',?,?,?,?,?,?,?,?,?)`, created.ID, "goal", "running", "pi", "profile-1", `{}`, `{}`, stamp, stamp); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_continuations (id,task_id,number,runtime_profile_id,runtime_provider,runner,status,started_at,updated_at)
		VALUES('cont-1','task-1',1,'profile-1','provider','pi','running',?,?)`, stamp, stamp); err != nil {
		t.Fatalf("insert continuation: %v", err)
	}
	input := []Input{{Type: TypeIP, Value: "192.0.2.1"}}
	valid := Origin{Kind: OriginRuntime, TaskID: "task-1", ContinuationID: "cont-1"}
	if _, err := service.Upsert(context.Background(), created.ID, input, valid); err != nil {
		t.Fatalf("valid runtime origin: %v", err)
	}
	for _, mutated := range []Origin{
		{Kind: OriginRuntime, TaskID: "other-task", ContinuationID: "cont-1"},
		{Kind: OriginRuntime, TaskID: "task-1", ContinuationID: "other-cont"},
		{Kind: OriginOperator, TaskID: "task-1"},
	} {
		if _, err := service.Upsert(context.Background(), created.ID, input, mutated); !errors.Is(err, ErrTrustedOrigin) {
			t.Fatalf("origin %#v error = %v", mutated, err)
		}
	}
}

func TestObservedAtControlsFirstAndLastSeen(t *testing.T) {
	service, projects, _ := newAssetTestServices(t)
	created := createAssetProject(t, projects, project.Scope{})
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Format(time.RFC3339Nano)
	result, err := service.Upsert(context.Background(), created.ID, []Input{{Type: TypeIP, Value: "192.0.2.1", ObservedAt: old}}, operatorOrigin())
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got := result.Assets[0].FirstSeenAt.Format(time.RFC3339Nano); got != old {
		t.Fatalf("first seen = %q want %q", got, old)
	}
}
