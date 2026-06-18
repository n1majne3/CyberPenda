package runtimeprofile_test

import (
	"testing"

	"pentest/internal/runtimeprofile"
)

func TestMergeAPIKeysPreservesConfiguredSentinel(t *testing.T) {
	merged := runtimeprofile.MergeAPIKeys(
		map[string]string{"OPENAI_API_KEY": "sk-secret"},
		map[string]string{"OPENAI_API_KEY": runtimeprofile.ConfiguredAPIKeySentinel},
	)
	if merged["OPENAI_API_KEY"] != "sk-secret" {
		t.Fatalf("expected existing key preserved, got %#v", merged)
	}
}

func TestMergeAPIKeysReplacesWhenNewValueProvided(t *testing.T) {
	merged := runtimeprofile.MergeAPIKeys(
		map[string]string{"OPENAI_API_KEY": "sk-old"},
		map[string]string{"OPENAI_API_KEY": "sk-new"},
	)
	if merged["OPENAI_API_KEY"] != "sk-new" {
		t.Fatalf("expected updated key, got %#v", merged)
	}
}

func TestSanitizeProfileRedactsAPIKeys(t *testing.T) {
	sanitized := runtimeprofile.SanitizeProfile(runtimeprofile.Profile{
		Fields: runtimeprofile.Fields{
			APIKeys: map[string]string{"OPENAI_API_KEY": "sk-secret"},
		},
	})
	if sanitized.Fields.APIKeys["OPENAI_API_KEY"] != runtimeprofile.ConfiguredAPIKeySentinel {
		t.Fatalf("expected redacted api key, got %#v", sanitized.Fields.APIKeys)
	}
}

func TestMaterializedAPIKeysSkipsSentinel(t *testing.T) {
	got := runtimeprofile.MaterializedAPIKeys(runtimeprofile.Profile{
		Fields: runtimeprofile.Fields{
			APIKeys: map[string]string{"OPENAI_API_KEY": runtimeprofile.ConfiguredAPIKeySentinel},
		},
	})
	if len(got) != 0 {
		t.Fatalf("expected no materialized keys, got %#v", got)
	}
}