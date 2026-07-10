package blackboard

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

func annotateOperationError(err error, operationIndex int) error {
	var validation *ValidationError
	if errors.As(err, &validation) {
		validation.OperationIndex = operationIndex
	}
	return err
}

func validateExecutionContext(tx *sql.Tx, ec ExecutionContext) error {
	if ec.ActorType != ActorTypeRuntime {
		return nil
	}
	if ec.TaskID == "" || ec.ContinuationID == "" || ec.RuntimeProfileID == "" || ec.Runner == "" || ec.ActorID == "" {
		return validationError(ErrCodeProvenanceRequired, "Runtime provenance requires actor, Task, Continuation, runtime profile, and runner", -1, "", "context")
	}
	var projectID string
	if err := tx.QueryRow(`SELECT project_id FROM tasks WHERE id=?`, ec.TaskID).Scan(&projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return validationError(ErrCodeProvenanceSpoofed, "Runtime Task does not exist", -1, "", "context.task_id")
		}
		return fmt.Errorf("validate Runtime Task provenance: %w", err)
	}
	if projectID != ec.ProjectID {
		return validationError(ErrCodeProvenanceSpoofed, "Runtime Task does not belong to the trusted Project", -1, "", "context.task_id")
	}
	var taskID, profileID, runner string
	if err := tx.QueryRow(`SELECT task_id,runtime_profile_id,runner FROM task_continuations WHERE id=?`, ec.ContinuationID).Scan(&taskID, &profileID, &runner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return validationError(ErrCodeProvenanceSpoofed, "Runtime Continuation does not exist", -1, "", "context.continuation_id")
		}
		return fmt.Errorf("validate Runtime Continuation provenance: %w", err)
	}
	if taskID != ec.TaskID || profileID != ec.RuntimeProfileID || runner != ec.Runner {
		return validationError(ErrCodeProvenanceSpoofed, "Runtime provenance does not match the durable Continuation", -1, "", "context")
	}
	return nil
}

func validateAttemptCreates(batch MutationBatch, edges map[string][2]resolvedNode) *ValidationError {
	for i, op := range batch.Operations {
		if op.Kind != OpCreateNode || op.Node.NodeType != NodeTypeAttempt {
			continue
		}
		identity := "op:" + op.OpID
		if !hasProposedOutgoing(identity, EdgeTypeTests, batch, edges) {
			return validationError(ErrCodeTransitionGuardFailed, "open Attempt requires an outgoing tests edge", i, op.OpID, "operations[].properties.status")
		}
	}
	return nil
}

func hasProposedOutgoing(identity string, typ EdgeType, batch MutationBatch, edges map[string][2]resolvedNode) bool {
	for _, op := range batch.Operations {
		if op.Kind == OpPutEdge && op.PutEdge.EdgeType == typ && edges[op.OpID][0].identity == identity {
			return true
		}
	}
	return false
}

func validateRuntimeProducedEdges(tx *sql.Tx, projectID string, batch MutationBatch, edges map[string][2]resolvedNode) error {
	if batch.Context.ActorType != ActorTypeRuntime {
		return nil
	}
	requiresProduced := map[NodeType]bool{
		NodeTypeObservation: true, NodeTypeHypothesis: true, NodeTypeProjectFact: true,
		NodeTypeFinding: true, NodeTypeSolution: true, NodeTypeEvidenceArtifact: true,
	}
	for i, op := range batch.Operations {
		if op.Kind != OpCreateNode || !requiresProduced[op.Node.NodeType] {
			continue
		}
		foundProduced, foundMatching := false, false
		for _, edgeOp := range batch.Operations {
			if edgeOp.Kind != OpPutEdge || edgeOp.PutEdge.EdgeType != EdgeTypeProduced {
				continue
			}
			pair := edges[edgeOp.OpID]
			if pair[1].identity != "op:"+op.OpID || pair[0].nodeType != NodeTypeAttempt {
				continue
			}
			foundProduced = true
			if pair[0].nodeID == "" {
				foundMatching = true
				break
			}
			taskID, continuationID, err := nodeHeadProvenance(tx, projectID, pair[0].nodeID)
			if err != nil {
				return fmt.Errorf("read producing Attempt provenance: %w", err)
			}
			if taskID == batch.Context.TaskID && continuationID == batch.Context.ContinuationID {
				foundMatching = true
				break
			}
		}
		if !foundProduced {
			return validationError(ErrCodeProvenanceRequired, "Runtime-created semantic records require an incoming produced edge", i, op.OpID, "operations[].node")
		}
		if !foundMatching {
			return validationError(ErrCodeProvenanceSpoofed, "producing Attempt provenance does not match Runtime Task and Continuation", i, op.OpID, "operations[].node")
		}
	}
	return nil
}

