package projectinterface_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/projectinterface"
	"pentest/internal/session"
	"pentest/internal/store"
)

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type fixedIDs struct{ value string }

func (ids fixedIDs) NextID() string { return ids.value }

type fixedTokens struct{ value string }

func (tokens fixedTokens) NewToken() (string, error) { return tokens.value, nil }

func TestSessionContinuationGrantResolvesOnlyItsBoundSession(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sessions := session.NewService(db, filepath.Join(t.TempDir(), "sessions"))
	created, err := sessions.Create(session.CreateRequest{Input: "investigate"})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := sessions.CreateContinuation(created.ID, "profile-1", "claude_code", session.RunnerSandbox, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	grants := projectinterface.NewGrantStore(
		db,
		fixedClock{now: time.Date(2026, time.August, 2, 1, 2, 3, 0, time.UTC)},
		fixedIDs{value: "grant-session-1"},
		fixedTokens{value: "session-secret"},
	)
	if _, _, err := grants.IssueSession(context.Background(), projectinterface.IssueSessionGrantRequest{
		SessionID: created.ID, ContinuationID: continuation.ID,
		RuntimeConfigVersionID: continuation.RuntimeConfigID,
		RuntimeProfileID:       continuation.RuntimeProfileID,
		RuntimePluginID:        "pi", Runner: string(continuation.Runner),
	}); err == nil {
		t.Fatal("provider-spoofed Session grant was accepted")
	}
	token, issued, err := grants.IssueSession(context.Background(), projectinterface.IssueSessionGrantRequest{
		SessionID: created.ID, ContinuationID: continuation.ID,
		RuntimeConfigVersionID: continuation.RuntimeConfigID,
		RuntimeProfileID:       continuation.RuntimeProfileID,
		RuntimePluginID:        "claude_code", Runner: string(continuation.Runner),
	})
	if err != nil {
		t.Fatal(err)
	}
	if token != "session-secret" || !issued.IsSession() || issued.Owner.SessionID != created.ID {
		t.Fatalf("issued Session grant = %#v, token = %q", issued, token)
	}
	resolved, err := grants.Resolve(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.IsSession() || resolved.Owner.SessionID != created.ID || resolved.ContinuationID != continuation.ID {
		t.Fatalf("resolved Session grant = %#v", resolved)
	}
	sessions.SetContinuationTerminalMarker(grants)
	if _, err := sessions.UpdateContinuationStatus(continuation.ID, session.RuntimeStatusCompleted); err != nil {
		t.Fatal(err)
	}
	terminal, err := grants.Resolve(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status() != projectinterface.GrantStatusTerminal {
		t.Fatalf("terminal Session grant status = %q", terminal.Status())
	}
}
