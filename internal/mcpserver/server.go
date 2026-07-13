// Package mcpserver implements the built-in trusted MCP server that exposes
// project interfaces to runtimes. Business behavior lives in domain services;
// this package is a thin transport that maps MCP tools onto those services.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"pentest/internal/blackboard"
	"pentest/internal/blackboardcompat"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/report"
	"pentest/internal/task"
)

// Deps are the domain services MCP tools call into.
type Deps struct {
	Projects *project.Service
	Facts    *blackboard.Service
	Tasks    *task.Service
	// Reads, when non-nil (graph store active), makes the compatibility read
	// tools delegate to BlackboardReadService instead of the legacy tables.
	Reads *blackboard.BlackboardReadService
	// Compatibility translates legacy_blackboard_v1 write tools after graph
	// cutover. Nil preserves the legacy_v1 direct-service path.
	Compatibility *blackboardcompat.Service
	// ProjectInterface, when non-nil (graph store active), exposes the
	// graph-native project-interface MCP tools authenticated with a
	// Continuation Interface Grant (runtime protocol §12.2).
	ProjectInterface *projectinterface.Service
	// Principal is the trusted Runtime caller resolved from the request's grant
	// token. It is nil for operator/legacy requests.
	Principal *projectinterface.Principal
	// PrincipalError, when set, is the structured failure from resolving a
	// presented-but-invalid grant token. It lets the project-interface tools
	// report grant_not_found rather than conflating an invalid token with "no
	// grant presented" (continuation_context_required).
	PrincipalError *projectinterface.Error
}

// legacyReadResult runs a legacy compatibility read against BlackboardReadService
// and returns the legacy-shaped result. Callers must guard with deps.Reads != nil.
func (deps Deps) legacyReadResult(ctx context.Context, readRequest blackboard.ReadRequest) (any, error) {
	if deps.Compatibility != nil {
		if kind := blackboardcompat.ReadCallKind(readRequest.Kind); kind != "" {
			if err := deps.Compatibility.RecordUse(ctx, blackboardcompat.Use{ProjectID: readRequest.ProjectID, Transport: blackboardcompat.TransportMCP, Kind: kind, Mode: blackboardcompat.UseModeRead}); err != nil {
				return nil, err
			}
		}
	}
	readRequest.ProtocolVersion = blackboard.BlackboardReadProtocolVersion
	envelope, err := deps.Reads.Read(ctx, readRequest)
	if err != nil {
		return nil, err
	}
	return envelope.Result, nil
}

