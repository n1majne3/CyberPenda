package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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

	// Register with the raw AddTool path so tools/list still advertises the closed
	// contract schemas while every call reaches controlled validation that returns
	// the compact v2 invalid_schema envelope (never generic SDK validation text).
	for _, tool := range tools {
		tool := tool
		inputSchema := schemas[tool.Name]
		switch tool.Name {
		case "blackboard_change":
			server.AddTool(&sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: inputSchema,
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				var args blackboardv2.ChangeBatch
				if decodeErr := decodeV2ToolArgs(harness, tool.InputSchema, req.Params.Arguments, &args); decodeErr != nil {
					return toolBlackboardV2ErrorResult(decodeErr)
				}
				// Exact replay remains available after Finish/supersession; response-loss
				// retries redeliver the same sync attachment via idempotency fingerprint.
				return deps.callV2WithFingerprint(ctx, false, true, blackboardv2.SynchronizationDeliveryFingerprint("change", args.IdempotencyKey), func(ctx context.Context, projectID, continuationID string) (any, error) {
					return deps.BlackboardV2.ApplyForContinuation(ctx, projectID, continuationID, args)
				})
			})
		case "blackboard_read":
			server.AddTool(&sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: inputSchema,
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				var args blackboardV2ReadArgs
				if decodeErr := decodeV2ToolArgs(harness, tool.InputSchema, req.Params.Arguments, &args); decodeErr != nil {
					return toolBlackboardV2ErrorResult(decodeErr)
				}
				// Live read/current knowledge authority only; closed Continuations
				// keep exact write/finish replay but not current knowledge reads.
				// Reads are Pending-only (no durable request fingerprint).
				return deps.callV2WithFingerprint(ctx, true, true, "", func(ctx context.Context, projectID, _ string) (any, error) {
					return deps.BlackboardV2.ReadCurrent(ctx, projectID, args.Key)
				})
			})
		case "blackboard_history":
			server.AddTool(&sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: inputSchema,
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				var args blackboardV2HistoryArgs
				if decodeErr := decodeV2ToolArgs(harness, tool.InputSchema, req.Params.Arguments, &args); decodeErr != nil {
					return toolBlackboardV2ErrorResult(decodeErr)
				}
				return deps.callV2WithFingerprint(ctx, true, true, "", func(ctx context.Context, projectID, _ string) (any, error) {
					return deps.BlackboardV2.ReadHistory(ctx, projectID, args.Key, blackboardv2.HistoryOptions{
						Cursor: args.Cursor, Limit: args.Limit,
					})
				})
			})
		case "blackboard_retain_evidence":
			server.AddTool(&sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: inputSchema,
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				var args blackboardv2.RetainEvidenceRequest
				if decodeErr := decodeV2ToolArgs(harness, tool.InputSchema, req.Params.Arguments, &args); decodeErr != nil {
					return toolBlackboardV2ErrorResult(decodeErr)
				}
				return deps.callV2WithFingerprint(ctx, false, true, blackboardv2.SynchronizationDeliveryFingerprint("evidence", args.IdempotencyKey), func(ctx context.Context, projectID, continuationID string) (any, error) {
					return deps.BlackboardV2.RetainEvidenceForContinuation(ctx, projectID, continuationID, args)
				})
			})
		case "blackboard_checkpoint_attempt":
			server.AddTool(&sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: inputSchema,
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				var args blackboardv2.CheckpointAttemptRequest
				if decodeErr := decodeV2ToolArgs(harness, tool.InputSchema, req.Params.Arguments, &args); decodeErr != nil {
					return toolBlackboardV2ErrorResult(decodeErr)
				}
				return deps.callV2WithFingerprint(ctx, false, true, blackboardv2.SynchronizationDeliveryFingerprint("checkpoint", args.IdempotencyKey), func(ctx context.Context, projectID, continuationID string) (any, error) {
					return deps.BlackboardV2.CheckpointAttemptForContinuation(ctx, projectID, continuationID, args)
				})
			})
		case "blackboard_finish":
			server.AddTool(&sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: inputSchema,
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				var args blackboardv2.FinishContinuationRequest
				if decodeErr := decodeV2ToolArgs(harness, tool.InputSchema, req.Params.Arguments, &args); decodeErr != nil {
					return toolBlackboardV2ErrorResult(decodeErr)
				}
				// Initial live Finish may carry pending synchronization; exact replay
				// redelivers via the finish idempotency fingerprint.
				return deps.callV2WithFingerprint(ctx, false, true, blackboardv2.SynchronizationDeliveryFingerprint("finish", args.IdempotencyKey), func(ctx context.Context, projectID, continuationID string) (any, error) {
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

// decodeV2ToolArgs validates raw MCP arguments against the frozen contract
// schema, then decodes into the closed service DTO. Failures always surface as
// invalid_schema so transport validation never leaks SDK prose.
func decodeV2ToolArgs(harness *blackboardv2contract.Harness, schemaName string, raw json.RawMessage, target any) *blackboardv2.Error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := harness.Validate(schemaName, raw); err != nil {
		return &blackboardv2.Error{
			Code:      "invalid_schema",
			Message:   "tool arguments do not match the closed input schema",
			Path:      "arguments",
			Retryable: false,
		}
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return &blackboardv2.Error{
			Code:      "invalid_schema",
			Message:   "tool arguments do not match the closed input schema",
			Path:      "arguments",
			Retryable: false,
		}
	}
	return nil
}

func (deps V2Deps) callV2(ctx context.Context, requireLive, attachSync bool, action func(context.Context, string, string) (any, error)) (*sdkmcp.CallToolResult, error) {
	return deps.callV2WithFingerprint(ctx, requireLive, attachSync, "", action)
}

func (deps V2Deps) callV2WithFingerprint(ctx context.Context, requireLive, attachSync bool, requestFingerprint string, action func(context.Context, string, string) (any, error)) (*sdkmcp.CallToolResult, error) {
	result, _, err := deps.serveV2(ctx, requireLive, attachSync, requestFingerprint, action)
	return result, err
}

func toolBlackboardV2ErrorResult(err *blackboardv2.Error) (*sdkmcp.CallToolResult, error) {
	result, _, callErr := toolBlackboardV2Error(err, nil)
	return result, callErr
}

func (deps V2Deps) serveV2(ctx context.Context, requireLive, attachSync bool, requestFingerprint string, action func(context.Context, string, string) (any, error)) (*sdkmcp.CallToolResult, any, error) {
	grant, authErr := deps.requireGrant()
	if authErr != nil {
		return toolBlackboardV2Error(authErr, nil)
	}
	if !grant.Status().IsReadable() {
		return toolBlackboardV2Error(blackboardV2AuthError("authority_denied", "Continuation Interface capability is revoked", "authorization"), nil)
	}
	// requireLive gates offline read/current knowledge authority. Mutating tools
	// that support exact replay pass requireLive=false so stored non-mutating
	// replays reach the service after Finish/supersession; the service still
	// rejects changed retries and new writes.
	authority, err := deps.BlackboardV2.AuthorizeContinuationBinding(ctx, grant.ProjectID, grant.TaskID, grant.ContinuationID, requireLive)
	if err != nil {
		return toolBlackboardV2Error(asBlackboardV2Error(err), nil)
	}
	// Reserve the pending notice before the action when a stable fingerprint exists.
	if attachSync && strings.TrimSpace(requestFingerprint) != "" && authority.Sync.Pending {
		if _, claimErr := deps.BlackboardV2.ClaimTrustedSynchronization(ctx, grant.ProjectID, grant.TaskID, grant.ContinuationID, requestFingerprint, authority.Sync); claimErr != nil {
			return toolBlackboardV2Error(asBlackboardV2Error(claimErr), nil)
		}
	}
	result, err := action(ctx, grant.ProjectID, grant.ContinuationID)
	if err != nil {
		var sync *blackboardv2.SynchronizationAttachment
		if attachSync {
			if attachment, syncErr := deps.BlackboardV2.CaptureTrustedSynchronization(ctx, grant.ProjectID, grant.TaskID, grant.ContinuationID, authority.Sync, authority.Live, requestFingerprint); syncErr == nil {
				sync = attachment
			}
		}
		return toolBlackboardV2Error(asBlackboardV2Error(err), sync)
	}
	if attachSync {
		attachment, syncErr := deps.BlackboardV2.CaptureTrustedSynchronization(ctx, grant.ProjectID, grant.TaskID, grant.ContinuationID, authority.Sync, authority.Live, requestFingerprint)
		if syncErr != nil {
			return toolBlackboardV2Error(asBlackboardV2Error(syncErr), nil)
		}
		return toolBlackboardV2JSON(result, attachment)
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
