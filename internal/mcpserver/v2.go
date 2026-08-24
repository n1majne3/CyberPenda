package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"pentest/internal/blackboardconclusion"
	"pentest/internal/blackboardv2"
	"pentest/internal/blackboardv2contract"
	"pentest/internal/blackboardv2input"
	"pentest/internal/projectinterface"
)

// Deps are the domain services and trusted session for Blackboard v2 MCP tools.
// Project, Task, and Continuation identity come only from the Continuation
// Interface Grant resolved into Grant; model-facing arguments never carry them.
type Deps struct {
	BlackboardV2 *blackboardv2.Service
	// Grant is the Continuation Interface capability bound to this MCP session.
	Grant *projectinterface.Grant
	// GrantError, when set, is the structured failure from resolving a
	// presented-but-invalid capability token.
	GrantError *blackboardv2.Error
	// FinishIntentPolicy resolves whether a blackboard_finish call must record a
	// deferred Blackboard Finish Intent (assisted mode) instead of closing the
	// Continuation immediately. It returns the source Work Turn provenance the
	// daemon carries from its own observation state, never from caller input. A
	// nil callback means interactive immediate-close behavior (ADR 0022).
	FinishIntentPolicy FinishIntentPolicy
}

// FinishDecision describes how blackboard_finish should treat one call.
type FinishDecision struct {
	// RecordIntent is true when the owner runs in assisted mode and the finish
	// must defer the close until the Work Runtime Turn settles.
	RecordIntent bool
	// Provenance is the daemon-owned source Work Turn correlation captured with
	// the intent. It is ignored when RecordIntent is false.
	Provenance blackboardv2.FinishIntentProvenance
}

// FinishIntentPolicy resolves a blackboard_finish decision for one owner and
// continuation. The daemon implementation reads Run Controls / Session mode and
// the active Work Turn observation state; it never trusts caller-supplied input.
type FinishIntentPolicy func(sessionOwner bool, ownerID, continuationID string) (FinishDecision, error)