// New builds a configured MCP server with the MVP trusted project-interface tools.
func New(deps Deps) *sdkmcp.Server {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "pentest-agent",
		Version: "0.1.0",
	}, nil)

	sdkmcp.AddTool(server, deps.legacyTool("upsert_project_fact", "Upsert a project fact by fact key. Conflicting writes update the existing fact and preserve history as fact versions.", "blackboard_apply"), func(ctx context.Context, req *sdkmcp.CallToolRequest, args upsertProjectFactArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		if deps.Compatibility != nil {
			principal, err := deps.compatibilityPrincipal(args.ProjectID)
			if err != nil {
				return toolProjectInterfaceError(compatibilityError(err))
			}
			result, err := deps.Compatibility.Call(ctx, blackboardcompat.LegacyCall{
				Kind: blackboardcompat.CallUpsertFact, Transport: blackboardcompat.TransportMCP,
				ProjectID: args.ProjectID, Principal: principal, IdempotencyKey: args.IdempotencyKey,
				ExpectedVersion: args.ExpectedVersion,
				Fact: &blackboardcompat.FactWrite{
					FactKey: args.FactKey, Category: args.Category, Summary: args.Summary,
					Body: args.Body, Confidence: args.Confidence, ScopeStatus: args.ScopeStatus,
				},
			})
			if err != nil {
				return toolProjectInterfaceError(projectinterface.AsError(err))
			}
			return toolJSON(result.Payload)
		}
		_ = ctx
		if _, err := deps.Projects.Get(args.ProjectID); err != nil {
			return toolError(err)
		}
		fact, err := deps.Facts.UpsertFact(blackboard.UpsertFactRequest{
			ProjectID:   args.ProjectID,
			FactKey:     args.FactKey,
			Category:    args.Category,
			Summary:     args.Summary,
			Body:        args.Body,
			Confidence:  blackboard.Confidence(args.Confidence),
			ScopeStatus: blackboard.ScopeStatus(args.ScopeStatus),
		})
		if err != nil {
			return toolError(err)
		}
		return toolJSON(fact)
	})

	sdkmcp.AddTool(server, deps.legacyTool("get_project_fact", "Fetch the full body of a project fact by fact key.", "blackboard_records_resolve"), func(ctx context.Context, req *sdkmcp.CallToolRequest, args projectFactKeyArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		if deps.Reads != nil {
			result, err := deps.legacyReadResult(ctx, blackboard.ReadRequest{ProjectID: args.ProjectID, Kind: blackboard.ReadKindLegacyFactDetailV1, LegacyFactDetail: &blackboard.LegacyFactDetailRequest{FactKey: args.FactKey}})
			if err != nil {
				return toolError(err)
			}
			return toolJSON(result)
		}
		fact, err := deps.Facts.GetFact(args.ProjectID, args.FactKey)
		if err != nil {
			return toolError(err)
		}
		return toolJSON(fact)
	})

	sdkmcp.AddTool(server, deps.legacyTool("list_project_facts", "List the compact fact index for current truth.", "blackboard_records_list"), func(ctx context.Context, req *sdkmcp.CallToolRequest, args projectOnlyArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		if deps.Reads != nil {
			result, err := deps.legacyReadResult(ctx, blackboard.ReadRequest{ProjectID: args.ProjectID, Kind: blackboard.ReadKindLegacyFactIndexV1, LegacyFactIndex: &blackboard.LegacyFactIndexRequest{}})
			if err != nil {
				return toolError(err)
			}
			return toolJSON(result)
		}
		index, err := deps.Facts.FactIndex(args.ProjectID, blackboard.FactIndexOptions{})
		if err != nil {
			return toolError(err)
		}
		return toolJSON(index)
	})

	sdkmcp.AddTool(server, deps.legacyTool("search_project_facts", "Search project facts by key, summary, or body.", "blackboard_records_list"), func(ctx context.Context, req *sdkmcp.CallToolRequest, args searchFactsArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		if deps.Reads != nil {
			include := args.IncludeDeprecated
			result, err := deps.legacyReadResult(ctx, blackboard.ReadRequest{ProjectID: args.ProjectID, Kind: blackboard.ReadKindLegacyFactIndexV1, LegacyFactIndex: &blackboard.LegacyFactIndexRequest{IncludeDeprecated: &include, Query: args.Query}})
			if err != nil {
				return toolError(err)
			}
			return toolJSON(result)
		}
		matches, err := deps.Facts.SearchFacts(args.ProjectID, args.Query, args.IncludeDeprecated)
		if err != nil {
			return toolError(err)
		}
		return toolJSON(matches)
	})

	sdkmcp.AddTool(server, deps.legacyTool("deprecate_project_fact", "Mark a project fact as deprecated while preserving its body and history.", "blackboard_apply"), func(ctx context.Context, req *sdkmcp.CallToolRequest, args projectFactKeyArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		if deps.Compatibility != nil {
			principal, err := deps.compatibilityPrincipal(args.ProjectID)
			if err != nil {
				return toolProjectInterfaceError(projectinterface.AsError(err))
			}
			result, err := deps.Compatibility.Call(ctx, blackboardcompat.LegacyCall{
				Kind: blackboardcompat.CallDeprecateFact, Transport: blackboardcompat.TransportMCP,
				ProjectID: args.ProjectID, Principal: principal, IdempotencyKey: args.IdempotencyKey, ExpectedVersion: args.ExpectedVersion,
				Fact: &blackboardcompat.FactWrite{FactKey: args.FactKey, Confidence: "deprecated"},
			})
			if err != nil {
				return toolProjectInterfaceError(projectinterface.AsError(err))
			}
			return toolJSON(result.Payload)
		}
		_ = ctx
		fact, err := deps.Facts.DeprecateFact(args.ProjectID, args.FactKey)
		if err != nil {
			return toolError(err)
		}
		return toolJSON(fact)
	})

	sdkmcp.AddTool(server, deps.legacyTool("upsert_fact_relation", "Create or update a typed relation between two project facts.", "blackboard_apply"), func(ctx context.Context, req *sdkmcp.CallToolRequest, args upsertRelationArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		if deps.Compatibility != nil {
			principal, err := deps.compatibilityPrincipal(args.ProjectID)
			if err != nil {
				return toolProjectInterfaceError(projectinterface.AsError(err))
			}
			result, err := deps.Compatibility.Call(ctx, blackboardcompat.LegacyCall{
				Kind: blackboardcompat.CallPutFactRelation, Transport: blackboardcompat.TransportMCP,
				ProjectID: args.ProjectID, Principal: principal, IdempotencyKey: args.IdempotencyKey, ExpectedVersion: args.ExpectedVersion,
				Relation: &blackboardcompat.FactRelationWrite{SourceFactKey: args.SourceFactKey, TargetFactKey: args.TargetFactKey, Relation: args.Relation, Summary: args.Summary},
			})
			if err != nil {
				return toolProjectInterfaceError(projectinterface.AsError(err))
			}
			return toolJSON(result.Payload)
		}
		_ = ctx
		relation, err := deps.Facts.UpsertFactRelation(blackboard.UpsertFactRelationRequest{
			ProjectID:     args.ProjectID,
			SourceFactKey: args.SourceFactKey,
			TargetFactKey: args.TargetFactKey,
			Relation:      args.Relation,
			Summary:       args.Summary,
		})
		if err != nil {
			return toolError(err)
		}
		return toolJSON(relation)
	})

	sdkmcp.AddTool(server, deps.legacyTool("record_vulnerability", "Record or update a finding by finding key. This is the reportable issue interface.", "blackboard_apply"), func(ctx context.Context, req *sdkmcp.CallToolRequest, args recordFindingArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		if deps.Compatibility != nil {
			principal, err := deps.compatibilityPrincipal(args.ProjectID)
			if err != nil {
				return toolProjectInterfaceError(projectinterface.AsError(err))
			}
			result, err := deps.Compatibility.Call(ctx, blackboardcompat.LegacyCall{
				Kind: blackboardcompat.CallUpsertFinding, Transport: blackboardcompat.TransportMCP,
				ProjectID: args.ProjectID, Principal: principal, IdempotencyKey: args.IdempotencyKey, ExpectedVersion: args.ExpectedVersion,
				Finding: &blackboardcompat.FindingWrite{FindingKey: args.FindingKey, Title: args.Title, Description: args.Description, Status: args.Status, Target: args.Target, Proof: args.Proof, Impact: args.Impact, Recommendation: args.Recommendation, CVSSVersion: args.CVSSVersion, CVSSVector: args.CVSSVector},
			})
			if err != nil {
				return toolProjectInterfaceError(projectinterface.AsError(err))
			}
			return toolJSON(result.Payload)
		}
		_ = ctx
		if _, err := deps.Projects.Get(args.ProjectID); err != nil {
			return toolError(err)
		}
		finding, err := deps.Facts.UpsertFinding(blackboard.UpsertFindingRequest{
			ProjectID:      args.ProjectID,
			FindingKey:     args.FindingKey,
			Title:          args.Title,
			Description:    args.Description,
			Status:         blackboard.FindingStatus(args.Status),
			Target:         args.Target,
			Proof:          args.Proof,
			Impact:         args.Impact,
			Recommendation: args.Recommendation,
			CVSSVersion:    args.CVSSVersion,
			CVSSVector:     args.CVSSVector,
		})
		if err != nil {
			return toolError(err)
		}
		return toolJSON(finding)
	})

	sdkmcp.AddTool(server, deps.legacyTool("list_vulnerabilities", "List all findings for a project.", "blackboard_records_list"), func(ctx context.Context, req *sdkmcp.CallToolRequest, args projectOnlyArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		if deps.Reads != nil {
			result, err := deps.legacyReadResult(ctx, blackboard.ReadRequest{ProjectID: args.ProjectID, Kind: blackboard.ReadKindLegacyFindingCollectionV1, LegacyFindingCollection: &blackboard.LegacyFindingCollectionRequest{}})
			if err != nil {
				return toolError(err)
			}
			return toolJSON(result)
		}
		findings, err := deps.Facts.ListFindings(args.ProjectID)
		if err != nil {
			return toolError(err)
		}
		return toolJSON(findings)
	})

	sdkmcp.AddTool(server, deps.legacyTool("attach_evidence", "Attach or retain an evidence artifact under a managed artifact root.", "blackboard_retain_evidence"), func(ctx context.Context, req *sdkmcp.CallToolRequest, args attachEvidenceArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		if deps.Compatibility != nil {
			principal, err := deps.compatibilityPrincipal(args.ProjectID)
			if err != nil {
				return toolProjectInterfaceError(projectinterface.AsError(err))
			}
			result, err := deps.Compatibility.Call(ctx, blackboardcompat.LegacyCall{
				Kind: blackboardcompat.CallAttachEvidence, Transport: blackboardcompat.TransportMCP,
				ProjectID: args.ProjectID, Principal: principal, IdempotencyKey: args.IdempotencyKey, ExpectedVersion: args.ExpectedVersion,
				Evidence: &blackboardcompat.EvidenceWrite{EvidenceKey: args.EvidenceKey, AttachToType: args.AttachToType, AttachToKey: args.AttachToKey, ArtifactType: args.ArtifactType, SourcePath: args.SourcePath, Summary: args.Summary, ProducedByAttempt: args.ProducedByAttempt},
			})
			if err != nil {
				return toolProjectInterfaceError(projectinterface.AsError(err))
			}
			return toolJSON(result.Payload)
		}
		_ = ctx
		artifact, err := deps.Facts.AttachEvidence(blackboard.AttachEvidenceRequest{
			ProjectID:    args.ProjectID,
			EvidenceKey:  args.EvidenceKey,
			AttachToType: blackboard.EvidenceAttachType(args.AttachToType),
			AttachToKey:  args.AttachToKey,
			ArtifactType: args.ArtifactType,
			SourcePath:   args.SourcePath,
			SHA256:       args.SHA256,
			Summary:      args.Summary,
		})
		if err != nil {
			return toolError(err)
		}
		return toolJSON(artifact)
	})

	sdkmcp.AddTool(server, deps.legacyTool("generate_report", "Generate a Markdown report from stored project state.", "PentestReportV1"), func(ctx context.Context, req *sdkmcp.CallToolRequest, args generateReportArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		if deps.Compatibility != nil {
			principal, err := deps.compatibilityPrincipal(args.ProjectID)
			if err != nil {
				return toolProjectInterfaceError(projectinterface.AsError(err))
			}
			result, err := deps.Compatibility.Call(ctx, blackboardcompat.LegacyCall{
				Kind: blackboardcompat.CallGenerateReport, Transport: blackboardcompat.TransportMCP,
				ProjectID: args.ProjectID, Principal: principal, IdempotencyKey: args.IdempotencyKey,
				Report: &blackboardcompat.ReportWrite{TaskID: args.TaskID},
			})
			if err != nil {
				return toolProjectInterfaceError(projectinterface.AsError(err))
			}
			return toolJSON(result.Payload)
		}
		taskID := args.TaskID
		if taskID == "" && deps.Tasks != nil {
			tasks, err := deps.Tasks.ListForProject(args.ProjectID)
			if err != nil {
				return toolError(err)
			}
			if len(tasks) > 0 {
				taskID = tasks[len(tasks)-1].ID
			}
		}
		if taskID == "" {
			return toolError(fmt.Errorf("task_id is required when the project has no tasks"))
		}
		generator := report.NewGenerator(deps.Facts, deps.Tasks)
		out, err := generator.Generate(report.Request{ProjectID: args.ProjectID, TaskID: taskID})
		if err != nil {
			return toolError(err)
		}
		return toolJSON(out)
	})

	sdkmcp.AddTool(server, deps.legacyTool("submit_task_summary", "Submit a task summary before ending a continuation so the next resume carries compact handoff context.", "blackboard_finish_continuation"), func(ctx context.Context, req *sdkmcp.CallToolRequest, args submitTaskSummaryArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		if deps.Compatibility != nil {
			principal, err := deps.compatibilityPrincipal(args.ProjectID)
			if err != nil {
				return toolProjectInterfaceError(projectinterface.AsError(err))
			}
			result, err := deps.Compatibility.Call(ctx, blackboardcompat.LegacyCall{
				Kind: blackboardcompat.CallPutTaskSummary, Transport: blackboardcompat.TransportMCP,
				ProjectID: args.ProjectID, Principal: principal, IdempotencyKey: args.IdempotencyKey,
				TaskSummary: &blackboardcompat.TaskSummaryWrite{TaskID: args.TaskID, Summary: args.Summary, SubmittedBy: args.SubmittedBy},
			})
			if err != nil {
				return toolProjectInterfaceError(projectinterface.AsError(err))
			}
			return toolJSON(result.Payload)
		}
		_ = ctx
		if deps.Tasks == nil {
			return toolError(fmt.Errorf("task service unavailable"))
		}
		if _, err := deps.Projects.Get(args.ProjectID); err != nil {
			return toolError(err)
		}
		found, err := deps.Tasks.Get(args.TaskID)
		if err != nil {
			return toolError(err)
		}
		if found.ProjectID != args.ProjectID {
			return toolError(fmt.Errorf("task not found"))
		}
		submittedBy := strings.TrimSpace(args.SubmittedBy)
		if submittedBy == "" {
			submittedBy = "runtime"
		}
		version, err := deps.Tasks.PutSummary(args.TaskID, args.Summary, submittedBy)
		if err != nil {
			return toolError(err)
		}
		return toolJSON(version)
	})

	if deps.ProjectInterface != nil {
		registerProjectInterfaceTools(server, deps)
	}

	return server
}

