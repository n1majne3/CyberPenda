package hostedcontroller

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const hostedChallengeSkillID = "tsecbench-hosted-challenge-loop"

//go:embed assets/tsecbench-hosted-challenge-loop/SKILL.md
var hostedChallengeSkillInstruction string

// HTTPApp uses only the normal daemon HTTP surface for hosted bootstrap and
// observation. It does not add TSecBench routes to the daemon.
type HTTPApp struct {
	baseURL    string
	client     *http.Client
	piBinary   string
	pollPeriod time.Duration
}

// HTTPAppConfig describes the loopback daemon used by the hosted process.
type HTTPAppConfig struct {
	BaseURL       string
	Client        *http.Client
	RuntimeBinary string
	PollPeriod    time.Duration
}

func NewHTTPApp(config HTTPAppConfig) *HTTPApp {
	client := config.Client
	if client == nil {
		client = http.DefaultClient
	}
	period := config.PollPeriod
	if period <= 0 {
		period = 250 * time.Millisecond
	}
	return &HTTPApp{baseURL: strings.TrimRight(config.BaseURL, "/"), client: client, piBinary: config.RuntimeBinary, pollPeriod: period}
}

func (app *HTTPApp) Start(ctx context.Context, evaluation Evaluation) (RunRef, error) {
	if err := app.request(ctx, http.MethodPut, "/api/skills/"+hostedChallengeSkillID, map[string]any{
		"name":        hostedChallengeSkillID,
		"description": "Completes a TSecBench Hosted Evaluation Run through the injected challenge API. Use only when the Task Goal requires TSecBench hosted evaluation.",
		"source_provenance": map[string]string{
			"kind": "hosted",
		},
		"files": map[string]string{"SKILL.md": hostedChallengeSkillInstruction},
	}, nil); err != nil {
		return RunRef{}, fmt.Errorf("publish hosted TSecBench Skill: %w", err)
	}

	var provider struct {
		ID        string `json:"id"`
		APIKeyEnv string `json:"api_key_env"`
	}
	if err := app.request(ctx, http.MethodPost, "/api/model-providers", map[string]any{
		"name":      "TSecBench Hosted Model",
		"endpoints": []map[string]string{{"protocol": evaluation.Runtime.ModelProtocol, "base_url": evaluation.Runtime.ModelBaseURL}},
		"catalog":   map[string]any{"manual": []string{evaluation.Runtime.Model}, "default_model": evaluation.Runtime.Model},
	}, &provider); err != nil {
		return RunRef{}, fmt.Errorf("create hosted Model Provider: %w", err)
	}

	var project struct {
		ID string `json:"id"`
	}
	if err := app.request(ctx, http.MethodPost, "/api/projects", map[string]any{
		"name": evaluation.Project.Name, "kind": evaluation.Project.Kind,
		"scope": map[string]any{"notes": evaluation.Project.ScopeNotes},
	}, &project); err != nil {
		return RunRef{}, fmt.Errorf("create hosted Project: %w", err)
	}

	fields := map[string]any{
		"model_provider_id": provider.ID, "model_provider_protocol": evaluation.Runtime.ModelProtocol,
		"model_override": evaluation.Runtime.Model, "env": evaluation.Runtime.Env,
		"credential_refs": []string{"BENCHMARK_TOKEN"},
	}
	if evaluation.Runtime.Provider == RuntimePi {
		// Pi's --approve trusts only this run's projected project-local
		// resources. It does not change tool permissions or Project Scope.
		fields["custom_args"] = []string{"--approve"}
	}
	if strings.TrimSpace(app.piBinary) != "" {
		fields["binary_path"] = app.piBinary
	}
	var profile struct {
		ID string `json:"id"`
	}
	if err := app.request(ctx, http.MethodPost, "/api/runtime-profiles", map[string]any{
		"name": "TSecBench Hosted Runtime", "provider": evaluation.Runtime.Provider, "fields": fields,
	}, &profile); err != nil {
		return RunRef{}, fmt.Errorf("create hosted Runtime Profile: %w", err)
	}

	bindings := map[string]string{"BENCHMARK_TOKEN": evaluation.Runtime.Credentials["BENCHMARK_TOKEN"], provider.APIKeyEnv: evaluation.Runtime.ModelAPIKey}
	for credentialRef, value := range bindings {
		if err := app.request(ctx, http.MethodPut, "/api/projects/"+project.ID+"/credential-bindings", map[string]any{
			"credential_ref": credentialRef,
			"source":         map[string]string{"kind": "literal", "value": value, "destination_env": credentialRef},
		}, nil); err != nil {
			return RunRef{}, fmt.Errorf("bind hosted credential: %w", err)
		}
	}

	var task struct {
		ID string `json:"id"`
	}
	if err := app.request(ctx, http.MethodPost, "/api/projects/"+project.ID+"/tasks", map[string]any{
		"type": evaluation.Task.Type, "goal": evaluation.Task.Goal,
		"runtime_profile_id": profile.ID, "runner": evaluation.Task.Runner,
		"run_controls": map[string]any{"host_activated": evaluation.Task.HostActivated, "blackboard_conclusion_mode": "interactive"},
	}, &task); err != nil {
		return RunRef{}, fmt.Errorf("create hosted Task: %w", err)
	}
	return RunRef{ProjectID: project.ID, TaskID: task.ID}, nil
}

func (app *HTTPApp) Wait(ctx context.Context, run RunRef, stdout io.Writer, secrets []string) error {
	if stdout == nil {
		return errors.New("hosted Transcript stdout is unavailable")
	}
	masker := newExactMasker(secrets)
	cursor, err := app.streamInitialTranscript(ctx, run, stdout, masker)
	if err != nil {
		if contextEnded(err) {
			return nil
		}
		return err
	}
	ticker := time.NewTicker(app.pollPeriod)
	defer ticker.Stop()
	for {
		cursor, err = app.drainTranscript(ctx, run, stdout, masker, cursor)
		if err != nil {
			if contextEnded(err) {
				return nil
			}
			return err
		}
		var taskState struct {
			Status string `json:"status"`
		}
		if err := app.request(ctx, http.MethodGet, "/api/projects/"+run.ProjectID+"/tasks/"+run.TaskID, nil, &taskState); err != nil {
			if contextEnded(err) {
				return nil
			}
			return fmt.Errorf("observe hosted Task: %w", err)
		}
		if taskState.Status == "failed" || taskState.Status == "interrupted" || taskState.Status == "stopped" {
			if _, err := app.drainTranscript(ctx, run, stdout, masker, cursor); err != nil {
				if contextEnded(err) {
					return nil
				}
				return fmt.Errorf("final hosted Transcript drain: %w", err)
			}
			return errors.New("hosted Runtime failed")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func contextEnded(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (app *HTTPApp) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, app.baseURL+path, body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := app.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("daemon returned HTTP %d", response.StatusCode)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("decode daemon response: %w", err)
	}
	return nil
}