func nodeHeadProvenance(tx *sql.Tx, projectID, nodeID string) (string, string, error) {
	var taskID, continuationID sql.NullString
	err := tx.QueryRow(`SELECT p.task_id,p.continuation_id
		FROM blackboard_node_heads h
		JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version
		JOIN blackboard_graph_operations o ON o.project_id=v.project_id AND o.mutation_seq=v.mutation_seq AND o.operation_index=v.operation_index
		JOIN blackboard_graph_provenance p ON p.project_id=o.project_id AND p.id=o.provenance_id
		WHERE h.project_id=? AND h.node_id=?`, projectID, nodeID).Scan(&taskID, &continuationID)
	return taskID.String, continuationID.String, err
}

func applyObjectiveTransition(tx *sql.Tx, projectID string, current mutableNode, op Operation, batch MutationBatch, edges map[string][2]resolvedNode, at time.Time, props map[string]any) error {
	oldStatus, _ := props["status"].(string)
	newStatus := op.Transition.Status
	if oldStatus != "open" || (newStatus != "resolved" && newStatus != "abandoned" && newStatus != "superseded") {
		return validationError(ErrCodeInvalidTransition, "Exploration Objective transitions are open -> resolved|abandoned|superseded only", -1, op.OpID, "operations[].transition.status")
	}
	if strings.TrimSpace(op.Transition.ResolutionSummary) == "" {
		return validationError(ErrCodeMissingProperty, "terminal Objective transition requires resolution_summary", -1, op.OpID, "operations[].transition.resolution_summary")
	}
	if newStatus == "resolved" {
		ok, err := hasIncomingEdge(tx, projectID, current.nodeID, EdgeTypeSatisfies, batch, edges)
		if err != nil {
			return err
		}
		if !ok {
			return validationError(ErrCodeTransitionGuardFailed, "resolved Exploration Objective requires an incoming satisfies edge", -1, op.OpID, "operations[].transition.status")
		}
	}
	if newStatus == "superseded" {
		ok, err := hasIncomingEdge(tx, projectID, current.nodeID, EdgeTypeSupersedes, batch, edges)
		if err != nil {
			return err
		}
		if !ok {
			return validationError(ErrCodeTransitionGuardFailed, "superseded Exploration Objective requires an incoming supersedes edge", -1, op.OpID, "operations[].transition.status")
		}
	}
	props["status"], props["resolution_summary"], props["resolved_at"] = newStatus, op.Transition.ResolutionSummary, at.Format(time.RFC3339Nano)
	return nil
}

const attemptInterruptionReconcilerActor = "task-interruption-reconciler"