// registerProjectInterfaceTools adds the graph-native Runtime project-interface
// MCP tools (runtime protocol §12.1, §12.2). They authenticate through the
// Continuation Interface Grant resolved into deps.Principal; operator/legacy
// requests get continuation_context_required.
func registerProjectInterfaceTools(server *sdkmcp.Server, deps Deps) {
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "blackboard_checkpoint_attempt",
		Description: projectinterface.TrustedToolDescription("blackboard_checkpoint_attempt"),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args blackboardCheckpointAttemptArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		principal, principalErr := deps.requirePrincipal()
		if principalErr != nil {
			return toolProjectInterfaceError(principalErr)
		}
		result, checkpointErr := deps.ProjectInterface.CheckpointAttempt(ctx, *principal, projectinterface.CheckpointAttemptRequest(args))
		if checkpointErr != nil {
			return toolProjectInterfaceError(projectinterface.AsError(checkpointErr))
		}
		return toolJSON(result)
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "blackboard_finish_continuation",
		Description: projectinterface.TrustedToolDescription("blackboard_finish_continuation"),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args blackboardFinishContinuationArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		principal, principalErr := deps.requirePrincipal()
		if principalErr != nil {
			return toolProjectInterfaceError(principalErr)
		}
		result, finishErr := deps.ProjectInterface.FinishContinuation(ctx, *principal, projectinterface.FinishContinuationRequest(args))
		if finishErr != nil {
			return toolProjectInterfaceError(projectinterface.AsError(finishErr))
		}
		return toolJSON(result)
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "blackboard_retain_evidence",
		Description: projectinterface.TrustedToolDescription("blackboard_retain_evidence"),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args blackboardRetainEvidenceArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		principal, principalErr := deps.requirePrincipal()
		if principalErr != nil {
			return toolProjectInterfaceError(principalErr)
		}
		result, retainErr := deps.ProjectInterface.RetainEvidence(ctx, *principal, projectinterface.RetainEvidenceRequest(args))
		if retainErr != nil {
			return toolProjectInterfaceError(projectinterface.AsError(retainErr))
		}
		return toolJSON(result)
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "blackboard_apply",
		Description: projectinterface.TrustedToolDescription("blackboard_apply"),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args blackboardApplyArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		principal, principalErr := deps.requirePrincipal()
		if principalErr != nil {
			return toolProjectInterfaceError(principalErr)
		}
		result, applyErr := deps.ProjectInterface.Apply(ctx, *principal, projectinterface.ApplyMutationRequest{
			ProtocolVersion:    args.ProtocolVersion,
			Batch:              args.Batch,
			SourceEventIDsByOp: args.SourceEventIDsByOp,
		})
		if applyErr != nil {
			return toolProjectInterfaceError(projectinterface.AsError(applyErr))
		}
		return toolJSON(result)
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "blackboard_resolve_records",
		Description: projectinterface.TrustedToolDescription("blackboard_resolve_records"),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args blackboardResolveRecordsArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		principal, principalErr := deps.requirePrincipal()
		if principalErr != nil {
			return toolProjectInterfaceError(principalErr)
		}
		result, resolveErr := deps.ProjectInterface.ResolveRecords(ctx, *principal, projectinterface.ResolveRecordsRequest{
			ProtocolVersion: args.ProtocolVersion,
			Nodes:           args.Nodes,
			EdgeIDs:         args.EdgeIDs,
		})
		if resolveErr != nil {
			return toolProjectInterfaceError(projectinterface.AsError(resolveErr))
		}
		return toolJSON(result)
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "blackboard_get_current_graph",
		Description: projectinterface.TrustedToolDescription("blackboard_get_current_graph"),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args blackboardCurrentGraphArgs) (*sdkmcp.CallToolResult, any, error) {
		_ = req
		principal, principalErr := deps.requirePrincipal()
		if principalErr != nil {
			return toolProjectInterfaceError(principalErr)
		}
		result, graphErr := deps.ProjectInterface.CurrentGraph(ctx, *principal, projectinterface.CurrentGraphRequest{
			ProtocolVersion: args.ProtocolVersion,
		})
		if graphErr != nil {
			return toolProjectInterfaceError(projectinterface.AsError(graphErr))
		}
		return toolJSON(result)
	})
}

