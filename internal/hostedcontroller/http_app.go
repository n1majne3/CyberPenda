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

const hostedChallengeSkillID = "ctf-orchestrator"

//go:embed assets/ctf-orchestrator/SKILL.md
var hostedChallengeSkillInstruction string

//go:embed assets/ctf-orchestrator/references/graph-protocol.md
var hostedChallengeGraphProtocol string

//go:embed assets/ctf-orchestrator/references/execute-prompt.md
var hostedChallengeExecutePrompt string

//go:embed assets/ctf-orchestrator/scripts/dispatch.py
var hostedChallengeDispatchScript string

// HTTPApp uses only the normal daemon HTTP surface for hosted bootstrap and
// observation. It does not add TSecBench routes to the daemon.
type HTTPApp struct {
	baseURL       string
	client        *http.Client
	runtimeBinary string
	pollPeriod    time.Duration
	diagnostics   io.Writer

	silenceReviveSec         int
	silenceReviveMax         int
	silenceReviveCooldownSec int
}

// HTTPAppConfig describes the loopback daemon used by the hosted process.
type HTTPAppConfig struct {
	BaseURL       string
	Client        *http.Client
	RuntimeBinary string
	PollPeriod    time.Duration
	Diagnostics   io.Writer
	// SilenceReviveSec enables the hosted silence watchdog when > 0: a
	// running Task whose Transcript cursor stops advancing for this many
	// seconds gets one revive steering per cooldown window to wake a parked
	// orchestrator turn (runs 24147/24160 died this way: the turn ended —
	// degenerate output or a provider outage beyond the retry budget — and
	// nothing ever started a new turn).
	SilenceReviveSec         int
	SilenceReviveMax         int
	SilenceReviveCooldownSec int
}

// reviveSteerMessage is delivered as task steering when the watchdog fires.
// It must restart the Decide loop without corrupting dispatcher state.
const reviveSteerMessage = "(CyberPenda hosted watchdog) 长时间无运行事件:上一轮可能已异常终止。" +
	"请按 ctf-orchestrator 的身份确认流程恢复 Decide 循环:读 leader.lock 与 dispatcher tmux 存活," +
	"检查 READY/升级队列后继续调度。不要重启容器,不要重置 ledger;若一切正常则回到阻塞等待。"

func NewHTTPApp(config HTTPAppConfig) *HTTPApp {
	client := config.Client
	if client == nil {
		client = http.DefaultClient
	}
	period := config.PollPeriod
	if period <= 0 {
		period = 250 * time.Millisecond
	}
	diagnostics := config.Diagnostics
	if diagnostics == nil {
		diagnostics = io.Discard
	}
	reviveSec := config.SilenceReviveSec
	reviveMax := config.SilenceReviveMax
	reviveCooldown := config.SilenceReviveCooldownSec
	if reviveCooldown <= 0 {
		reviveCooldown = reviveSec
	}
	return &HTTPApp{baseURL: strings.TrimRight(config.BaseURL, "/"), client: client, runtimeBinary: config.RuntimeBinary, pollPeriod: period, diagnostics: diagnostics,
		silenceReviveSec: reviveSec, silenceReviveMax: reviveMax, silenceReviveCooldownSec: reviveCooldown}
}