func applyAttemptTransition(tx *sql.Tx, projectID string, current mutableNode, op Operation, batch MutationBatch, edges map[string][2]resolvedNode, at time.Time, props map[string]any) error {
	oldStatus, _ := props["status"].(string)
	newStatus := op.Transition.Status
	if oldStatus != "open" || !isAttemptTerminal(newStatus) {
		return validationError(ErrCodeInvalidTransition, "Attempt may transition once from open to a terminal outcome", -1, op.OpID, "operations[].transition.status")
	}
	summary := op.Transition.Summary
	if summary == "" {
		summary = op.Transition.ResolutionSummary
	}
	if strings.TrimSpace(summary) == "" {
		return validationError(ErrCodeMissingProperty, "terminal Attempt requires summary", -1, op.OpID, "operations[].transition.summary")
	}
	if newStatus == "interrupted" && (batch.Context.ActorType != ActorTypeSystem || batch.Context.ActorID != attemptInterruptionReconcilerActor) {
		return validationError(ErrCodeTransitionGuardFailed, "only system reconciliation may interrupt an Attempt", -1, op.OpID, "operations[].transition.status")
	}
	if newStatus == "succeeded" {
		ok, err := hasOutgoingEdge(tx, projectID, current.nodeID, EdgeTypeProduced, batch, edges)
		if err != nil {
			return err
		}
		if !ok {
			return validationError(ErrCodeTransitionGuardFailed, "succeeded Attempt requires an outgoing produced edge", -1, op.OpID, "operations[].transition.status")
		}
	}
	props["status"], props["summary"], props["ended_at"] = newStatus, summary, at.Format(time.RFC3339Nano)
	return nil
}

func isAttemptTerminal(status string) bool {
	switch status {
	case "succeeded", "failed", "blocked", "inconclusive", "interrupted":
		return true
	}
	return false
}