// requirePrincipal returns the resolved Runtime principal, or a structured
// error: PrincipalError when a grant token was presented but rejected,
// otherwise continuation_context_required for operator/legacy requests.
func (deps Deps) requirePrincipal() (*projectinterface.Principal, *projectinterface.Error) {
	if deps.Principal != nil {
		return deps.Principal, nil
	}
	if deps.PrincipalError != nil {
		return nil, deps.PrincipalError
	}
	return nil, projectinterface.ValidationError(projectinterface.ErrCodeContinuationContextRequired,
		"this tool requires a Continuation Interface Grant", "authorization")
}

// toolProjectInterfaceError returns a structured isError result carrying the
// ProjectInterfaceErrorV1 envelope (runtime protocol §12.2: domain/interface
// failures are structured isError results, not unstructured transport errors).
func toolProjectInterfaceError(err *projectinterface.Error) (*sdkmcp.CallToolResult, any, error) {
	if err == nil {
		err = projectinterface.ValidationError(projectinterface.ErrCodeInvalidRequest, "unexpected failure", "internal")
	}
	envelope := struct {
		Error projectinterface.Error `json:"error"`
	}{Error: *err}
	data, _ := json.Marshal(envelope)
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(data)}},
		IsError: true,
	}, envelope, nil
}

