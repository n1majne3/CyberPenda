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
