package configtext

import (
	"encoding/json"
	"testing"
)

func TestMergeJSONObjectsOneDocument(t *testing.T) {
	structured := "{\n  \"env\": {},\n  \"permissions\": {\"allow\": [\"tool\"]}\n}\n"
	operator := "{\n  \"enabledPlugins\" : { \"warp@claude-code-warp\" : true }\n}"
	got, err := MergeJSONObjects(structured, operator)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !json.Valid([]byte(got)) {
		t.Fatalf("merged JSON must be valid: %s", got)
	}
}

func TestRenderJSONTargetNestedMember(t *testing.T) {
	source := `{"enabledPlugins":{"known":true,"unknown" : true}}`
	target := map[string]any{"enabledPlugins": map[string]any{"unknown": true}}
	got, err := RenderJSONTarget(source, target)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := `{"enabledPlugins":{"unknown" : true}}`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRenderJSONTargetAddsNewMembersInKeyOrder(t *testing.T) {
	source := `{"a": 1}`
	target := map[string]any{"a": float64(1), "z": 2, "m": 3, "b": 4, "c": 5}
	want := `{"a": 1,"b": 4,"c": 5,"m": 3,"z": 2}`
	for range 20 {
		got, err := RenderJSONTarget(source, target)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}