func hasOutgoingEdge(tx *sql.Tx, projectID, nodeID string, typ EdgeType, batch MutationBatch, edges map[string][2]resolvedNode) (bool, error) {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM blackboard_edge_heads WHERE project_id=? AND edge_type=? AND from_node_id=? AND state='active'`, projectID, string(typ), nodeID).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	return hasProposedOutgoing("id:"+nodeID, typ, batch, edges), nil
}

func applyHypothesisTransition(tx *sql.Tx, projectID string, current mutableNode, op Operation, batch MutationBatch, edges map[string][2]resolvedNode, props map[string]any) error {
	old, _ := props["status"].(string)
	next := op.Transition.Status
	allowed := (old == "open" && contains(next, "supported", "contradicted", "inconclusive", "superseded")) ||
		(old == "inconclusive" && contains(next, "open", "supported", "contradicted", "superseded")) ||
		(old == "supported" && contains(next, "contradicted", "superseded")) ||
		(old == "contradicted" && contains(next, "supported", "superseded"))
	if !allowed {
		return validationError(ErrCodeInvalidTransition, "invalid Hypothesis transition", -1, op.OpID, "operations[].transition.status")
	}
	guard := map[string]EdgeType{"supported": EdgeTypeSupports, "contradicted": EdgeTypeContradicts, "superseded": EdgeTypeSupersedes}[next]
	if guard != "" {
		ok, err := hasIncomingEdge(tx, projectID, current.nodeID, guard, batch, edges)
		if err != nil {
			return err
		}
		if !ok {
			return validationError(ErrCodeTransitionGuardFailed, next+" Hypothesis lacks semantic support", -1, op.OpID, "operations[].transition.status")
		}
	}
	props["status"] = next
	return nil
}

func applyProjectFactTransition(tx *sql.Tx, projectID string, current mutableNode, op Operation, batch MutationBatch, edges map[string][2]resolvedNode, props map[string]any) error {
	old, _ := props["confidence"].(string)
	next := op.Transition.Status
	if !((old == "tentative" && contains(next, "confirmed", "deprecated")) || (old == "confirmed" && contains(next, "tentative", "deprecated"))) {
		return validationError(ErrCodeInvalidTransition, "invalid Project Fact confidence transition", -1, op.OpID, "operations[].transition.status")
	}
	if next == "confirmed" {
		supported, err := projectFactConfirmationSupported(tx, projectID, "id:"+current.nodeID, props, batch, edges)
		if err != nil {
			return err
		}
		if !supported {
			return validationError(ErrCodeTransitionGuardFailed, "confirmed Project Fact requires a confirmation basis", -1, op.OpID, "operations[].transition.status")
		}
	}
	props["confidence"] = next
	return nil
}

func applyFindingTransition(tx *sql.Tx, projectID string, current mutableNode, op Operation, batch MutationBatch, edges map[string][2]resolvedNode, props map[string]any) error {
	old, _ := props["status"].(string)
	next := op.Transition.Status
	if !((old == "unconfirmed" && contains(next, "confirmed", "false_positive")) || (old == "confirmed" && next == "false_positive")) {
		return validationError(ErrCodeInvalidTransition, "invalid Finding transition", -1, op.OpID, "operations[].transition.status")
	}
	if next == "confirmed" {
		for _, key := range []string{"target", "proof", "impact", "recommendation", "cvss_version", "cvss_vector"} {
			if strings.TrimSpace(stringProp(props, key)) == "" {
				return validationError(ErrCodeMissingProperty, "confirmed Finding requires "+key, -1, op.OpID, "operations[].transition.status")
			}
		}
		if !validCVSS(props) {
			return validationError(ErrCodeInvalidProperty, "confirmed Finding requires a complete CVSS vector matching cvss_version", -1, op.OpID, "operations[].transition.status")
		}
		supported, err := findingConfirmationSupported(tx, projectID, "id:"+current.nodeID, batch, edges)
		if err != nil {
			return err
		}
		if !supported {
			return validationError(ErrCodeTransitionGuardFailed, "confirmed Finding requires an Evidence Artifact or confirmed Project Fact", -1, op.OpID, "operations[].transition.status")
		}
	}
	props["status"] = next
	return nil
}

func applySolutionTransition(tx *sql.Tx, projectID string, current mutableNode, op Operation, batch MutationBatch, edges map[string][2]resolvedNode, props map[string]any) error {
	old, _ := props["status"].(string)
	next := op.Transition.Status
	allowed := (old == "candidate" && contains(next, "verified", "rejected", "superseded")) ||
		(old == "verified" && contains(next, "rejected", "superseded"))
	if !allowed {
		return validationError(ErrCodeInvalidTransition, "Solution transitions are candidate -> verified|rejected|superseded and verified -> rejected|superseded", -1, op.OpID, "operations[].transition.status")
	}
	if next == "verified" {
		if strings.TrimSpace(op.Transition.VerificationSummary) == "" {
			return validationError(ErrCodeMissingProperty, "verified Solution requires verification_summary", -1, op.OpID, "operations[].transition.verification_summary")
		}
		if props["kind"] == "flag" {
			if !solutionVerificationActor(batch.Context.ActorType) {
				return validationError(ErrCodeTransitionGuardFailed, "verified flag Solution requires Runtime, operator, or system provenance", -1, op.OpID, "operations[].transition.status")
			}
			producingTaskID, _, err := nodeHeadProvenance(tx, projectID, current.nodeID)
			if err != nil {
				return fmt.Errorf("read Solution producing Task provenance: %w", err)
			}
			supported, err := solutionSatisfiesTaskGoal(tx, projectID, "id:"+current.nodeID, producingTaskID, batch, edges)
			if err != nil {
				return err
			}
			if !supported {
				return validationError(ErrCodeTransitionGuardFailed, "verified flag Solution must satisfy its producing Task Goal", -1, op.OpID, "operations[].transition.status")
			}
		}
		props["verification_summary"] = op.Transition.VerificationSummary
	}
	if next == "superseded" {
		ok, err := hasIncomingEdge(tx, projectID, current.nodeID, EdgeTypeSupersedes, batch, edges)
		if err != nil {
			return err
		}
		if !ok {
			return validationError(ErrCodeTransitionGuardFailed, "superseded Solution requires an incoming supersedes edge", -1, op.OpID, "operations[].transition.status")
		}
	}
	props["status"] = next
	return nil
}

func solutionVerificationActor(actor ActorType) bool {
	return actor == ActorTypeRuntime || actor == ActorTypeOperator || actor == ActorTypeSystem
}

func solutionSatisfiesTaskGoal(tx *sql.Tx, projectID, solutionIdentity, taskID string, batch MutationBatch, edges map[string][2]resolvedNode) (bool, error) {
	if strings.TrimSpace(taskID) == "" {
		return false, nil
	}
	if strings.HasPrefix(solutionIdentity, "id:") {
		var count int
		err := tx.QueryRow(`SELECT COUNT(*)
			FROM blackboard_edge_heads e
			JOIN blackboard_node_heads h ON h.project_id=e.project_id AND h.node_id=e.to_node_id AND h.node_type='goal' AND h.disposition='main'
			JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version
			WHERE e.project_id=? AND e.edge_type='satisfies' AND e.from_node_id=? AND e.state='active'
			  AND json_extract(v.properties_json, '$.task_id')=?`, projectID, strings.TrimPrefix(solutionIdentity, "id:"), taskID).Scan(&count)
		if err != nil {
			return false, fmt.Errorf("check Solution satisfying Goal: %w", err)
		}
		if count > 0 {
			return true, nil
		}
	}
	for _, edgeOp := range batch.Operations {
		if edgeOp.Kind != OpPutEdge || edgeOp.PutEdge.EdgeType != EdgeTypeSatisfies {
			continue
		}
		pair := edges[edgeOp.OpID]
		if pair[0].identity != solutionIdentity || pair[1].nodeType != NodeTypeGoal {
			continue
		}
		goalTaskID, err := resolvedGoalTaskID(tx, projectID, pair[1], batch)
		if err != nil {
			return false, err
		}
		if goalTaskID == taskID {
			return true, nil
		}
	}
	return false, nil
}

func resolvedGoalTaskID(tx *sql.Tx, projectID string, goal resolvedNode, batch MutationBatch) (string, error) {
	if goal.opID != "" {
		op, ok := findOperation(batch, goal.opID)
		if !ok {
			return "", nil
		}
		return stringProp(normalizedCreateProperties(op), "task_id"), nil
	}
	var propertiesJSON string
	if err := tx.QueryRow(`SELECT v.properties_json FROM blackboard_node_heads h JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version WHERE h.project_id=? AND h.node_id=? AND h.node_type='goal' AND h.disposition='main'`, projectID, goal.nodeID).Scan(&propertiesJSON); err != nil {
		return "", fmt.Errorf("read satisfying Goal: %w", err)
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(propertiesJSON), &props); err != nil {
		return "", fmt.Errorf("decode satisfying Goal: %w", err)
	}
	return stringProp(props, "task_id"), nil
}

func validateCreatedConfirmations(tx *sql.Tx, projectID string, batch MutationBatch, edges map[string][2]resolvedNode) error {
	for i, op := range batch.Operations {
		if op.Kind != OpCreateNode {
			continue
		}
		props := normalizedCreateProperties(op)
		identity := "op:" + op.OpID
		if op.Node.NodeType == NodeTypeHypothesis {
			status, _ := props["status"].(string)
			guard := map[string]EdgeType{"supported": EdgeTypeSupports, "contradicted": EdgeTypeContradicts, "superseded": EdgeTypeSupersedes}[status]
			if guard != "" {
				supported, err := incomingFrom(tx, projectID, identity, guard, nil, batch, edges)
				if err != nil {
					return err
				}
				if !supported {
					return validationError(ErrCodeTransitionGuardFailed, status+" Hypothesis lacks semantic support", i, op.OpID, "operations[].properties.status")
				}
			}
		}
		if op.Node.NodeType == NodeTypeProjectFact && props["confidence"] == "confirmed" {
			supported, err := projectFactConfirmationSupported(tx, projectID, identity, props, batch, edges)
			if err != nil {
				return err
			}
			if !supported {
				return validationError(ErrCodeTransitionGuardFailed, "confirmed Project Fact requires a confirmation basis", i, op.OpID, "operations[].properties.confidence")
			}
		}
		if op.Node.NodeType == NodeTypeFinding && props["status"] == "confirmed" {
			for _, key := range []string{"target", "proof", "impact", "recommendation", "cvss_version", "cvss_vector"} {
				if strings.TrimSpace(stringProp(props, key)) == "" {
					return validationError(ErrCodeMissingProperty, "confirmed Finding requires "+key, i, op.OpID, "operations[].properties."+key)
				}
			}
			if !validCVSS(props) {
				return validationError(ErrCodeInvalidProperty, "confirmed Finding requires a complete CVSS vector matching cvss_version", i, op.OpID, "operations[].properties.cvss_vector")
			}
			supported, err := findingConfirmationSupported(tx, projectID, identity, batch, edges)
			if err != nil {
				return err
			}
			if !supported {
				return validationError(ErrCodeTransitionGuardFailed, "confirmed Finding requires an Evidence Artifact or confirmed Project Fact", i, op.OpID, "operations[].properties.status")
			}
		}
		if op.Node.NodeType == NodeTypeSolution {
			status, _ := props["status"].(string)
			if status == "verified" && props["kind"] == "flag" {
				if !solutionVerificationActor(batch.Context.ActorType) {
					return validationError(ErrCodeTransitionGuardFailed, "verified flag Solution requires Runtime, operator, or system provenance", i, op.OpID, "operations[].properties.status")
				}
				supported, err := solutionSatisfiesTaskGoal(tx, projectID, identity, batch.Context.TaskID, batch, edges)
				if err != nil {
					return err
				}
				if !supported {
					return validationError(ErrCodeTransitionGuardFailed, "verified flag Solution must satisfy its producing Task Goal", i, op.OpID, "operations[].properties.status")
				}
			}
			if status == "superseded" {
				supported, err := incomingFrom(tx, projectID, identity, EdgeTypeSupersedes, nil, batch, edges)
				if err != nil {
					return err
				}
				if !supported {
					return validationError(ErrCodeTransitionGuardFailed, "superseded Solution requires an incoming supersedes edge", i, op.OpID, "operations[].properties.status")
				}
			}
		}
	}
	return nil
}

type resolvedNodePredicate func(resolvedNode) (bool, error)

func projectFactConfirmationSupported(tx *sql.Tx, projectID, target string, props map[string]any, batch MutationBatch, edges map[string][2]resolvedNode) (bool, error) {
	if batch.Context.ActorType != ActorTypeRuntime && strings.TrimSpace(stringProp(props, "body")) != "" {
		return true, nil
	}
	if supported, err := incomingFrom(tx, projectID, target, EdgeTypeEvidences, nil, batch, edges); err != nil || supported {
		return supported, err
	}
	if supported, err := incomingFrom(tx, projectID, target, EdgeTypeSupports, func(n resolvedNode) (bool, error) {
		if n.nodeType == NodeTypeObservation {
			return true, nil
		}
		if n.nodeType != NodeTypeProjectFact {
			return false, nil
		}
		state, err := finalNodeState(tx, projectID, n, batch)
		return state == "confirmed", err
	}, batch, edges); err != nil || supported {
		return supported, err
	}
	return incomingFrom(tx, projectID, target, EdgeTypeProduced, func(n resolvedNode) (bool, error) {
		if n.nodeType != NodeTypeAttempt {
			return false, nil
		}
		state, err := finalNodeState(tx, projectID, n, batch)
		if err != nil || state != "succeeded" {
			return false, err
		}
		if batch.Context.ActorType != ActorTypeRuntime {
			return false, nil
		}
		if n.nodeID == "" {
			return true, nil
		}
		taskID, contID, err := nodeHeadProvenance(tx, projectID, n.nodeID)
		if err != nil {
			return false, fmt.Errorf("read producing Attempt provenance: %w", err)
		}
		return taskID == batch.Context.TaskID && contID == batch.Context.ContinuationID, nil
	}, batch, edges)
}

func findingConfirmationSupported(tx *sql.Tx, projectID, target string, batch MutationBatch, edges map[string][2]resolvedNode) (bool, error) {
	if supported, err := incomingFrom(tx, projectID, target, EdgeTypeEvidences, func(n resolvedNode) (bool, error) {
		return n.nodeType == NodeTypeEvidenceArtifact, nil
	}, batch, edges); err != nil || supported {
		return supported, err
	}
	return incomingFrom(tx, projectID, target, EdgeTypeSupports, func(n resolvedNode) (bool, error) {
		if n.nodeType != NodeTypeProjectFact {
			return false, nil
		}
		state, err := finalNodeState(tx, projectID, n, batch)
		return state == "confirmed", err
	}, batch, edges)
}

func incomingFrom(tx *sql.Tx, projectID, target string, typ EdgeType, accept resolvedNodePredicate, batch MutationBatch, edges map[string][2]resolvedNode) (bool, error) {
	if strings.HasPrefix(target, "id:") {
		rows, err := tx.Query(`SELECT h.node_id,h.node_type FROM blackboard_edge_heads e JOIN blackboard_node_heads h ON h.project_id=e.project_id AND h.node_id=e.from_node_id WHERE e.project_id=? AND e.edge_type=? AND e.to_node_id=? AND e.state='active'`, projectID, string(typ), strings.TrimPrefix(target, "id:"))
		if err != nil {
			return false, fmt.Errorf("query incoming %s edges: %w", typ, err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, nt string
			if err := rows.Scan(&id, &nt); err != nil {
				return false, fmt.Errorf("scan incoming %s edge: %w", typ, err)
			}
			n := resolvedNode{identity: "id:" + id, nodeID: id, nodeType: NodeType(nt)}
			if accept == nil {
				return true, nil
			}
			accepted, err := accept(n)
			if err != nil {
				return false, err
			}
			if accepted {
				return true, nil
			}
		}
		if err := rows.Err(); err != nil {
			return false, fmt.Errorf("iterate incoming %s edges: %w", typ, err)
		}
	}
	for _, op := range batch.Operations {
		if op.Kind != OpPutEdge || op.PutEdge.EdgeType != typ {
			continue
		}
		pair := edges[op.OpID]
		if pair[1].identity != target {
			continue
		}
		if accept == nil {
			return true, nil
		}
		accepted, err := accept(pair[0])
		if err != nil {
			return false, err
		}
		if accepted {
			return true, nil
		}
	}
	return false, nil
}

func finalNodeState(tx *sql.Tx, projectID string, n resolvedNode, batch MutationBatch) (string, error) {
	if n.opID != "" {
		if op, ok := findOperation(batch, n.opID); ok {
			p := normalizedCreateProperties(op)
			if s, _ := p["status"].(string); s != "" {
				return s, nil
			}
			s, _ := p["confidence"].(string)
			return s, nil
		}
	}
	var props string
	if err := tx.QueryRow(`SELECT v.properties_json FROM blackboard_node_heads h JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version WHERE h.project_id=? AND h.node_id=?`, projectID, n.nodeID).Scan(&props); err != nil {
		return "", fmt.Errorf("read final node state: %w", err)
	}
	state, err := jsonState(props)
	if err != nil {
		return "", err
	}
	for _, op := range batch.Operations {
		if op.Kind == OpTransitionNode && nodeRefMatches(op.Node, n) {
			state = op.Transition.Status
		}
	}
	return state, nil
}

func jsonState(raw string) (string, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return "", fmt.Errorf("decode node state: %w", err)
	}
	if s, _ := m["status"].(string); s != "" {
		return s, nil
	}
	s, _ := m["confidence"].(string)
	return s, nil
}

func findOperation(batch MutationBatch, id string) (Operation, bool) {
	for _, op := range batch.Operations {
		if op.OpID == id {
			return op, true
		}
	}
	return Operation{}, false
}

func nodeRefMatches(ref NodeRef, n resolvedNode) bool {
	return ref.ID != "" && ref.ID == n.nodeID || ref.NodeType == n.nodeType && ref.StableKey != "" && ref.StableKey == n.stableKey
}

var cvssMetricPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*:[A-Za-z0-9]+$`)