type blackboardApplyArgs struct {
	ProtocolVersion    int                           `json:"protocol_version" jsonschema:"protocol version, always 1"`
	Batch              projectinterface.RequestBatch `json:"batch" jsonschema:"mutation batch"`
	SourceEventIDsByOp map[string][]string           `json:"source_event_ids_by_op,omitempty" jsonschema:"optional op_id to source Task Event IDs"`
}

type blackboardRetainEvidenceArgs projectinterface.RetainEvidenceRequest

type blackboardCheckpointAttemptArgs projectinterface.CheckpointAttemptRequest

type blackboardFinishContinuationArgs projectinterface.FinishContinuationRequest

type blackboardResolveRecordsArgs struct {
	ProtocolVersion int                           `json:"protocol_version" jsonschema:"protocol version, always 1"`
	Nodes           []projectinterface.NodeLookup `json:"nodes" jsonschema:"node references to resolve"`
	EdgeIDs         []string                      `json:"edge_ids,omitempty" jsonschema:"edge IDs to resolve"`
}

type blackboardCurrentGraphArgs struct {
	ProtocolVersion int `json:"protocol_version" jsonschema:"protocol version, always 1"`
}

type upsertProjectFactArgs struct {
	ProjectID       string `json:"project_id" jsonschema:"project id"`
	FactKey         string `json:"fact_key" jsonschema:"stable fact key"`
	Category        string `json:"category,omitempty" jsonschema:"fact category"`
	Summary         string `json:"summary" jsonschema:"short summary"`
	Body            string `json:"body,omitempty" jsonschema:"full fact body"`
	Confidence      string `json:"confidence,omitempty" jsonschema:"tentative, confirmed, or deprecated"`
	ScopeStatus     string `json:"scope_status,omitempty" jsonschema:"in_scope or out_of_scope"`
	ExpectedVersion *int   `json:"expected_version,omitempty" jsonschema:"optional current graph node version"`
	IdempotencyKey  string `json:"idempotency_key,omitempty" jsonschema:"optional exact replay key"`
}