func (app *HTTPApp) Start(ctx context.Context, evaluation HostedEvaluationBootstrap) (HostedEvaluationReference, error) {
	// Direct bootstrap callers bypass ConfigFromEnv, so the pure model-plan
	// checks run again here, before the first HTTP write.
	groups, err := PlanHostedModelGroups(evaluation.Runtime)
	if err != nil {
		return HostedEvaluationReference{}, err
	}
	if err := app.request(ctx, http.MethodPut, "/api/skills/"+hostedChallengeSkillID, map[string]any{
		"name":        hostedChallengeSkillID,
		"description": "Orchestrates a TSecBench Hosted Evaluation Run with the tested Decide/Execute and FGS protocol.",
		"source_provenance": map[string]string{
			"kind": "hosted",
		},
		"files": map[string]string{
			"SKILL.md":                     hostedChallengeSkillInstruction,
			"references/graph-protocol.md": hostedChallengeGraphProtocol,
			"references/execute-prompt.md": hostedChallengeExecutePrompt,
			"scripts/dispatch.py":          hostedChallengeDispatchScript,
		},
	}, nil); err != nil {
		return HostedEvaluationReference{}, fmt.Errorf("publish hosted ctf-orchestrator Skill: %w", err)
	}

	// One Model Provider per plan group. The parent group is always first and
	// the Runtime Profile keeps pointing at it, so the parent session model
	// does not change. Additional groups only widen the Pi model registry.
	type createdProvider struct {
		ID        string
		APIKeyEnv string
		APIKey    string
	}
	providers := make([]createdProvider, 0, len(groups))
	for _, group := range groups {
		catalog := map[string]any{"manual": append([]string(nil), group.Models...), "default_model": group.Models[0]}
		if evaluation.Runtime.ContextWindow > 0 || evaluation.Runtime.MaxOutputTokens > 0 {
			limits := map[string]any{}
			if evaluation.Runtime.ContextWindow > 0 {
				limits["context_window"] = evaluation.Runtime.ContextWindow
			}
			if evaluation.Runtime.MaxOutputTokens > 0 {
				limits["max_output_tokens"] = evaluation.Runtime.MaxOutputTokens
			}
			perModel := make(map[string]any, len(group.Models))
			for _, model := range group.Models {
				perModel[model] = limits
			}
			catalog["limits"] = perModel
		}
		var provider struct {
			ID        string `json:"id"`
			APIKeyEnv string `json:"api_key_env"`
		}
		if err := app.request(ctx, http.MethodPost, "/api/model-providers", map[string]any{
			"name":      group.Name,
			"endpoints": []map[string]string{{"protocol": group.Protocol, "base_url": group.BaseURL}},
			"catalog":   catalog,
		}, &provider); err != nil {
			return HostedEvaluationReference{}, fmt.Errorf("create hosted Model Provider: %w", err)
		}
		providers = append(providers, createdProvider{ID: provider.ID, APIKeyEnv: provider.APIKeyEnv, APIKey: group.APIKey})
	}

	var project struct {
		ID string `json:"id"`
	}
	if err := app.request(ctx, http.MethodPost, "/api/projects", map[string]any{
		"name": evaluation.Project.Name, "kind": evaluation.Project.Kind,
		"scope": map[string]any{"notes": evaluation.Project.ScopeNotes},
	}, &project); err != nil {
		return HostedEvaluationReference{}, fmt.Errorf("create hosted Project: %w", err)
	}

	fields := map[string]any{
		// The parent group is the first plan group by contract.
		"model_provider_id": providers[0].ID, "model_provider_protocol": evaluation.Runtime.ModelProtocol,
		"model_override": evaluation.Runtime.Model, "env": evaluation.Runtime.Env,
		"credential_refs": []string{"BENCHMARK_TOKEN"},
	}
	if effort := strings.TrimSpace(evaluation.Runtime.ReasoningEffort); effort != "" {
		fields["reasoning_effort"] = effort
	}
	if evaluation.Runtime.Provider == RuntimePi {
		// Pi's --approve trusts only this run's projected project-local
		// resources. It does not change tool permissions or Project Scope.
		fields["custom_args"] = []string{"--approve"}
	}
	if strings.TrimSpace(app.runtimeBinary) != "" {
		fields["binary_path"] = app.runtimeBinary
	}
	if evaluation.Runtime.Provider == "codex" {
		fields["codex_multi_agent"] = map[string]any{"enabled": true}
	}
	var profile struct {
		ID string `json:"id"`
	}
	if err := app.request(ctx, http.MethodPost, "/api/runtime-profiles", map[string]any{
		"name": "TSecBench Hosted Runtime", "provider": evaluation.Runtime.Provider, "fields": fields,
	}, &profile); err != nil {
		return HostedEvaluationReference{}, fmt.Errorf("create hosted Runtime Profile: %w", err)
	}

	bindings := map[string]string{"BENCHMARK_TOKEN": evaluation.Runtime.Credentials["BENCHMARK_TOKEN"]}
	for _, provider := range providers {
		bindings[provider.APIKeyEnv] = provider.APIKey
	}
	for credentialRef, value := range bindings {
		if err := app.request(ctx, http.MethodPut, "/api/projects/"+project.ID+"/credential-bindings", map[string]any{
			"credential_ref": credentialRef,
			"source":         map[string]string{"kind": "literal", "value": value, "destination_env": credentialRef},
		}, nil); err != nil {
			return HostedEvaluationReference{}, fmt.Errorf("bind hosted credential: %w", err)
		}
	}

	var task struct {
		ID string `json:"id"`
	}
	if err := app.request(ctx, http.MethodPost, "/api/projects/"+project.ID+"/tasks", map[string]any{
		"type": evaluation.Task.Type, "goal": evaluation.Task.Goal,
		"runtime_profile_id": profile.ID, "runner": evaluation.Task.Runner,
		"run_controls": map[string]any{"host_activated": evaluation.Task.HostActivated, "blackboard_mode": "disabled"},
	}, &task); err != nil {
		return HostedEvaluationReference{}, fmt.Errorf("create hosted Task: %w", err)
	}
	return HostedEvaluationReference{ProjectID: project.ID, TaskID: task.ID}, nil
}

