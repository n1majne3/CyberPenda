package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"pentest/internal/blackboardv2"
	"pentest/internal/blackboardv2contract"
	"pentest/internal/projectinterface"
)

// V2Deps are the domain services and trusted session for Blackboard v2 MCP tools.
// Project, Task, and Continuation identity come only from the Continuation
// Interface Grant resolved into Grant; model-facing arguments never carry them.
type V2Deps struct {
	BlackboardV2 *blackboardv2.Service
	// Grant is the Continuation Interface capability bound to this MCP session.
	Grant *projectinterface.Grant
	// GrantError, when set, is the structured failure from resolving a
	// presented-but-invalid capability token.
	GrantError *blackboardv2.Error
}

// NewV2 builds an MCP server that registers exactly the six Blackboard v2
// trusted tools. Input schemas are closed objects generated from the frozen
// v2 contract definitions.
func NewV2(deps V2Deps) *sdkmcp.Server {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "pentest-agent",
		Version: "0.1.0",
	}, nil)
	if deps.BlackboardV2 == nil {
		return server
	}
	registerBlackboardV2Tools(server, deps)
	return server
}

func registerBlackboardV2Tools(server *sdkmcp.Server, deps V2Deps) {
	harness, err := blackboardv2contract.NewHarness()
	if err != nil {
		panic(fmt.Errorf("load Blackboard v2 contract for trusted MCP: %w", err))
	}
	tools, err := harness.TrustedTools()
	if err != nil {
		panic(fmt.Errorf("load Blackboard v2 trusted tools: %w", err))
	}
	schemas := make(map[string]*jsonschema.Schema, len(tools))
	for _, tool := range tools {
		schema, schemaErr := harness.ToolInputSchema(tool.InputSchema)
		if schemaErr != nil {
			panic(fmt.Errorf("load MCP input schema for %s: %w", tool.Name, schemaErr))
		}
		schemas[tool.Name] = schema
	}

	for _, tool := range tools {
		switch tool.Name {
		case "blackboard_change":
			sdkmcp.AddTool(server, &sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: schemas[tool.Name],
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args blackboardv2.ChangeBatch) (*sdkmcp.CallToolResult, any, error) {
				_ = req
				return deps.serveV2(ctx, true, func(ctx context.Context, projectID, continuationID string) (any, error) {
					return deps.BlackboardV2.ApplyForContinuation(ctx, projectID, continuationID, args)
				})
			})
		case "blackboard_read":
			sdkmcp.AddTool(server, &sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: schemas[tool.Name],
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args blackboardV2ReadArgs) (*sdkmcp.CallToolResult, any, error) {
				_ = req
				return deps.serveV2(ctx, true, func(ctx context.Context, projectID, _ string) (any, error) {
					return deps.BlackboardV2.ReadCurrent(ctx, projectID, args.Key)
				})
			})
		case "blackboard_history":
			sdkmcp.AddTool(server, &sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: schemas[tool.Name],
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args blackboardV2HistoryArgs) (*sdkmcp.CallToolResult, any, error) {
				_ = req
				return deps.serveV2(ctx, true, func(ctx context.Context, projectID, _ string) (any, error) {
					return deps.BlackboardV2.ReadHistory(ctx, projectID, args.Key, blackboardv2.HistoryOptions{
						Cursor: args.Cursor, Limit: args.Limit,
					})
				})
			})
		case "blackboard_retain_evidence":
			sdkmcp.AddTool(server, &sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: schemas[tool.Name],
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args blackboardv2.RetainEvidenceRequest) (*sdkmcp.CallToolResult, any, error) {
				_ = req
				return deps.serveV2(ctx, true, func(ctx context.Context, projectID, continuationID string) (any, error) {
					return deps.BlackboardV2.RetainEvidenceForContinuation(ctx, projectID, continuationID, args)
				})
			})
		case "blackboard_checkpoint_attempt":
			sdkmcp.AddTool(server, &sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: schemas[tool.Name],
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args blackboardv2.CheckpointAttemptRequest) (*sdkmcp.CallToolResult, any, error) {
				_ = req
				return deps.serveV2(ctx, true, func(ctx context.Context, projectID, continuationID string) (any, error) {
					return deps.BlackboardV2.CheckpointAttemptForContinuation(ctx, projectID, continuationID, args)
				})
			})
		case "blackboard_finish":
			sdkmcp.AddTool(server, &sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: schemas[tool.Name],
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args blackboardv2.FinishContinuationRequest) (*sdkmcp.CallToolResult, any, error) {
				_ = req
				// Exact Finish results never attach a new live synchronization sibling.
				return deps.serveV2(ctx, false, func(ctx context.Context, projectID, continuationID string) (any, error) {
					return deps.BlackboardV2.FinishContinuation(ctx, projectID, continuationID, args)
				})
			})
		default:
			panic(fmt.Errorf("unhandled Blackboard v2 trusted tool %q", tool.Name))
		}
	}
}

