package hostedcontroller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"pentest/internal/tsecbenchclient"
)

// CompletionPlatform exposes only the read operation used before recovery.
type CompletionPlatform interface {
	List(context.Context) (tsecbenchclient.ListResult, error)
}

// completionSettled observes completion; it never schedules or closes a target.
// Failed or incomplete reads keep recovery enabled. The dispatcher receipt is
// its public file protocol, not a replacement for the fresh platform snapshot.
func (app *HTTPApp) completionSettled(ctx context.Context, run HostedEvaluationReference) bool {
	if app.completionPlatform == nil || app.runtimeRoot == "" || filepath.Base(run.TaskID) != run.TaskID || run.TaskID == "." || run.TaskID == ".." {
		return false
	}
	file, err := os.Open(filepath.Join(app.runtimeRoot, run.TaskID, "workdir", "graph", "ledger.json"))
	if err != nil {
		return false
	}
	defer file.Close()
	var ledger struct {
		Challenges map[string]json.RawMessage `json:"challenges"`
		Dispatches map[string]struct {
			Code           string `json:"code"`
			State          string `json:"state"`
			AgentID        string `json:"agent_id"`
			TerminalAt     int64  `json:"terminal_at"`
			TerminalStatus string `json:"terminal_status"`
			RuntimeSettled bool   `json:"runtime_settled"`
		} `json:"dispatches"`
	}
	if err := json.NewDecoder(io.LimitReader(file, 4<<20)).Decode(&ledger); err != nil || len(ledger.Challenges) == 0 || len(ledger.Dispatches) == 0 {
		return false
	}
	for _, d := range ledger.Dispatches {
		if _, known := ledger.Challenges[d.Code]; !known {
			return false
		}
		if d.State == "cancelled" && d.AgentID == "" {
			continue
		}
		if (d.State != "done" && d.State != "invalid") || d.TerminalAt <= 0 {
			return false
		}
		if d.AgentID != "" {
			if !d.RuntimeSettled {
				return false
			}
		} else if d.TerminalStatus != "not_started" {
			return false
		}
	}
	var taskState struct {
		RuntimeActivity struct {
			Liveness     string `json:"liveness"`
			TurnActivity string `json:"turn_activity"`
		} `json:"runtime_activity"`
	}
	if err := app.request(ctx, http.MethodGet, "/api/projects/"+run.ProjectID+"/tasks/"+run.TaskID, nil, &taskState); err != nil ||
		taskState.RuntimeActivity.Liveness != "live" || taskState.RuntimeActivity.TurnActivity != "idle" {
		return false
	}
	state, err := app.completionPlatform.List(ctx)
	if err != nil || len(state.Challenges) != len(ledger.Challenges) {
		return false
	}
	seen := make(map[string]bool, len(state.Challenges))
	for _, c := range state.Challenges {
		if _, known := ledger.Challenges[c.UniqueCode]; !known || seen[c.UniqueCode] {
			return false
		}
		seen[c.UniqueCode] = true
		if !c.IsCompleted && (c.FlagCount <= 0 || c.CorrectFlagCount < c.FlagCount) {
			return false
		}
		if c.ContainerStatus != "stopped" || len(c.ContainerAddr) != 0 {
			return false
		}
	}
	return true
}