// New builds an MCP server that registers exactly the seven Blackboard v2
// trusted tools. Input schemas are closed objects generated from the frozen
// v2 contract definitions.
func New(deps Deps) *sdkmcp.Server {
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

func registerBlackboardV2Tools(server *sdkmcp.Server, deps Deps) {
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
		if deps.ownerIsSession() {
			schema = blackboardv2input.SessionToolInputSchema(schema)
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
				raw := req.Params.Arguments
				idempotencyKey := v2InputIdempotencyKey(raw)
				// Exact replay remains available after Finish/supersession; response-loss
				// retries redeliver the same sync attachment via idempotency fingerprint.
				return deps.callV2WithFingerprint(ctx, false, true, blackboardv2.SynchronizationDeliveryFingerprint("change", idempotencyKey), func(ctx context.Context, projectID, continuationID string) (any, error) {
					var args blackboardv2.ChangeBatch
					if decodeErr := blackboardv2input.DecodeContractInput(tool.InputSchema, deps.ownerIsSession(), raw, &args); decodeErr != nil {
						return nil, decodeErr
					}
					if deps.ownerIsSession() {
						return deps.BlackboardV2.ApplyForSessionContinuation(ctx, projectID, continuationID, args)
					}
					return deps.BlackboardV2.ApplyForContinuation(ctx, projectID, continuationID, args)
				})
			})
		case "blackboard_record_attempt_result":
			server.AddTool(&sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: inputSchema,
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				rawArguments := req.Params.Arguments
				fingerprint := blackboardv2.SynchronizationDeliveryFingerprint("attempt-result", v2InputIdempotencyKey(rawArguments))
				return deps.callV2WithFingerprint(ctx, false, true, fingerprint, func(ctx context.Context, ownerID, continuationID string) (any, error) {
					var args blackboardAttemptResultArgs
					if decodeErr := blackboardv2input.DecodeContractInput(tool.InputSchema, deps.ownerIsSession(), rawArguments, &args); decodeErr != nil {
						return nil, decodeErr
					}
					rawResult, marshalErr := json.Marshal(args.Result)
					if marshalErr != nil {
						return nil, marshalErr
					}
					validated, validationErr := blackboardconclusion.Decode(rawResult)
					if validationErr != nil {
						detail := blackboardconclusion.DecodeDetailOf(validationErr)
						return nil, &blackboardv2.Error{
							Code: "invalid_schema", Message: "Attempt result violates the closed semantic contract",
							Path: detail.FieldPath, Retryable: false,
							Details: map[string]any{"reason": detail.Reason, "expected": detail.Expected},
						}
					}
					batch, compileErr := blackboardconclusion.Compile(validated.Result, args.IdempotencyKey)
					if compileErr != nil {
						return nil, &blackboardv2.Error{Code: "invalid_schema", Message: "Attempt result cannot be compiled", Retryable: false}
					}
					if aliasErr := deps.rejectAttemptResultKeyAliases(ctx, ownerID, validated.Result); aliasErr != nil {
						return nil, aliasErr
					}
					if deps.ownerIsSession() {
						return deps.BlackboardV2.ApplyForSessionContinuationAtRevision(ctx, ownerID, continuationID, validated.Result.BaseRevision, batch)
					}
					return deps.BlackboardV2.ApplyForContinuationAtRevision(ctx, ownerID, continuationID, validated.Result.BaseRevision, batch)
				})
			})
		case "blackboard_read":
			server.AddTool(&sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: inputSchema,
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				rawArguments := req.Params.Arguments
				// Live read/current knowledge authority only; closed Continuations
				// keep exact write/finish replay but not current knowledge reads.
				// Reads are Pending-only (no durable request fingerprint).
				return deps.callV2WithFingerprint(ctx, true, true, "", func(ctx context.Context, projectID, _ string) (any, error) {
					var args blackboardV2ReadArgs
					if decodeErr := blackboardv2input.DecodeContractInput(tool.InputSchema, deps.ownerIsSession(), rawArguments, &args); decodeErr != nil {
						return nil, decodeErr
					}
					if deps.ownerIsSession() {
						return deps.BlackboardV2.ReadSessionCurrent(ctx, projectID, args.Key)
					}
					return deps.BlackboardV2.ReadCurrent(ctx, projectID, args.Key)
				})
			})
		case "blackboard_history":
			server.AddTool(&sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: inputSchema,
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				rawArguments := req.Params.Arguments
				return deps.callV2WithFingerprint(ctx, true, true, "", func(ctx context.Context, projectID, _ string) (any, error) {
					var args blackboardV2HistoryArgs
					if decodeErr := blackboardv2input.DecodeContractInput(tool.InputSchema, deps.ownerIsSession(), rawArguments, &args); decodeErr != nil {
						return nil, decodeErr
					}
					if deps.ownerIsSession() {
						return deps.BlackboardV2.ReadSessionHistory(ctx, projectID, args.Key, blackboardv2.HistoryOptions{Cursor: args.Cursor, Limit: args.Limit})
					}
					return deps.BlackboardV2.ReadHistory(ctx, projectID, args.Key, blackboardv2.HistoryOptions{
						Cursor: args.Cursor, Limit: args.Limit,
					})
				})
			})
		case "blackboard_retain_evidence":
			server.AddTool(&sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: inputSchema,
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				rawArguments := req.Params.Arguments
				fingerprint := blackboardv2.SynchronizationDeliveryFingerprint("evidence", v2InputIdempotencyKey(rawArguments))
				return deps.callV2WithFingerprint(ctx, false, true, fingerprint, func(ctx context.Context, projectID, continuationID string) (any, error) {
					var args blackboardv2.RetainEvidenceRequest
					if decodeErr := blackboardv2input.DecodeContractInput(tool.InputSchema, deps.ownerIsSession(), rawArguments, &args); decodeErr != nil {
						return nil, decodeErr
					}
					if deps.ownerIsSession() {
						return nil, blackboardV2AuthError("authority_denied", "Evidence retention is Project-only", "authorization")
					}
					return deps.BlackboardV2.RetainEvidenceForContinuation(ctx, projectID, continuationID, args)
				})
			})
		case "blackboard_checkpoint_attempt":
			server.AddTool(&sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: inputSchema,
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				rawArguments := req.Params.Arguments
				fingerprint := blackboardv2.SynchronizationDeliveryFingerprint("checkpoint", v2InputIdempotencyKey(rawArguments))
				return deps.callV2WithFingerprint(ctx, false, true, fingerprint, func(ctx context.Context, projectID, continuationID string) (any, error) {
					var args blackboardv2.CheckpointAttemptRequest
					if decodeErr := blackboardv2input.DecodeContractInput(tool.InputSchema, deps.ownerIsSession(), rawArguments, &args); decodeErr != nil {
						return nil, decodeErr
					}
					if deps.ownerIsSession() {
						return deps.BlackboardV2.CheckpointSessionAttemptForContinuation(ctx, projectID, continuationID, args)
					}
					return deps.BlackboardV2.CheckpointAttemptForContinuation(ctx, projectID, continuationID, args)
				})
			})
		case "blackboard_finish":
			server.AddTool(&sdkmcp.Tool{
				Name: tool.Name, Description: tool.Description, InputSchema: inputSchema,
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				rawArguments := req.Params.Arguments
				// Initial live Finish may carry pending synchronization; exact replay
				// redelivers via the finish idempotency fingerprint.
				fingerprint := blackboardv2.SynchronizationDeliveryFingerprint("finish", v2InputIdempotencyKey(rawArguments))
				return deps.callV2WithFingerprint(ctx, false, true, fingerprint, func(ctx context.Context, projectID, continuationID string) (any, error) {
					var args blackboardv2.FinishContinuationRequest
					if decodeErr := blackboardv2input.DecodeContractInput(tool.InputSchema, deps.ownerIsSession(), rawArguments, &args); decodeErr != nil {
						return nil, decodeErr
					}
					decision, decideErr := deps.resolveFinishDecision(continuationID)
					if decideErr != nil {
						return nil, decideErr
					}
					if decision.RecordIntent {
						// ADR 0022: assisted mode records a Blackboard Finish Intent and
						// defers the close until the Work Runtime Turn settles. The tool
						// reports intent_recorded, not finished.
						if deps.ownerIsSession() {
							return deps.BlackboardV2.RecordSessionFinishIntent(ctx, projectID, continuationID, args.IdempotencyKey, decision.Provenance)
						}
						return deps.BlackboardV2.RecordFinishIntent(ctx, projectID, continuationID, args, decision.Provenance)
					}
					if deps.ownerIsSession() {
						return deps.BlackboardV2.FinishSessionContinuation(ctx, projectID, continuationID, args.IdempotencyKey)
					}
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

type blackboardAttemptResultArgs struct {
	IdempotencyKey string                                    `json:"idempotency_key"`
	Result         blackboardconclusion.RuntimeAttemptResult `json:"result"`
}

func (deps Deps) rejectAttemptResultKeyAliases(ctx context.Context, ownerID string, result blackboardconclusion.RuntimeAttemptResult) error {
	createdKeys := make([]string, 0, 1+len(result.TestedTargets))
	if result.Attempt.Create {
		createdKeys = append(createdKeys, result.Attempt.Key)
	}
	for _, target := range result.TestedTargets {
		if target.CreateObjective != nil {
			createdKeys = append(createdKeys, target.Key)
		}
	}
	for _, key := range createdKeys {
		alias := attemptResultKeySeparatorAlias(key)
		if alias == "" {
			continue
		}
		var exists bool
		var err error
		if deps.ownerIsSession() {
			exists, err = deps.BlackboardV2.HasSessionSemanticKey(ctx, ownerID, alias)
		} else {
			exists, err = deps.BlackboardV2.HasSemanticKey(ctx, ownerID, alias)
		}
		if err != nil {
			return err
		}
		if exists {
			return &blackboardv2.Error{
				Code: "key_conflict", Message: "a punctuation alias of the proposed Blackboard Key already exists",
				Path: "result", Retryable: false, Details: map[string]any{"key": key, "existing_key": alias},
			}
		}
	}
	return nil
}

func attemptResultKeySeparatorAlias(key string) string {
	for _, prefix := range []string{"attempt", "objective"} {
		if strings.HasPrefix(key, prefix+":") {
			return prefix + "/" + strings.TrimPrefix(key, prefix+":")
		}
		if strings.HasPrefix(key, prefix+"/") {
			return prefix + ":" + strings.TrimPrefix(key, prefix+"/")
		}
	}
	return ""
}

type blackboardV2HistoryArgs struct {
	Key    string `json:"key"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

func v2InputIdempotencyKey(raw json.RawMessage) string {
	var envelope struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	_ = json.Unmarshal(raw, &envelope)
	return envelope.IdempotencyKey
}

func (deps Deps) callV2WithFingerprint(ctx context.Context, requireLive, attachSync bool, requestFingerprint string, action func(context.Context, string, string) (any, error)) (*sdkmcp.CallToolResult, error) {
	result, _, err := deps.serveV2(ctx, requireLive, attachSync, requestFingerprint, action)
	return result, err
}

func (deps Deps) serveV2(ctx context.Context, requireLive, attachSync bool, requestFingerprint string, action func(context.Context, string, string) (any, error)) (*sdkmcp.CallToolResult, any, error) {
	grant, authErr := deps.requireGrant()
	if authErr != nil {
		return toolBlackboardV2Error(authErr, nil)
	}
	if !grant.Status().IsReadable() {
		return toolBlackboardV2Error(blackboardV2AuthError("authority_denied", "Continuation Interface capability is revoked", "authorization"), nil)
	}
	if grant.IsSession() {
		if requireLive {
			if _, err := deps.BlackboardV2.AuthorizeSessionContinuation(ctx, grant.Owner.SessionID, grant.ContinuationID); err != nil {
				return toolBlackboardV2Error(asBlackboardV2Error(err), nil)
			}
		}
		result, err := action(ctx, grant.Owner.SessionID, grant.ContinuationID)
		if err != nil {
			return toolBlackboardV2Error(asBlackboardV2Error(err), nil)
		}
		return toolBlackboardV2JSON(result, nil)
	}
	// requireLive gates offline read/current knowledge authority. Mutating tools
	// that support exact replay pass requireLive=false so stored non-mutating
	// replays reach the service after Finish/supersession; the service still
	// rejects changed retries and new writes.
	authority, err := deps.BlackboardV2.AuthorizeContinuationBinding(ctx, grant.Owner.ProjectID, grant.Owner.TaskID, grant.ContinuationID, requireLive)
	if err != nil {
		return toolBlackboardV2Error(asBlackboardV2Error(err), nil)
	}
	// Reserve the pending notice before the action when a stable fingerprint exists.
	if attachSync && strings.TrimSpace(requestFingerprint) != "" && authority.Sync.Pending {
		if _, claimErr := deps.BlackboardV2.ClaimTrustedSynchronization(ctx, grant.Owner.ProjectID, grant.Owner.TaskID, grant.ContinuationID, requestFingerprint, authority.Sync); claimErr != nil {
			return toolBlackboardV2Error(asBlackboardV2Error(claimErr), nil)
		}
	}
	result, err := action(ctx, grant.Owner.ProjectID, grant.ContinuationID)
	if err != nil {
		var sync *blackboardv2.SynchronizationAttachment
		if attachSync {
			if attachment, syncErr := deps.BlackboardV2.CaptureTrustedSynchronization(ctx, grant.Owner.ProjectID, grant.Owner.TaskID, grant.ContinuationID, authority.Sync, authority.Live, requestFingerprint); syncErr == nil {
				sync = attachment
			}
		}
		return toolBlackboardV2Error(asBlackboardV2Error(err), sync)
	}
	if attachSync {
		attachment, syncErr := deps.BlackboardV2.CaptureTrustedSynchronization(ctx, grant.Owner.ProjectID, grant.Owner.TaskID, grant.ContinuationID, authority.Sync, authority.Live, requestFingerprint)
		if syncErr != nil {
			return toolBlackboardV2Error(asBlackboardV2Error(syncErr), nil)
		}
		return toolBlackboardV2JSON(result, attachment)
	}
	return toolBlackboardV2JSON(result, nil)
}

func (deps Deps) ownerIsSession() bool { return deps.Grant != nil && deps.Grant.IsSession() }

// resolveFinishDecision asks the daemon policy whether this finish call must
// record a deferred Blackboard Finish Intent. Without a policy the behavior is
// the legacy interactive immediate-close.
func (deps Deps) resolveFinishDecision(continuationID string) (FinishDecision, error) {
	if deps.FinishIntentPolicy == nil {
		return FinishDecision{}, nil
	}
	sessionOwner := false
	ownerID := ""
	if deps.Grant != nil {
		sessionOwner = deps.Grant.IsSession()
		if sessionOwner {
			ownerID = deps.Grant.Owner.SessionID
		} else {
			ownerID = deps.Grant.Owner.TaskID
		}
	}
	return deps.FinishIntentPolicy(sessionOwner, ownerID, continuationID)
}

func (deps Deps) requireGrant() (projectinterface.Grant, *blackboardv2.Error) {
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

func toolJSON(payload any) (*sdkmcp.CallToolResult, any, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(data)}},
	}, payload, nil
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
