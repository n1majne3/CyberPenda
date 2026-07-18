package mcpserver_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"pentest/internal/blackboardv2"
	"pentest/internal/mcpserver"
)

func connectMCPV2(t *testing.T, deps mcpserver.Deps) *sdkmcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := mcpserver.New(deps)
	t1, t2 := sdkmcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("connect server: %v", err)
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestBlackboardChangeMCPSchemaAdvertisesObjectiveAndAttemptCreateEnvelope(t *testing.T) {
	// The advertised trusted MCP schema must guide objective and attempt
	// creation through the accepted v2 change envelope.
	session := connectMCPV2(t, mcpserver.Deps{
		BlackboardV2: blackboardv2.NewService(nil),
	})
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list MCP tools: %v", err)
	}

	var inputSchema any
	for _, tool := range listed.Tools {
		if tool.Name == "blackboard_change" {
			inputSchema = tool.InputSchema
			break
		}
	}
	if inputSchema == nil {
		t.Fatal("blackboard_change tool is not registered")
	}
	schemaJSON, err := json.Marshal(inputSchema)
	if err != nil {
		t.Fatalf("marshal blackboard_change input schema: %v", err)
	}
	for _, propertyContract := range []string{
		"op is a verb: create creates a record",
		"create requires key, type, and record",
		"record types such as objective and attempt belong in type, never op",
		"objective record: objective (required), status (open only)",
		"attempt record: summary (required), status (open only)",
	} {
		if !strings.Contains(string(schemaJSON), propertyContract) {
			t.Errorf("blackboard_change input schema does not advertise %q: %s", propertyContract, schemaJSON)
		}
	}
}