func (deps Deps) compatibilityPrincipal(projectID string) (projectinterface.Principal, error) {
	if deps.Principal != nil {
		return *deps.Principal, nil
	}
	if deps.PrincipalError != nil {
		return projectinterface.Principal{}, deps.PrincipalError
	}
	return projectinterface.OperatorPrincipal(projectID, "local-operator")
}

func compatibilityError(err error) *projectinterface.Error {
	if mapped := projectinterface.AsError(err); mapped != nil {
		return mapped
	}
	var validation *blackboard.ValidationError
	if errors.As(err, &validation) {
		return &projectinterface.Error{
			ProtocolVersion: projectinterface.RuntimeProtocolVersion,
			Code:            validation.Code, Message: validation.Message, Path: validation.Path,
			Retryable: false, Details: validation.Details,
		}
	}
	return projectinterface.InternalError("unexpected compatibility failure")
}

func (deps Deps) legacyTool(name, description, replacement string) *sdkmcp.Tool {
	tool := &sdkmcp.Tool{Name: name, Description: description}
	if deps.Compatibility != nil {
		tool.Meta = sdkmcp.Meta{"deprecated": true, "replacement": replacement, "compatibility": "legacy_blackboard_v1"}
		tool.Description = "Deprecated compatibility tool. Use " + replacement + ". " + description
	}
	return tool
}

