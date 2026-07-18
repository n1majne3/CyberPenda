package blackboard

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const taskGoalProjectorActor = "task-goal-projector"

// ProjectTaskGoal projects the current durable Task row into its one system-
// owned Goal. It rereads Task state on every call, so concurrent status writers
// converge on the latest committed Task status rather than caller timing.
func (s *GraphService) ProjectTaskGoal(taskID string) error {
	for attempt := 0; attempt < 8; attempt++ {
		err := s.projectTaskGoalOnce(taskID)
		var validation *ValidationError
		if !errors.As(err, &validation) || validation.Code != ErrCodeVersionConflict {
			return err
		}
	}
	return validationError(ErrCodeVersionConflict, "Task Goal projection did not converge after concurrent updates", -1, "", "goal")
}

func (s *GraphService) projectTaskGoalOnce(taskID string) error {
	var projectID, projectKind, text, status, updatedAt string
	err := s.db.QueryRow(`
		SELECT t.project_id, p.kind, t.goal, t.status, t.updated_at
		FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.id = ?`, taskID).Scan(&projectID, &projectKind, &text, &status, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return validationError(ErrCodeNodeNotFound, "task does not exist", -1, "", "task_id")
	}
	if err != nil {
		return fmt.Errorf("read task goal projection source: %w", err)
	}

	key := "task:" + taskID + ":goal"
	props := map[string]any{"task_id": taskID, "text": text, "task_status": status}
	ctx := SystemExecutionContext(projectID, projectKind, taskGoalProjectorActor)
	ctx.TaskID = taskID

	var projectedKeys []string
	rows, err := s.db.Query(`
		SELECT n.original_stable_key
		FROM blackboard_node_heads h
		JOIN blackboard_nodes n ON n.project_id=h.project_id AND n.id=h.node_id
		JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version
		WHERE h.project_id=? AND h.node_type='goal' AND json_extract(v.properties_json,'$.task_id')=?
		ORDER BY n.original_stable_key,h.node_id`, projectID, taskID)
	if err != nil {
		return fmt.Errorf("find Task Goal projection: %w", err)
	}
	for rows.Next() {
		var candidate string
		if err := rows.Scan(&candidate); err != nil {
			_ = rows.Close()
			return err
		}
		projectedKeys = append(projectedKeys, candidate)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if len(projectedKeys) > 1 || (len(projectedKeys) == 1 && projectedKeys[0] != key) {
		return validationError(ErrCodeInvariantViolation, "Task Goal projection stable-key drift or duplication detected", -1, "", "goal")
	}

	current, err := s.ReadNode(context.Background(), ReadNodeRequest{ProjectID: projectID, NodeType: NodeTypeGoal, Key: key})
	if err != nil {
		var validation *ValidationError
		if !errors.As(err, &validation) || validation.Code != ErrCodeNodeNotFound {
			return err
		}
		_, err = s.Apply(context.Background(), MutationBatch{
			SchemaVersion:  GraphMutationSchemaVersion,
			IdempotencyKey: goalProjectionKey(taskID, updatedAt, "create"),
			Context:        ctx,
			Operations: []Operation{{
				OpID:   "project-goal",
				Kind:   OpCreateNode,
				Node:   NodeRef{NodeType: NodeTypeGoal, StableKey: key},
				Create: CreateNodeInput{PropertyMap: props},
			}},
		})
		return err
	}

	if current.Node.StableKey != key || current.Node.PropertyMap["task_id"] != taskID || current.Node.PropertyMap["text"] != text {
		return validationError(ErrCodeInvariantViolation,
			"Task Goal projection identity or immutable content drifted", -1, "", "goal")
	}
	if current.Node.PropertyMap["task_status"] == status {
		return nil
	}
	_, err = s.Apply(context.Background(), MutationBatch{
		SchemaVersion:  GraphMutationSchemaVersion,
		IdempotencyKey: goalProjectionKey(taskID, updatedAt, fmt.Sprintf("status:v%d", current.Node.Version)),
		Context:        ctx,
		Operations: []Operation{{
			OpID:  "sync-goal-status",
			Kind:  OpPatchNode,
			Node:  NodeRef{NodeType: NodeTypeGoal, StableKey: key},
			Patch: PatchNodeInput{ExpectedVersion: current.Node.Version, Properties: map[string]any{"task_status": status}},
		}},
	})
	return err
}

func goalProjectionKey(taskID, updatedAt, action string) string {
	sum := sha256.Sum256([]byte(updatedAt))
	return "task:" + taskID + ":goal:" + action + ":" + hex.EncodeToString(sum[:8])
}

// RepairTaskGoals creates missing Goals and synchronizes status-stale Goals.
// Immutable Task ID, Goal text, or stable-key drift is returned as a critical
// invariant violation and is never rewritten.
func (s *GraphService) RepairTaskGoals(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM tasks ORDER BY project_id, created_at, id`)
	if err != nil {
		return fmt.Errorf("list tasks for Goal repair: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan task for Goal repair: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate tasks for Goal repair: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close Task Goal repair rows: %w", err)
	}
	for _, id := range ids {
		if err := s.ProjectTaskGoal(id); err != nil {
			return fmt.Errorf("repair Goal for Task %s: %w", id, err)
		}
	}
	return nil
}

// FrontierExplorationObjective is an open Exploration Objective whose active
// prerequisites and blockers are all resolved at the observed graph revision.
type FrontierGoal struct {
	ID         string `json:"id"`
	StableKey  string `json:"stable_key"`
	TaskStatus string `json:"task_status"`
}

type FrontierExplorationObjective struct {
	ID          string         `json:"id"`
	StableKey   string         `json:"stable_key"`
	Objective   string         `json:"objective"`
	CreatedAt   string         `json:"created_at"`
	Rank        int            `json:"rank"`
	ParentGoals []FrontierGoal `json:"parent_goals,omitempty"`
	PropertyMap map[string]any `json:"properties"`
	goalRank    int
}

type FrontierResult struct {
	ObservedGraphRevision int                            `json:"observed_graph_revision"`
	Objectives            []FrontierExplorationObjective `json:"objectives"`
}

// ExplorationFrontier derives readiness and never persists ranks or claims.
func (s *GraphService) ExplorationFrontier(ctx context.Context, projectID string) (FrontierResult, error) {
	var result FrontierResult
	_ = s.db.QueryRowContext(ctx, `SELECT current_graph_revision FROM blackboard_graph_state WHERE project_id=?`, projectID).Scan(&result.ObservedGraphRevision)

	rows, err := s.db.QueryContext(ctx, `
		SELECT h.node_id,n.original_stable_key,n.created_at,v.properties_json
		FROM blackboard_node_heads h
		JOIN blackboard_nodes n ON n.project_id=h.project_id AND n.id=h.node_id
		JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version
		WHERE h.project_id=? AND h.node_type=? AND h.disposition='main'
		ORDER BY n.created_at,n.original_stable_key,h.node_id`, projectID, string(NodeTypeExplorationObjective))
	if err != nil {
		return FrontierResult{}, fmt.Errorf("list exploration objectives: %w", err)
	}
	var candidates []FrontierExplorationObjective
	for rows.Next() {
		var item FrontierExplorationObjective
		var propsJSON string
		if err := rows.Scan(&item.ID, &item.StableKey, &item.CreatedAt, &propsJSON); err != nil {
			_ = rows.Close()
			return FrontierResult{}, fmt.Errorf("scan exploration objective: %w", err)
		}
		if err := json.Unmarshal([]byte(propsJSON), &item.PropertyMap); err != nil {
			// A corrupt materialized head is excluded from the Frontier. Dependents
			// still observe it through frontierBlockingReasons and remain blocked.
			continue
		}
		item.Objective, _ = item.PropertyMap["objective"].(string)
		if item.PropertyMap["status"] == "open" {
			candidates = append(candidates, item)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return FrontierResult{}, fmt.Errorf("iterate exploration objectives: %w", err)
	}
	if err := rows.Close(); err != nil {
		return FrontierResult{}, fmt.Errorf("close exploration objective rows: %w", err)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].CreatedAt != candidates[j].CreatedAt {
			return candidates[i].CreatedAt < candidates[j].CreatedAt
		}
		if candidates[i].StableKey != candidates[j].StableKey {
			return candidates[i].StableKey < candidates[j].StableKey
		}
		return candidates[i].ID < candidates[j].ID
	})
	for _, item := range candidates {
		reasons, err := s.frontierBlockingReasons(ctx, projectID, item.ID)
		if err != nil {
			return FrontierResult{}, err
		}
		if len(reasons) > 0 {
			continue
		}
		item.Rank = len(result.Objectives) + 1
		result.Objectives = append(result.Objectives, item)
	}
	return result, nil
}

func (s *GraphService) frontierParentGoals(ctx context.Context, projectID, objectiveID string) ([]FrontierGoal, int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.node_id,n.original_stable_key,v.properties_json
		FROM blackboard_edge_heads e
		JOIN blackboard_node_heads h ON h.project_id=e.project_id AND h.node_id=e.to_node_id AND h.node_type='goal' AND h.disposition='main'
		JOIN blackboard_nodes n ON n.project_id=h.project_id AND n.id=h.node_id
		JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version
		WHERE e.project_id=? AND e.edge_type='part_of' AND e.from_node_id=? AND e.state='active'
		ORDER BY n.original_stable_key,h.node_id`, projectID, objectiveID)
	if err != nil {
		return nil, 0, fmt.Errorf("read Objective parent Goals: %w", err)
	}
	defer rows.Close()
	parents := []FrontierGoal{}
	best := 3 // no parent Goal
	for rows.Next() {
		var g FrontierGoal
		var propsJSON string
		if err := rows.Scan(&g.ID, &g.StableKey, &propsJSON); err != nil {
			return nil, 0, err
		}
		var props map[string]any
		if err := json.Unmarshal([]byte(propsJSON), &props); err != nil {
			return nil, 0, fmt.Errorf("decode parent Goal %s: %w", g.ID, err)
		}
		g.TaskStatus, _ = props["task_status"].(string)
		rank := goalTaskStatusRank(g.TaskStatus)
		if len(parents) == 0 || rank < best {
			best = rank
		}
		parents = append(parents, g)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return parents, best, nil
}

func goalTaskStatusRank(status string) int {
	switch status {
	case "running":
		return 0
	case "paused":
		return 1
	case "pending":
		return 2
	case "completed":
		return 4
	case "failed":
		return 5
	case "stopped":
		return 6
	case "interrupted":
		return 7
	default:
		return 8
	}
}

func (s *GraphService) frontierBlockingReasons(ctx context.Context, projectID, objectiveID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.edge_type,
		       CASE WHEN e.edge_type='depends_on' THEN e.to_node_id ELSE e.from_node_id END AS prerequisite_id,
		       h.disposition,h.node_type,v.properties_json
		FROM blackboard_edge_heads e
		LEFT JOIN blackboard_node_heads h ON h.project_id=e.project_id AND h.node_id=CASE WHEN e.edge_type='depends_on' THEN e.to_node_id ELSE e.from_node_id END
		LEFT JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version
		WHERE e.project_id=? AND e.state='active'
		  AND ((e.edge_type='depends_on' AND e.from_node_id=?) OR (e.edge_type='blocks' AND e.to_node_id=?))
		ORDER BY e.edge_type,prerequisite_id`, projectID, objectiveID, objectiveID)
	if err != nil {
		return nil, fmt.Errorf("read Objective prerequisites: %w", err)
	}
	defer rows.Close()
	var reasons []string
	for rows.Next() {
		var edgeType, prerequisiteID string
		var disposition, nodeType, propsJSON sql.NullString
		if err := rows.Scan(&edgeType, &prerequisiteID, &disposition, &nodeType, &propsJSON); err != nil {
			return nil, fmt.Errorf("scan Objective prerequisite: %w", err)
		}
		reason := ""
		switch {
		case !nodeType.Valid:
			reason = "missing:" + prerequisiteID
		case NodeType(nodeType.String) != NodeTypeExplorationObjective:
			reason = "corrupt_type:" + prerequisiteID
		case !disposition.Valid || Disposition(disposition.String) != DispositionMain:
			reason = disposition.String + ":" + prerequisiteID
		case !propsJSON.Valid:
			reason = "missing_properties:" + prerequisiteID
		default:
			var props map[string]any
			if err := json.Unmarshal([]byte(propsJSON.String), &props); err != nil {
				reason = "corrupt_properties:" + prerequisiteID
			} else if props["status"] != "resolved" {
				reason = edgeType + ":" + prerequisiteID
			}
		}
		if reason != "" {
			reasons = append(reasons, reason)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Objective prerequisites: %w", err)
	}
	sort.Strings(reasons)
	return compactStrings(reasons), nil
}

func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if strings.Compare(value, out[len(out)-1]) != 0 {
			out = append(out, value)
		}
	}
	return out
}
