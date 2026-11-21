package blackboardv2contract

import (
	"bytes"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func compileContractSchema(raw []byte, name string) (*jsonschema.Schema, error) {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode Blackboard v2 schema: %w", err)
	}
	root, ok := document.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Blackboard v2 schema must be an object")
	}
	definitions, _ := root["$defs"].(map[string]any)
	if _, exists := definitions[name]; !exists {
		return nil, fmt.Errorf("unknown Blackboard v2 schema %q", name)
	}
	const schemaURL = "https://cyberpenda.local/schemas/blackboard-v2.schema.json"
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource(schemaURL, document); err != nil {
		return nil, fmt.Errorf("register Blackboard v2 schema: %w", err)
	}
	return compiler.Compile(schemaURL + "#/$defs/" + name)
}