type projectFactKeyArgs struct {
	ProjectID       string `json:"project_id" jsonschema:"project id"`
	FactKey         string `json:"fact_key" jsonschema:"stable fact key"`
	ExpectedVersion *int   `json:"expected_version,omitempty" jsonschema:"optional current graph node version"`
	IdempotencyKey  string `json:"idempotency_key,omitempty" jsonschema:"optional exact replay key"`
}

type projectOnlyArgs struct {
	ProjectID string `json:"project_id" jsonschema:"project id"`
}

type searchFactsArgs struct {
	ProjectID         string `json:"project_id" jsonschema:"project id"`
	Query             string `json:"query" jsonschema:"search text"`
	IncludeDeprecated bool   `json:"include_deprecated,omitempty" jsonschema:"include deprecated facts"`
}

type upsertRelationArgs struct {
	ProjectID       string `json:"project_id" jsonschema:"project id"`
	SourceFactKey   string `json:"source_fact_key" jsonschema:"source fact key"`
	TargetFactKey   string `json:"target_fact_key" jsonschema:"target fact key"`
	Relation        string `json:"relation" jsonschema:"supports, contradicts, depends_on, leads_to, or duplicates"`
	Summary         string `json:"summary,omitempty" jsonschema:"relation summary"`
	ExpectedVersion *int   `json:"expected_version,omitempty" jsonschema:"optional current edge version"`
	IdempotencyKey  string `json:"idempotency_key,omitempty" jsonschema:"optional exact replay key"`
}

