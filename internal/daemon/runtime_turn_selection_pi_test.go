package daemon

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/modelprovider"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
	"pentest/internal/testsupport/fakeprovider"
)

func TestPiNativeSteerAcceptsCrossProviderWithoutRestart(t *testing.T) {
	session := fakeprovider.New(fakeprovider.Config{
		SessionID: "pi-session-1", ActiveTurnID: "pi-work-turn-1", ActiveTurnKind: runtime.RuntimeTurnKindWork,
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true, InTurnSteer: true,
		},
	})
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	primary, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Primary", BaseURL: "https://a.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"m1"}, DefaultModel: "m1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	alternate, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Alternate", BaseURL: "https://b.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"m2"}, DefaultModel: "m2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(primary.APIKeyEnv, "sk-a")
	t.Setenv(alternate.APIKeyEnv, "sk-b")

	profile, err := server.profiles.Create("Pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		ModelProviderID: primary.ID, ModelOverride: "m1", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Type: task.TypePentest, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox),
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "pi", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	projected := testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox)
	projected["config_projection"] = map[string]any{"projected_model_provider_ids": []string{primary.ID, alternate.ID}}
	if _, err := server.tasks.RecordRuntimeConfig(created.ID, profile.ID, projected); err != nil {
		t.Fatal(err)
	}
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}

	versionsBefore, err := server.tasks.RuntimeConfigVersions(created.ID)
	if err != nil {
		t.Fatal(err)
	}

	body := `{
		"request_id":"pi-provider-switch-1",
		"message":"switch to alternate",
		"model_provider_id":` + quoteJSON(alternate.ID) + `,
		"model":"m2",
		"reasoning_effort":"xhigh"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", resp.Code, resp.Body.String())
	}

	waitForProviderRequests(t, session, 1)
	requests := session.LastRequests()
	last := requests[len(requests)-1]
	if last.ModelProviderID != alternate.ID {
		t.Fatalf("model_provider_id = %q, want %q", last.ModelProviderID, alternate.ID)
	}
	if last.Model != "m2" {
		t.Fatalf("model = %q, want m2", last.Model)
	}
	if last.RequestedReasoningEffort != "xhigh" {
		t.Fatalf("effort = %q, want xhigh", last.RequestedReasoningEffort)
	}
	if last.ProviderTurnID != "pi-work-turn-1" {
		t.Fatalf("replacement request fence = %q, want pi-work-turn-1", last.ProviderTurnID)
	}

	versionsAfter, err := server.tasks.RuntimeConfigVersions(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versionsAfter) != len(versionsBefore) {
		t.Fatalf("Pi cross-provider native turn must not mint Runtime Config Version: before=%d after=%d", len(versionsBefore), len(versionsAfter))
	}

	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundConversation := false
	for _, event := range events {
		if event.Kind == task.EventKindConversation && event.Payload["request_id"] == "pi-provider-switch-1" {
			foundConversation = true
			if event.Payload["model_provider_id"] != alternate.ID || event.Payload["model"] != "m2" {
				t.Fatalf("conversation selection = %#v", event.Payload)
			}
			if event.Payload["requested_reasoning_effort"] != "xhigh" {
				t.Fatalf("conversation effort = %#v", event.Payload["requested_reasoning_effort"])
			}
			if event.Payload["delivery"] != "native_steer" {
				t.Fatalf("delivery = %#v", event.Payload["delivery"])
			}
			if event.Payload["mode"] != string(runtime.ProviderSessionModeInterruptThenReplace) {
				t.Fatalf("Pi selection-changing mode = %#v, want interrupt_then_replace", event.Payload["mode"])
			}
		}
	}
	if !foundConversation {
		t.Fatal("expected native_steer conversation event")
	}
}

// TestPiNativeSteerRejectsProviderOutsideProjectedSet keeps the projected set
// fixed until the next Config Projection / Runtime restart.
func TestPiNativeSteerRejectsProviderOutsideProjectedSet(t *testing.T) {
	session := fakeprovider.New(fakeprovider.Config{
		SessionID: "pi-session-2",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	primary, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Primary", BaseURL: "https://a.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"m1"}, DefaultModel: "m1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	outside, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Outside", BaseURL: "https://out.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"out"}, DefaultModel: "out"},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		ModelProviderID: primary.ID, ModelOverride: "m1",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Type: task.TypePentest, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox),
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "pi", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	projected := testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox)
	projected["config_projection"] = map[string]any{"projected_model_provider_ids": []string{primary.ID}}
	if _, err := server.tasks.RecordRuntimeConfig(created.ID, profile.ID, projected); err != nil {
		t.Fatal(err)
	}
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}

	body := `{
		"request_id":"pi-outside-1",
		"message":"use outside",
		"model_provider_id":` + quoteJSON(outside.ID) + `,
		"model":"out"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d body %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "restart") {
		t.Fatalf("expected restart guidance, body %s", resp.Body.String())
	}
	if len(session.LastRequests()) != 0 {
		t.Fatalf("outside provider must not reach live session: %#v", session.LastRequests())
	}
}

