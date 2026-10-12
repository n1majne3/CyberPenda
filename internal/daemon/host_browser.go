package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"pentest/internal/preflight"
	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

type hostBrowserProcess struct {
	cancel context.CancelFunc
	ready  chan struct{}
	done   chan struct{}
	port   string
	err    error
}

func (server *Server) hostBrowserCommand() (string, string, string, error) {
	binary, err := exec.LookPath(firstNonBlank(server.hostBrowserMCPBinary, "playwright-mcp"))
	if err != nil {
		return "", "", "", fmt.Errorf("Host Browser requires an installed playwright-mcp binary: %w", err)
	}
	browser := firstNonBlank(server.hostBrowserBrowser, "msedge")
	if browser != "msedge" && browser != "chrome" {
		return "", "", "", errors.New("Host Browser supports msedge or chrome extension mode")
	}
	executable := server.hostBrowserExecutable
	if executable == "" {
		switch goruntime.GOOS {
		case "darwin":
			if browser == "msedge" {
				executable = "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"
			} else {
				executable = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
			}
		case "windows":
			for _, root := range []string{os.Getenv("PROGRAMFILES(X86)"), os.Getenv("PROGRAMFILES"), os.Getenv("LOCALAPPDATA")} {
				if root == "" {
					continue
				}
				candidate := filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe")
				if browser == "chrome" {
					candidate = filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe")
				}
				if _, err := exec.LookPath(candidate); err == nil {
					executable = candidate
					break
				}
			}
		default:
			if browser == "msedge" {
				executable = "microsoft-edge"
			} else {
				executable = "google-chrome"
			}
		}
	}
	executable, err = exec.LookPath(executable)
	if err != nil {
		return "", "", "", fmt.Errorf("Host Browser executable is unavailable; configure host-browser-executable: %w", err)
	}
	return binary, browser, executable, nil
}

func (server *Server) checkHostBrowser(result *preflight.Result, enabled bool, provider runtimeprofile.Provider) {
	if !enabled || provider == runtimeprofile.ProviderFake {
		return
	}
	check := preflight.Check{Name: "host_browser", Status: preflight.CheckPass, Detail: "Host Browser uses the real browser session and host MCP code execution. Extension connection is approved in the browser."}
	if _, _, _, err := server.hostBrowserCommand(); err != nil {
		check.Status = preflight.CheckFail
		check.Detail = err.Error()
		result.Pass = false
	}
	result.Checks = append(result.Checks, check)
}

func (server *Server) ensureTaskHostBrowser(created task.Task) error {
	versions, err := server.tasks.RuntimeConfigVersions(created.ID)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		return errors.New("runtime configuration snapshot is missing")
	}
	snapshot, err := decodeRuntimeSnapshot(versions[len(versions)-1].Config)
	if err != nil {
		return err
	}
	return server.ensureHostBrowser(created.ID, snapshot, filepath.Join(server.runtimeRoot, created.ID, "host-browser"))
}

func (server *Server) ensureHostBrowser(ownerID string, snapshot runtimeconfig.RuntimeConfigurationSnapshot, directory string) error {
	if !snapshot.HostBrowserEnabled || snapshot.RuntimePluginID == string(runtimeprofile.ProviderFake) {
		return nil
	}
	// A new launch owns a new service, even if the prior Harness has released
	// ownership before its deferred resource cleanup finishes.
	if err := server.closeHostBrowser(context.Background(), ownerID); err != nil {
		return err
	}
	server.hostBrowserMu.Lock()
	if err := server.runtimeLaunchCtx.Err(); err != nil {
		server.hostBrowserMu.Unlock()
		return err
	}
	ctx, cancel := context.WithCancel(server.runtimeLaunchCtx)
	process := &hostBrowserProcess{cancel: cancel, ready: make(chan struct{}), done: make(chan struct{})}
	server.hostBrowserProcesses[ownerID] = process
	go func() {
		process.err = server.startHostBrowser(ctx, directory, process)
		close(process.ready)
	}()
	server.hostBrowserMu.Unlock()
	select {
	case <-server.runtimeLaunchCtx.Done():
		return server.runtimeLaunchCtx.Err()
	case <-process.ready:
	}
	if process.err != nil {
		_ = server.closeHostBrowser(context.Background(), ownerID)
		return process.err
	}
	select {
	case <-process.done:
		return errors.New("Host Browser MCP service exited")
	default:
		return nil
	}
}