type recordFindingArgs struct {
	ProjectID       string `json:"project_id" jsonschema:"project id"`
	FindingKey      string `json:"finding_key" jsonschema:"stable finding key"`
	Title           string `json:"title" jsonschema:"finding title"`
	Description     string `json:"description,omitempty"`
	Status          string `json:"status,omitempty" jsonschema:"unconfirmed or confirmed"`
	Target          string `json:"target,omitempty"`
	Proof           string `json:"proof,omitempty"`
	Impact          string `json:"impact,omitempty"`
	Recommendation  string `json:"recommendation,omitempty"`
	CVSSVersion     string `json:"cvss_version,omitempty"`
	CVSSVector      string `json:"cvss_vector,omitempty"`
	ExpectedVersion *int   `json:"expected_version,omitempty" jsonschema:"optional current graph node version"`
	IdempotencyKey  string `json:"idempotency_key,omitempty" jsonschema:"optional exact replay key"`
}

type attachEvidenceArgs struct {
	ProjectID         string             `json:"project_id" jsonschema:"project id"`
	EvidenceKey       string             `json:"evidence_key" jsonschema:"stable evidence key"`
	AttachToType      string             `json:"attach_to_type" jsonschema:"fact or finding"`
	AttachToKey       string             `json:"attach_to_key" jsonschema:"target fact or finding key"`
	ArtifactType      string             `json:"artifact_type" jsonschema:"artifact type"`
	SourcePath        string             `json:"source_path,omitempty"`
	SHA256            string             `json:"sha256,omitempty"`
	Summary           string             `json:"summary,omitempty"`
	ExpectedVersion   *int               `json:"expected_version,omitempty" jsonschema:"optional current graph node version"`
	IdempotencyKey    string             `json:"idempotency_key,omitempty" jsonschema:"optional exact replay key"`
	ProducedByAttempt blackboard.NodeRef `json:"produced_by_attempt,omitempty" jsonschema:"required matching Attempt for Runtime Evidence"`
}

type generateReportArgs struct {
	ProjectID      string `json:"project_id" jsonschema:"project id"`
	TaskID         string `json:"task_id,omitempty" jsonschema:"task id for runner and scope context"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"optional compatibility use key"`
}

type submitTaskSummaryArgs struct {
	ProjectID      string `json:"project_id" jsonschema:"project id"`
	TaskID         string `json:"task_id" jsonschema:"task id"`
	Summary        string `json:"summary" jsonschema:"compact handoff summary for the next continuation"`
	SubmittedBy    string `json:"submitted_by,omitempty" jsonschema:"runtime identifier"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"optional exact Finish replay key"`
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

func toolError(err error) (*sdkmcp.CallToolResult, any, error) {
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
		IsError: true,
	}, nil, nil
}
