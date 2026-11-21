package projectinterface

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/store"
)

func TestBearerTokenRejectsQueryParameterAndAcceptsAuthorizationHeader(t *testing.T) {
	queryReq := httptest.NewRequest("GET", "http://127.0.0.1/api/v2/projects/p1/blackboard?token=query-secret", nil)
	if got := BearerToken(queryReq); got != "" {
		t.Fatalf("BearerToken accepted query token %q, want empty", got)
	}

	headerReq := httptest.NewRequest("GET", "http://127.0.0.1/api/v2/projects/p1/blackboard?token=query-secret", nil)
	headerReq.Header.Set("Authorization", "Bearer header-secret")
	if got := BearerToken(headerReq); got != "header-secret" {
		t.Fatalf("BearerToken = %q, want header-secret", got)
	}
}

func TestIssueSessionGrantRejectsArchivedSession(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(
		`INSERT INTO sessions (id, title, lifecycle, workdir, created_at, updated_at, last_activity_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"session-archived", "Archived Session", "archived", t.TempDir(), now, now, now,
	)
	if err != nil {
		t.Fatalf("insert archived session: %v", err)
	}
	_, err = db.Exec(
		`INSERT INTO session_runtime_config_versions (id, session_id, version, runtime_profile_id, config_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"cfg-1", "session-archived", 1, "profile-1", "{}", now,
	)
	if err != nil {
		t.Fatalf("insert session runtime config version: %v", err)
	}
	_, err = db.Exec(
		`INSERT INTO session_continuations (id, session_id, number, runtime_profile_id, runtime_provider, runner, runtime_config_version_id, status, started_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"cont-archived", "session-archived", 1, "profile-1", "codex", "sandbox", "cfg-1", "running", now, now,
	)
	if err != nil {
		t.Fatalf("insert session continuation: %v", err)
	}

	grants := NewGrantStore(db, nil, nil, nil)
	_, _, err = grants.IssueSession(context.Background(), IssueSessionGrantRequest{
		SessionID:              "session-archived",
		ContinuationID:         "cont-archived",
		RuntimeConfigVersionID: "cfg-1",
		RuntimeProfileID:       "profile-1",
		RuntimePluginID:        "codex",
		Runner:                 "sandbox",
	})
	if err == nil {
		t.Fatal("expected IssueSession to reject archived Session")
	}
	var piErr *Error
	if !errors.As(err, &piErr) || piErr.Code != ErrCodeContinuationClosed {
		t.Fatalf("expected %s error, got %v", ErrCodeContinuationClosed, err)
	}
}
