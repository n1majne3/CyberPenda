package runtimeprofile_test

import (
	"errors"
	"strings"
	"testing"

	"pentest/internal/runtimeprofile"
)

func TestProfileCustomConfigFileRoundTrips(t *testing.T) {
	service := newTestService(t)

	const overlay = "# keep my comments\nterminal:\n  backend: local\n"
	created, err := service.Create("Hermes Preset", runtimeprofile.ProviderHermes, runtimeprofile.Fields{
		CustomConfigFile: overlay,
	})
	if err != nil {
		t.Fatalf("create with custom config file: %v", err)
	}
	if created.Fields.CustomConfigFile != overlay {
		t.Fatalf("custom config file not returned, got %q", created.Fields.CustomConfigFile)
	}

	fetched, err := service.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fetched.Fields.CustomConfigFile != overlay {
		t.Fatalf("custom config file not persisted verbatim, got %q", fetched.Fields.CustomConfigFile)
	}
}

func TestProviderSwitchRequiresOverlayClearConfirmation(t *testing.T) {
	service := newTestService(t)

	created, err := service.Create("Claude Preset", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		CustomConfigFile: "enabledPlugins:\n  warp@claude-code-warp: true\n",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Switching provider without confirmation is refused; the overlay stays.
	var switchErr *runtimeprofile.ProviderSwitchNeedsOverlayClearError
	if _, err := service.Update(created.ID, "", runtimeprofile.ProviderHermes, runtimeprofile.Fields{}, false, false); !errors.As(err, &switchErr) {
		t.Fatalf("expected provider switch overlay confirmation error, got %v", err)
	}
	kept, err := service.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if kept.Provider != runtimeprofile.ProviderClaudeCode || kept.Fields.CustomConfigFile == "" {
		t.Fatalf("refused switch must keep provider and overlay, got %q %q", kept.Provider, kept.Fields.CustomConfigFile)
	}

	// Confirming clears the overlay and switches.
	switched, err := service.Update(created.ID, "", runtimeprofile.ProviderHermes, runtimeprofile.Fields{}, false, true)
	if err != nil {
		t.Fatalf("confirmed switch: %v", err)
	}
	if switched.Provider != runtimeprofile.ProviderHermes {
		t.Fatalf("switched provider = %q", switched.Provider)
	}
	if switched.Fields.CustomConfigFile != "" {
		t.Fatalf("confirmed switch must clear the overlay, got %q", switched.Fields.CustomConfigFile)
	}
}

func TestProviderSwitchConfirmClearsOverlayEvenWhenFieldsCarryIt(t *testing.T) {
	service := newTestService(t)
	overlay := `{"enabledPlugins":{"warp@claude-code-warp":true}}`
	created, err := service.Create("Claude Preset", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		CustomConfigFile: overlay,
		Model:            "claude-opus-4-6",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	switched, err := service.Update(created.ID, "Claude Preset", runtimeprofile.ProviderHermes, runtimeprofile.Fields{
		Model:            "claude-opus-4-6",
		CustomConfigFile: overlay,
	}, true, true)
	if err != nil {
		t.Fatalf("confirmed switch with fields: %v", err)
	}
	if switched.Provider != runtimeprofile.ProviderHermes {
		t.Fatalf("switched provider = %q", switched.Provider)
	}
	if switched.Fields.CustomConfigFile != "" {
		t.Fatalf("confirmed switch must clear overlay even when fields resubmit it, got %q", switched.Fields.CustomConfigFile)
	}
}

func TestCreateRefusesSecretShapedCustomConfigFile(t *testing.T) {
	service := newTestService(t)
	_, err := service.Create("Claude Secret Overlay", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		CustomConfigFile: `{"toolConfig":{"license":"sk-ant-api03-live-1234567890abcdef"}}`,
	})
	if err == nil {
		t.Fatal("create must refuse a secret-shaped Custom Config File")
	}
	if !strings.Contains(err.Error(), "secret-shaped") {
		t.Fatalf("error = %v, want secret-shaped refusal", err)
	}
}

func TestUpdateRefusesSecretShapedCustomConfigFile(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Claude Overlay", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		CustomConfigFile: `{"enabledPlugins":{"warp@claude-code-warp":true}}`,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = service.Update(created.ID, "", "", runtimeprofile.Fields{
		CustomConfigFile: `{"toolConfig":{"license":"sk-ant-api03-live-1234567890abcdef"}}`,
	}, true, false)
	if err == nil {
		t.Fatal("update must refuse a secret-shaped Custom Config File")
	}
	if !strings.Contains(err.Error(), "secret-shaped") {
		t.Fatalf("error = %v, want secret-shaped refusal", err)
	}
}

func TestCreateRefusesSecretNamedOverlayKey(t *testing.T) {
	service := newTestService(t)
	_, err := service.Create("Claude Secret Key", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		CustomConfigFile: `{"api_key":"not-a-token-shape"}`,
	})
	if err == nil {
		t.Fatal("create must refuse a secret-named Custom Config File key")
	}
	lower := strings.ToLower(err.Error())
	if !strings.Contains(lower, "secret") && !strings.Contains(lower, "api_key") {
		t.Fatalf("error = %v, want secret-key refusal", err)
	}
}

func TestCreateRefusesSecretNamedKeyInsideArray(t *testing.T) {
	service := newTestService(t)
	_, err := service.Create("Claude Secret Array", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		CustomConfigFile: `{"servers":[{"api_key":"not-a-token-shape"}]}`,
	})
	if err == nil {
		t.Fatal("create must refuse a secret-named key nested in an array")
	}
}
