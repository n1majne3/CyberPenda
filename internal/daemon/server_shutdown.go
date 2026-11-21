package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"pentest/internal/runtime"
)

func (server *Server) Close() error {
	if server.fgsCancel != nil {
		server.fgsCancel()
		server.fgsWG.Wait()
		server.fgs.CloseScans()
	}
	server.controlMu.Lock()
	server.closing = true
	server.providerControlCancel()
	server.runtimeLaunchCancel(runtime.ErrHarnessShutdown)
	server.controlMu.Unlock()
	server.providerControlWG.Wait()
	err := server.closeAllHostBrowsers()
	taskRunsStopped := server.harness == nil || server.harness.ShutdownAllAndWait(server.runtimeStopTimeout)
	if !taskRunsStopped {
		err = fmt.Errorf("Task Runtime shutdown timed out")
	}
	sessionRunsStopped := server.sessionHarness == nil || server.sessionHarness.ShutdownAllAndWait(server.runtimeStopTimeout)
	if !sessionRunsStopped && err == nil {
		err = fmt.Errorf("Session Runtime shutdown timed out")
	}
	if providerErr := server.providerSessions.closeAll(context.Background()); err == nil {
		err = providerErr
	}
	if sessionErr := server.sessionProviderSessions.closeAll(context.Background()); err == nil {
		err = sessionErr
	}
	if !taskRunsStopped && server.harness != nil {
		taskRunsStopped = server.harness.ShutdownAllAndWait(server.runtimeStopTimeout)
	}
	if !sessionRunsStopped && server.sessionHarness != nil {
		sessionRunsStopped = server.sessionHarness.ShutdownAllAndWait(server.runtimeStopTimeout)
	}
	launchesStopped := waitForRuntimeLaunches(&server.runtimeLaunchWG, server.runtimeStopTimeout)
	if !launchesStopped && err == nil {
		err = fmt.Errorf("Runtime launch shutdown timed out")
	}
	if !taskRunsStopped || !sessionRunsStopped || !launchesStopped {
		return err
	}
	if dbErr := server.db.Close(); err == nil {
		err = dbErr
	}
	if server.tempSkillsRoot != "" {
		if removeErr := os.RemoveAll(server.tempSkillsRoot); err == nil {
			err = removeErr
		}
	}
	return err
}

var errServerClosing = errors.New("server is closing")

func (server *Server) beginRuntimeLaunch() (context.Context, error) {
	server.controlMu.Lock()
	defer server.controlMu.Unlock()
	if server.closing {
		return nil, errServerClosing
	}
	server.runtimeLaunchWG.Add(1)
	return server.runtimeLaunchCtx, nil
}

func waitForRuntimeLaunches(group *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()
	if timeout <= 0 {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