func (server *Server) startHostBrowser(ctx context.Context, directory string, process *hostBrowserProcess) error {
	started := false
	defer func() {
		if !started {
			close(process.done)
		}
	}()
	binary, browser, executable, err := server.hostBrowserCommand()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	_ = listener.Close()
	process.port = port
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	output, err := os.OpenFile(filepath.Join(directory, "service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	args := []string{"--extension", "--browser", browser, "--executable-path", executable,
		"--host", "127.0.0.1", "--port", port,
		"--allowed-hosts", "localhost:" + port + ",127.0.0.1:" + port + ",host.docker.internal:" + port,
		"--output-dir", directory, "--codegen", "none"}
	command := exec.CommandContext(ctx, binary, args...)
	command.Stdout, command.Stderr = output, output
	// Browser connection keeps extension approval. Do not inherit an automatic token.
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "PLAYWRIGHT_MCP_EXTENSION_TOKEN=") {
			command.Env = append(command.Env, value)
		}
	}
	if err := command.Start(); err != nil {
		_ = output.Close()
		return err
	}
	started = true
	go func() { _ = command.Wait(); _ = output.Close(); close(process.done) }()
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 250 * time.Millisecond, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, _ := http.NewRequestWithContext(startup, http.MethodGet, "http://127.0.0.1:"+port+"/mcp", nil)
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode < 300 || response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusMethodNotAllowed || response.StatusCode == http.StatusNotAcceptable {
				return nil
			}
		}
		select {
		case <-startup.Done():
			process.cancel()
			return fmt.Errorf("Host Browser MCP startup: %w", startup.Err())
		case <-process.done:
			return errors.New("Host Browser MCP exited before readiness; see task host-browser/service.log")
		case <-ticker.C:
		}
	}
}

func (server *Server) hostBrowserURL(taskID string, sandbox bool) string {
	server.hostBrowserMu.Lock()
	process := server.hostBrowserProcesses[taskID]
	server.hostBrowserMu.Unlock()
	if process == nil {
		return ""
	}
	<-process.ready
	if process.err != nil {
		return ""
	}
	select {
	case <-process.done:
		return ""
	default:
	}
	host := "127.0.0.1"
	if sandbox {
		host = "host.docker.internal"
	}
	return "http://" + net.JoinHostPort(host, process.port) + "/mcp"
}

func (server *Server) closeHostBrowser(ctx context.Context, taskID string) error {
	server.hostBrowserMu.Lock()
	process := server.hostBrowserProcesses[taskID]
	server.hostBrowserMu.Unlock()
	return server.closeHostBrowserProcess(ctx, taskID, process)
}

func (server *Server) closeHostBrowserProcess(ctx context.Context, taskID string, process *hostBrowserProcess) error {
	if process == nil {
		return nil
	}
	server.hostBrowserMu.Lock()
	if server.hostBrowserProcesses[taskID] == process {
		delete(server.hostBrowserProcesses, taskID)
	}
	server.hostBrowserMu.Unlock()
	process.cancel()
	ctx, cancel := context.WithTimeout(ctx, server.runtimeStopTimeout)
	defer cancel()
	select {
	case <-process.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (server *Server) closeAllHostBrowsers() error {
	server.hostBrowserMu.Lock()
	ids := make([]string, 0, len(server.hostBrowserProcesses))
	for id, process := range server.hostBrowserProcesses {
		process.cancel()
		ids = append(ids, id)
	}
	server.hostBrowserMu.Unlock()
	var result error
	for _, id := range ids {
		result = errors.Join(result, server.closeHostBrowser(context.Background(), id))
	}
	return result
}
