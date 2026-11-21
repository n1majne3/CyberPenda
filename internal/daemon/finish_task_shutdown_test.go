package daemon

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
	"pentest/internal/testsupport/fakeprovider"
)

// delayedFinishCloseSession blocks inside Close so Finish cannot complete
// reconciliation before provider resources shut down.
type delayedFinishCloseSession struct {
	*fakeprovider.Session
	closed       chan struct{}
	closeEntered chan struct{}
	allowClose   chan struct{}
	once         sync.Once
	allowOnce    sync.Once
}

func (s *delayedFinishCloseSession) releaseClose() {
	s.allowOnce.Do(func() { close(s.allowClose) })
}

func (s *delayedFinishCloseSession) Close(ctx context.Context) error {
	select {
	case <-s.closeEntered:
	default:
		close(s.closeEntered)
	}
	select {
	case <-s.allowClose:
	case <-ctx.Done():
		return ctx.Err()
	}
	err := s.Session.Close(ctx)
	s.once.Do(func() { close(s.closed) })
	return err
}

func TestFinishTaskShutdownHappensBeforeReconciliation(t *testing.T) {
	inner := fakeprovider.New(fakeprovider.Config{
		SessionID: "finish-order",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	session := &delayedFinishCloseSession{
		Session:      inner,
		closed:       make(chan struct{}),
		closeEntered: make(chan struct{}),
		allowClose:   make(chan struct{}),
	}
	adapter := runtime.NewProviderSessionRunAdapter(session, session.closed)
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixture(t, factory)
	t.Cleanup(session.releaseClose)

	counter := &countingFinishReconciler{inner: nil}
	// Preserve production recon when present; still count calls for order.
	if server.blackboardV2 != nil {
		counter.inner = server.blackboardV2
	}
	server.tasks.SetContinuationReconciler(counter)
	launchFinishTask(t, server, created)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- postFinish(server, created.ProjectID, created.ID)
	}()

	select {
	case <-session.closeEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("Finish did not enter provider session Close")
	}
	// While Close is blocked, reconciliation and Task completed must not run.
	if counter.callCount() != 0 {
		t.Fatalf("reconciliation ran before session Close completed: calls=%d", counter.callCount())
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status == task.StatusCompleted {
		t.Fatal("Task completed before provider resources closed")
	}

	session.releaseClose()
	var resp *httptest.ResponseRecorder
	select {
	case resp = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Finish did not return after Close released")
	}
	if resp.Code != http.StatusOK {
		t.Fatalf("finish status = %d body %s", resp.Code, resp.Body.String())
	}
	if counter.callCount() < 1 {
		t.Fatal("expected reconciliation after resource close")
	}
	found, err = server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != task.StatusCompleted {
		t.Fatalf("status after ordered finish = %q", found.Status)
	}
}

func TestConcurrentResumeSecondConflictsWithoutStoppingFirst(t *testing.T) {
	newSession := func(open int) (runtime.ProviderSession, runtime.Adapter) {
		return newFinishSessionPairWithCaps(fmt.Sprintf("resume-race-%d", open), runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true, ResumeSession: true,
		})
	}
	seedSession, seedAdapter := newSession(0)
	factory := &finishSessionFactory{session: seedSession, adapter: seedAdapter, newSession: newSession}
	server, created, _ := newFinishTaskFixture(t, factory)
	launchFinishTask(t, server, created)

	// Finish so Task is terminal and resumable.
	finishResp := postFinish(server, created.ProjectID, created.ID)
	if finishResp.Code != http.StatusOK {
		t.Fatalf("finish status = %d body %s", finishResp.Code, finishResp.Body.String())
	}
	beforeLatest, err := server.tasks.LatestContinuation(created.ID)
	if err != nil || beforeLatest == nil {
		t.Fatalf("latest continuation before resume: %v %#v", err, beforeLatest)
	}
	beforeNumber := beforeLatest.Number
	opensBefore := factory.openCount()

	const n = 8
	type result struct {
		code int
		body string
	}
	results := make(chan result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/resume", bytes.NewReader([]byte(`{}`)))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			server.ServeHTTP(resp, req)
			results <- result{code: resp.Code, body: resp.Body.String()}
		}()
	}
	wg.Wait()
	close(results)

	var accepted, conflicted int
	for r := range results {
		switch r.code {
		case http.StatusAccepted:
			accepted++
		case http.StatusConflict:
			conflicted++
		default:
			t.Fatalf("unexpected resume status %d body %s", r.code, r.body)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted resumes = %d, want 1 (rest conflict)", accepted)
	}
	if conflicted != n-1 {
		t.Fatalf("conflicted resumes = %d, want %d", conflicted, n-1)
	}
	// Exactly one replacement Runtime — second must not stop/replace the first.
	if factory.openCount() != opensBefore+1 {
		t.Fatalf("opens before=%d after=%d, want exactly one new Runtime", opensBefore, factory.openCount())
	}
	waitForTaskRunning(t, server, created.ID)
	if _, ok := server.providerSessions.get(created.ID); !ok {
		t.Fatal("first resume Runtime ownership lost after concurrent resumes")
	}
	latest, err := server.tasks.LatestContinuation(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest continuation: %v %#v", err, latest)
	}
	if latest.Number > beforeNumber+1 {
		t.Fatalf("continuation number jumped from %d to %d after concurrent resumes", beforeNumber, latest.Number)
	}
}