func TestPiNativeSteerFailsClosedWithoutProjectedSet(t *testing.T) {
	session := fakeprovider.New(fakeprovider.Config{
		SessionID: "pi-session-legacy",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	primary, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Primary", BaseURL: "https://a.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"m1"}, DefaultModel: "m1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	alternate, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Alternate", BaseURL: "https://b.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"m2"}, DefaultModel: "m2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		ModelProviderID: primary.ID, ModelOverride: "m1",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Type: task.TypePentest, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox),
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "pi", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	legacyProjection := testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox)
	if _, err := server.tasks.RecordRuntimeConfig(created.ID, profile.ID, legacyProjection); err != nil {
		t.Fatal(err)
	}
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}

	body := `{
		"request_id":"pi-legacy-cross-1",
		"message":"switch without projected set",
		"model_provider_id":` + quoteJSON(alternate.ID) + `,
		"model":"m2"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d body %s, want 409 fail-closed", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "restart") {
		t.Fatalf("expected restart guidance, body %s", resp.Body.String())
	}
	if len(session.LastRequests()) != 0 {
		t.Fatalf("legacy missing projected set must not native-switch providers: %#v", session.LastRequests())
	}
}

// TestPiV2LaunchDoesNotDeadlockListingGlobalProviders proves the daemon lists
// global Model Providers before CreateContinuation and projects from the
// immutable snapshot, so Pi launch never re-enters modelProviders.Service
// while the continuity transaction holds SQLite locks.
func TestPiV2LaunchDoesNotDeadlockListingGlobalProviders(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	primary, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Primary", BaseURL: "https://a.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"m1", "m2"}, DefaultModel: "m1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	alternate, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Alternate", BaseURL: "https://b.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"claude-alt"}, DefaultModel: "claude-alt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(primary.APIKeyEnv, "sk-primary")
	t.Setenv(alternate.APIKeyEnv, "sk-alternate")

	profile, err := server.profiles.Create("Pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		ModelProviderID: primary.ID, ModelOverride: "m1", BinaryPath: "/usr/bin/pi",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID,
		Type:      task.TypePentest, Goal: "inspect example.com",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox),
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "")
		if err != nil {
			done <- err
			return
		}
		if plan.PreparedProjection == nil {
			done <- fmt.Errorf("launch plan missing prepared Config Projection")
			return
		}
		_, bound, err := server.prepareBlackboardV2ContinuationLaunch(created, plan, created.Goal)
		if err != nil {
			done <- err
			return
		}
		// Projected set must be fixed into captured runtime config.
		ids, _ := bound.CapturedRuntimeConfig["projected_model_provider_ids"].([]string)
		if len(ids) == 0 {
			if raw, ok := bound.CapturedRuntimeConfig["projected_model_provider_ids"].([]any); ok {
				for _, v := range raw {
					ids = append(ids, fmt.Sprint(v))
				}
			}
		}
		if len(ids) < 2 {
			done <- fmt.Errorf("projected_model_provider_ids = %#v", bound.CapturedRuntimeConfig["projected_model_provider_ids"])
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Pi v2 launch: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Pi v2 launch deadlocked listing global Model Providers inside CreateContinuation")
	}
}