type blackboardV2ReadArgs struct {
	Key string `json:"key"`
}

type blackboardV2HistoryArgs struct {
	Key    string `json:"key"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

func (deps V2Deps) serveV2(ctx context.Context, attachSync bool, action func(context.Context, string, string) (any, error)) (*sdkmcp.CallToolResult, any, error) {
	grant, authErr := deps.requireGrant()
	if authErr != nil {
		return toolBlackboardV2Error(authErr, nil)
	}
	if !grant.Status().IsReadable() {
		return toolBlackboardV2Error(blackboardV2AuthError("authority_denied", "Continuation Interface capability is revoked", "authorization"), nil)
	}
	authority, err := deps.BlackboardV2.AuthorizeContinuationBinding(ctx, grant.ProjectID, grant.TaskID, grant.ContinuationID, attachSync)
	if err != nil {
		return toolBlackboardV2Error(asBlackboardV2Error(err), nil)
	}
	result, err := action(ctx, grant.ProjectID, grant.ContinuationID)
	if err != nil {
		var sync *blackboardv2.SynchronizationAttachment
		if authority.Live && authority.Sync.Pending && attachSync {
			if attachment, syncErr := deps.BlackboardV2.SynchronizeContinuation(ctx, grant.ProjectID, grant.TaskID, grant.ContinuationID, authority.Sync.FromRevision); syncErr == nil {
				sync = &attachment
			}
		}
		return toolBlackboardV2Error(asBlackboardV2Error(err), sync)
	}
	if authority.Live && authority.Sync.Pending && attachSync {
		attachment, syncErr := deps.BlackboardV2.SynchronizeContinuation(ctx, grant.ProjectID, grant.TaskID, grant.ContinuationID, authority.Sync.FromRevision)
		if syncErr != nil {
			return toolBlackboardV2Error(asBlackboardV2Error(syncErr), nil)
		}
		return toolBlackboardV2JSON(result, &attachment)
	}
	return toolBlackboardV2JSON(result, nil)
}

func (deps V2Deps) requireGrant() (projectinterface.Grant, *blackboardv2.Error) {
	if deps.Grant != nil {
		return *deps.Grant, nil
	}
	if deps.GrantError != nil {
		return projectinterface.Grant{}, deps.GrantError
	}
	return projectinterface.Grant{}, blackboardV2AuthError("authority_denied", "this tool requires a Continuation Interface capability", "authorization")
}

func toolBlackboardV2JSON(payload any, sync *blackboardv2.SynchronizationAttachment) (*sdkmcp.CallToolResult, any, error) {
	if sync == nil {
		return toolJSON(payload)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, nil, err
	}
	syncRaw, err := json.Marshal(sync)
	if err != nil {
		return nil, nil, err
	}
	object["sync"] = syncRaw
	return toolJSON(object)
}

func toolBlackboardV2Error(err *blackboardv2.Error, sync *blackboardv2.SynchronizationAttachment) (*sdkmcp.CallToolResult, any, error) {
	if err == nil {
		err = blackboardV2AuthError("internal", "unexpected Blackboard v2 failure", "internal")
	}
	envelope := struct {
		Error *blackboardv2.Error                     `json:"error"`
		Sync  *blackboardv2.SynchronizationAttachment `json:"sync,omitempty"`
	}{Error: err, Sync: sync}
	data, _ := json.Marshal(envelope)
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(data)}},
		IsError: true,
	}, envelope, nil
}

func asBlackboardV2Error(err error) *blackboardv2.Error {
	var semantic *blackboardv2.Error
	if errors.As(err, &semantic) {
		return semantic
	}
	return &blackboardv2.Error{Code: "internal", Message: err.Error(), Retryable: false}
}

func blackboardV2AuthError(code, message, path string) *blackboardv2.Error {
	return &blackboardv2.Error{Code: code, Message: message, Path: path, Retryable: false}
}
