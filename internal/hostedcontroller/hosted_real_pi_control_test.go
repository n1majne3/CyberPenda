package hostedcontroller_test

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestHostedRealPiStopsExecuteBeforeFinish(t *testing.T) {
	fixture := newRealPiHostedAcceptanceFixture(t)
	fixture.model.control = true
	fixture.start(t)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		fixture.model.mu.Lock()
		ok := fixture.model.controlOK
		fixture.model.mu.Unlock()
		if ok && fixture.runtimeTurnSettled(t) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("real Pi control did not settle: %s", fixture.model.lastRequestText())
}

func (model *realPiControlledModel) serveControl(response io.Writer, body []byte) {
	var request struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &request)
	child, toolSeen := false, false
	for _, message := range request.Messages {
		text, _ := json.Marshal(message.Content)
		if message.Role == "user" && strings.Contains(string(text), "CONTROL_ACCEPTANCE_CHILD") {
			child = true
		}
		toolSeen = toolSeen || message.Role == "tool"
	}
	if child {
		if !toolSeen {
			writeRealPiToolCall(response, "control-child", "bash", map[string]any{
				"command": "touch control-child-running; sleep 60; touch control-child-leaked",
			})
		} else {
			writeRealPiText(response, "Child complete")
		}
		return
	}
	model.mu.Lock()
	model.parentCalls++
	call := model.parentCalls
	model.mu.Unlock()
	switch call {
	case 1:
		writeRealPiToolCall(response, "control-spawn", "Agent", map[string]any{
			"subagent_type": "execute", "run_in_background": true,
			"description": "Runtime control acceptance", "prompt": "CONTROL_ACCEPTANCE_CHILD",
		})
	case 2:
		id := regexp.MustCompile(`Agent ID: ([a-zA-Z0-9_-]+)`).FindSubmatch(body)
		if len(id) != 2 {
			writeRealPiText(response, "Missing native Execute ID")
			return
		}
		writeRealPiToolCall(response, "control-check", "bash", map[string]any{
			"command": strings.ReplaceAll(realPiControlCommand, "NATIVE_ID", string(id[1])), "timeout": 15,
		})
	default:
		for _, message := range request.Messages {
			if message.Role == "tool" {
				text, _ := json.Marshal(message.Content)
				if strings.Contains(string(text), "CONTROL_VERIFIED") {
					model.mu.Lock()
					model.controlOK = true
					model.mu.Unlock()
				}
			}
		}
		writeRealPiText(response, "TSecBench reports all challenge flags complete.")
	}
}

const realPiControlCommand = `python3 - <<'PY'
import json, os, pathlib, subprocess, time
root = pathlib.Path.cwd()
script = root / '.agents/skills/ctf-orchestrator/scripts/dispatch.py'
assert os.environ.get('CYBERPENDA_HOSTED_PI') == '1'
def cli(*args, ok=True):
    p = subprocess.run(['python3', str(script), *args, '--ws', str(root)], capture_output=True, text=True, timeout=12)
    assert (p.returncode == 0) == ok, p.stdout + p.stderr
    return p.stdout
(root / 'control-challenges.json').write_text(json.dumps({'challenges': [{'unique_code': 'control', 'description': 'control', 'total_score': 100, 'flag_count': 1}]}))
cli('init', '--challenges', str(root / 'control-challenges.json'))
cli('assemble', 'control')
cli('take')
cli('bind', 'd001', 'NATIVE_ID')
deadline = time.monotonic() + 8
while not (root / 'control-child-running').exists() and time.monotonic() < deadline:
    time.sleep(0.05)
assert (root / 'control-child-running').exists(), 'Child tool did not start'
cli('finish', 'd001', '--agent-id', 'NATIVE_ID', '--status', 'failed', ok=False)
cli('stop', 'd001')
assert 'stopped' in cli('wait', '--timeout', '8')
cli('finish', 'd001', '--agent-id', 'NATIVE_ID', '--status', 'stopped')
assert not (root / 'control-child-leaked').exists()
assert json.loads((root / 'graph/ledger.json').read_text())['dispatches']['d001']['terminal_status'] == 'stopped'
print('CONTROL_VERIFIED')
PY`
