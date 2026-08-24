package configtext

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestSplitTOMLBlocksRepeatedAndMultiline(t *testing.T) {
	raw := "my_list = [\n  \"a\",\n  \"b\",\n]\n[[custom.backends]]\nname = \"a\"\n[[custom.backends]]\nname = \"b\"\n"
	blocks, err := SplitTOMLBlocks(raw)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks: %#v", len(blocks), blocks)
	}
	if !strings.Contains(blocks[0].Raw, "\"a\",\n  \"b\",") {
		t.Fatalf("multiline value split: %#v", blocks[0])
	}
	if blocks[1].Key != "custom\x00backends" || blocks[2].Key != "custom\x00backends" {
		t.Fatalf("array tables must be distinct repeated blocks: %#v", blocks)
	}
}

func TestMergeTOMLDocumentsPreservesRepeatedTables(t *testing.T) {
	seed := "approval_policy = \"never\"\n[mcp_servers]\n"
	remainder := "[[custom.backends]]\nname = \"a\"\n[[custom.backends]]\nname = \"b\"\n"
	got, err := MergeTOMLDocuments(seed, remainder)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if strings.Count(got, "[[custom.backends]]") != 2 {
		t.Fatalf("repeated tables lost: %s", got)
	}
	var doc map[string]any
	if _, err := toml.Decode(got, &doc); err != nil {
		t.Fatalf("merged TOML invalid: %v\n%s", err, got)
	}
}

func TestSplitTOMLBlocksHeaderCommentAndQuotedKeys(t *testing.T) {
	raw := "\"custom.table\" = 2\n\n[custom] # header\nx = 1\n"
	blocks, err := SplitTOMLBlocks(raw)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(blocks) != 2 || blocks[0].Key != "custom.table" || !strings.Contains(blocks[1].Raw, "# header") {
		t.Fatalf("header comment/quoted key lost: %#v", blocks)
	}
}

func TestSplitTOMLBlocksMultilineStringFourQuotes(t *testing.T) {
	for _, raw := range []string{
		"x = \"\"\"abc\"\"\"\"\ny = 2\n",
		"x = '''abc''''\ny = 2\n",
	} {
		blocks, err := SplitTOMLBlocks(raw)
		if err != nil || len(blocks) != 2 {
			t.Fatalf("valid multiline string must split: err=%v blocks=%#v", err, blocks)
		}
	}
}

func TestMergeTOMLDocumentsQuotedCollisionUsesStructuredValue(t *testing.T) {
	got, err := MergeTOMLDocuments("approval_policy = \"never\"\n", "'approval_policy' = \"operator\"\n")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var doc map[string]any
	if _, err := toml.Decode(got, &doc); err != nil {
		t.Fatalf("merge must stay valid: %v\n%s", err, got)
	}
	if doc["approval_policy"] != "never" {
		t.Fatalf("structured value must win: %#v", doc)
	}
}

func TestMergeTOMLDocumentsSameTableKeepsOperatorChild(t *testing.T) {
	remainder := "[a]\ny   =   \"v\" # keep inline\n"
	got, err := MergeTOMLDocuments("[a]\nx = 1\n", remainder)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var doc map[string]any
	if _, err := toml.Decode(got, &doc); err != nil {
		t.Fatalf("merge must stay valid: %v\n%s", err, got)
	}
	a, _ := doc["a"].(map[string]any)
	if a["x"] != int64(1) || a["y"] != "v" {
		t.Fatalf("table children must merge recursively: %#v\n%s", doc, got)
	}
	if !strings.Contains(got, "y   =   \"v\" # keep inline") {
		t.Fatalf("operator-only child must stay byte-for-byte:\n%s", got)
	}
}

func TestMergeTOMLDocumentsSameTableNestedSubtableNotFlattened(t *testing.T) {
	got, err := MergeTOMLDocuments("[a]\nx = 1\n", "[a]\ny = 2\n\n[a.nested]\nz = 3\n")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var doc map[string]any
	if _, err := toml.Decode(got, &doc); err != nil {
		t.Fatalf("merge must stay valid: %v\n%s", err, got)
	}
	a, _ := doc["a"].(map[string]any)
	nested, _ := a["nested"].(map[string]any)
	if nested["z"] != int64(3) || a["y"] != int64(2) {
		t.Fatalf("nested subtable must not flatten: %#v\n%s", doc, got)
	}
}