type cvssMetricSpec struct {
	name     string
	values   []string
	required bool
}

func validCVSS(props map[string]any) bool {
	version := stringProp(props, "cvss_version")
	vector := stringProp(props, "cvss_vector")
	var specs []cvssMetricSpec
	switch version {
	case "4.0":
		specs = []cvssMetricSpec{
			{"AV", []string{"N", "A", "L", "P"}, true}, {"AC", []string{"L", "H"}, true}, {"AT", []string{"N", "P"}, true},
			{"PR", []string{"N", "L", "H"}, true}, {"UI", []string{"N", "P", "A"}, true},
			{"VC", []string{"H", "L", "N"}, true}, {"VI", []string{"H", "L", "N"}, true}, {"VA", []string{"H", "L", "N"}, true},
			{"SC", []string{"H", "L", "N"}, true}, {"SI", []string{"H", "L", "N"}, true}, {"SA", []string{"H", "L", "N"}, true},
			{"E", []string{"X", "A", "P", "U"}, false},
			{"CR", []string{"X", "H", "M", "L"}, false}, {"IR", []string{"X", "H", "M", "L"}, false}, {"AR", []string{"X", "H", "M", "L"}, false},
			{"MAV", []string{"X", "N", "A", "L", "P"}, false}, {"MAC", []string{"X", "L", "H"}, false}, {"MAT", []string{"X", "N", "P"}, false},
			{"MPR", []string{"X", "N", "L", "H"}, false}, {"MUI", []string{"X", "N", "P", "A"}, false},
			{"MVC", []string{"X", "H", "L", "N"}, false}, {"MVI", []string{"X", "H", "L", "N"}, false}, {"MVA", []string{"X", "H", "L", "N"}, false},
			{"MSC", []string{"X", "H", "L", "N"}, false}, {"MSI", []string{"X", "H", "L", "N", "S"}, false}, {"MSA", []string{"X", "H", "L", "N", "S"}, false},
			{"S", []string{"X", "N", "P"}, false}, {"AU", []string{"X", "N", "Y"}, false}, {"R", []string{"X", "A", "U", "I"}, false},
			{"V", []string{"X", "D", "C"}, false}, {"RE", []string{"X", "L", "M", "H"}, false}, {"U", []string{"X", "Clear", "Green", "Amber", "Red"}, false},
		}
	case "3.1":
		specs = []cvssMetricSpec{
			{"AV", []string{"N", "A", "L", "P"}, true}, {"AC", []string{"L", "H"}, true}, {"PR", []string{"N", "L", "H"}, true},
			{"UI", []string{"N", "R"}, true}, {"S", []string{"U", "C"}, true}, {"C", []string{"H", "L", "N"}, true},
			{"I", []string{"H", "L", "N"}, true}, {"A", []string{"H", "L", "N"}, true},
			{"E", []string{"X", "U", "P", "F", "H"}, false}, {"RL", []string{"X", "O", "T", "W", "U"}, false}, {"RC", []string{"X", "U", "R", "C"}, false},
			{"CR", []string{"X", "L", "M", "H"}, false}, {"IR", []string{"X", "L", "M", "H"}, false}, {"AR", []string{"X", "L", "M", "H"}, false},
			{"MAV", []string{"X", "N", "A", "L", "P"}, false}, {"MAC", []string{"X", "L", "H"}, false}, {"MPR", []string{"X", "N", "L", "H"}, false},
			{"MUI", []string{"X", "N", "R"}, false}, {"MS", []string{"X", "U", "C"}, false},
			{"MC", []string{"X", "N", "L", "H"}, false}, {"MI", []string{"X", "N", "L", "H"}, false}, {"MA", []string{"X", "N", "L", "H"}, false},
		}
	default:
		return false
	}
	parts := strings.Split(vector, "/")
	if len(parts) < 2 || parts[0] != "CVSS:"+version {
		return false
	}
	byName := make(map[string]cvssMetricSpec, len(specs))
	order := make(map[string]int, len(specs))
	for i, spec := range specs {
		byName[spec.name] = spec
		order[spec.name] = i
	}
	seen := make(map[string]bool, len(parts)-1)
	lastOrder := -1
	for _, metric := range parts[1:] {
		if !cvssMetricPattern.MatchString(metric) {
			return false
		}
		name, value, _ := strings.Cut(metric, ":")
		spec, known := byName[name]
		if !known || seen[name] || order[name] <= lastOrder || !contains(value, spec.values...) {
			return false
		}
		seen[name] = true
		lastOrder = order[name]
	}
	for _, spec := range specs {
		if spec.required && !seen[spec.name] {
			return false
		}
	}
	return true
}

func contains(v string, values ...string) bool {
	for _, x := range values {
		if v == x {
			return true
		}
	}
	return false
}
func stringProp(m map[string]any, key string) string { s, _ := m[key].(string); return s }
