package runtimeprofile_test

import (
	"testing"

	"pentest/internal/modelprovider"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

func TestFindLaunchProfileMatchesProviderAndModelProvider(t *testing.T) {
	profiles := []runtimeprofile.Profile{
		{
			ID:       "p1",
			Provider: runtimeprofile.ProviderCodex,
			Fields: runtimeprofile.Fields{
				ModelProviderID: "mimo",
				ModelOverride:   "mimo-v2-flash",
			},
		},
		{
			ID:       "p2",
			Provider: runtimeprofile.ProviderCodex,
			Fields: runtimeprofile.Fields{
				ModelProviderID: "mimo",
			},
		},
	}

	found, ok := runtimeprofile.FindLaunchProfile(profiles, runtimeprofile.LaunchSelection{
		Provider:        runtimeprofile.ProviderCodex,
		ModelProviderID: "mimo",
	})
	if !ok || found.ID != "p2" {
		t.Fatalf("expected default override profile, got %#v", found)
	}

	found, ok = runtimeprofile.FindLaunchProfile(profiles, runtimeprofile.LaunchSelection{
		Provider:        runtimeprofile.ProviderCodex,
		ModelProviderID: "mimo",
		ModelOverride:   "mimo-v2-flash",
	})
	if !ok || found.ID != "p1" {
		t.Fatalf("expected override profile, got %#v", found)
	}
}

func TestResolveLaunchProfileCreatesMinimalProfileWhenMissing(t *testing.T) {
	db, err := store.Open("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	providers := modelprovider.NewService(db)
	provider, err := providers.Create(modelprovider.CreateRequest{
		Name:      "MiMo",
		BaseURL:   "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"mimo"}, DefaultModel: "mimo"},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	svc := runtimeprofile.NewService(db)

	resolution, err := svc.ResolveLaunchProfile(runtimeprofile.LaunchSelection{
		Provider:        runtimeprofile.ProviderCodex,
		ModelProviderID: provider.ID,
	}, provider.Name)
	if err != nil {
		t.Fatalf("resolve launch profile: %v", err)
	}
	if !resolution.Created {
		t.Fatal("expected created profile")
	}
	if resolution.Profile.Provider != runtimeprofile.ProviderCodex {
		t.Fatalf("provider = %q", resolution.Profile.Provider)
	}
	if resolution.Profile.Fields.ModelProviderID != provider.ID {
		t.Fatalf("fields = %#v", resolution.Profile.Fields)
	}
	if resolution.Profile.Fields.DefaultRunner != "sandbox" {
		t.Fatalf("default runner = %q", resolution.Profile.Fields.DefaultRunner)
	}
	if resolution.Profile.Kind != runtimeprofile.ProfileKindLaunchResolve {
		t.Fatalf("kind = %q, want launch_resolve", resolution.Profile.Kind)
	}

	second, err := svc.ResolveLaunchProfile(runtimeprofile.LaunchSelection{
		Provider:        runtimeprofile.ProviderCodex,
		ModelProviderID: provider.ID,
	}, provider.Name)
	if err != nil {
		t.Fatalf("resolve again: %v", err)
	}
	if second.Created || second.Profile.ID != resolution.Profile.ID {
		t.Fatalf("expected reuse, got %#v", second)
	}
}