func (app *HTTPApp) Wait(ctx context.Context, run HostedEvaluationReference, stdout io.Writer, secrets []string) error {
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
	sawRunning := false
	// Silence watchdog state: lastProgress is the moment the Transcript
	// cursor last advanced; a parked orchestrator turn leaves it frozen.
	lastProgress := time.Now()
	var revives int
	var lastRevive time.Time
	for {
		var taskState struct {
			Status string `json:"status"`
		}
		if err := app.request(ctx, http.MethodGet, "/api/projects/"+run.ProjectID+"/tasks/"+run.TaskID, nil, &taskState); err != nil {
			if contextEnded(err) {
				return nil
			}
			if !sawRunning {
				return fmt.Errorf("observe hosted Task: %w", err)
			}
			app.logOperational("observe hosted Task: %v", err)
		} else if taskState.Status == "running" {
			sawRunning = true
		}
		next, drainErr := app.drainTranscript(ctx, run, stdout, masker, cursor)
		if drainErr != nil {
			if contextEnded(drainErr) {
				return nil
			}
			if !sawRunning {
				return drainErr
			}
			app.logOperational("hosted Transcript drain: %v", drainErr)
		} else {
			if next != cursor {
				lastProgress = time.Now()
			}
			cursor = next
		}
		if sawRunning && app.silenceReviveSec > 0 && taskState.Status == "running" &&
			time.Since(lastProgress) > time.Duration(app.silenceReviveSec)*time.Second &&
			(revives == 0 || time.Since(lastRevive) > time.Duration(app.silenceReviveCooldownSec)*time.Second) &&
			(app.silenceReviveMax <= 0 || revives < app.silenceReviveMax) {
			payload := map[string]any{
				"request_id": fmt.Sprintf("revive-%s-%d", run.TaskID, time.Now().Unix()),
				"message":    reviveSteerMessage,
			}
			if err := app.request(ctx, http.MethodPost, "/api/projects/"+run.ProjectID+"/tasks/"+run.TaskID+"/steer", payload, nil); err != nil {
				app.logOperational("silence watchdog steer failed: %v", err)
			} else {
				app.logOperational("silence watchdog: no transcript progress for %s; revive steering sent (%d/%d)",
					time.Since(lastProgress).Round(time.Second), revives+1, app.silenceReviveMax)
			}
			revives++
			lastRevive = time.Now()
		}
		if taskState.Status == "failed" || taskState.Status == "interrupted" || taskState.Status == "stopped" {
			if !sawRunning {
				if next, drainErr := app.drainTranscript(ctx, run, stdout, masker, cursor); drainErr != nil {
					if contextEnded(drainErr) {
						return nil
					}
					return fmt.Errorf("final hosted Transcript drain: %w", drainErr)
				} else {
					cursor = next
				}
				return errors.New("hosted Runtime failed")
			}
			app.logOperational("hosted Runtime is %s; wait for platform termination", taskState.Status)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (app *HTTPApp) logOperational(format string, args ...any) {
	if app == nil || app.diagnostics == nil {
		return
	}
	_, _ = fmt.Fprintf(app.diagnostics, format+"\n", args...)
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
