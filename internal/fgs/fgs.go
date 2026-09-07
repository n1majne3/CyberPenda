// Package fgs stores accepted Goal, Step, and Fact updates and delivery receipts.
// Callers supply a server-derived Owner contract, never a Runtime-selected owner.
package fgs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"pentest/internal/owner"
	"pentest/internal/store"
)

const Schema = "fgs-update/v1"
const MaxUpdateSize = 1 << 20

var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]{0,159}$`)

type Update struct {
	Schema           string      `json:"schema"`
	ID               string      `json:"id"`
	Sequence         int         `json:"sequence"`
	Operations       []Operation `json:"operations"`
	Resolves         *Identity   `json:"resolves,omitempty"`
	WithdrawalReason string      `json:"withdrawal_reason,omitempty"`
}

// Identity is relative to the trusted Owner supplied to the service.
type Identity struct {
	ContinuationID string `json:"continuation_id"`
	IntentID       string `json:"intent_id"`
}

var ErrBlocked = errors.New("FGS updates are blocked by a rejected update")

type Operation struct {
	Priority        string            `json:"priority,omitempty"`
	DataRefs        []string          `json:"data_refs,omitempty"`
	Op              string            `json:"op"`
	Key             string            `json:"key"`
	Title           string            `json:"title,omitempty"`
	SuccessCriteria string            `json:"success_criteria,omitempty"`
	Goal            string            `json:"goal,omitempty"`
	Action          string            `json:"action,omitempty"`
	Step            string            `json:"step,omitempty"`
	Summary         string            `json:"summary,omitempty"`
	Body            string            `json:"body,omitempty"`
	From            string            `json:"from,omitempty"`
	To              string            `json:"to,omitempty"`
	Outputs         []string          `json:"outputs,omitempty"`
	Facts           []string          `json:"facts,omitempty"`
	Inputs          []string          `json:"inputs,omitempty"`
	Executor        string            `json:"executor,omitempty"`
	Reason          string            `json:"reason,omitempty"`
	Expected        map[string]string `json:"expected,omitempty"`
	ParentGoal      string            `json:"parent_goal,omitempty"`
	After           []string          `json:"after,omitempty"`
	Corrects        string            `json:"corrects,omitempty"`
}

type Node struct {
	Priority        string     `json:"priority,omitempty"`
	DataRefs        []string   `json:"data_refs,omitempty"`
	AcceptedAt      string     `json:"accepted_at"`
	Key             string     `json:"key"`
	Type            string     `json:"type"`
	Version         int        `json:"version"`
	State           string     `json:"state,omitempty"`
	Title           string     `json:"title,omitempty"`
	SuccessCriteria string     `json:"success_criteria,omitempty"`
	Goal            string     `json:"goal,omitempty"`
	Action          string     `json:"action,omitempty"`
	Step            string     `json:"step,omitempty"`
	Summary         string     `json:"summary,omitempty"`
	Body            string     `json:"body,omitempty"`
	Outputs         []string   `json:"outputs,omitempty"`
	Facts           []string   `json:"facts,omitempty"`
	Inputs          []string   `json:"inputs,omitempty"`
	Executor        string     `json:"executor,omitempty"`
	Reason          string     `json:"reason,omitempty"`
	OwnerKind       owner.Kind `json:"owner_kind"`
	OwnerID         string     `json:"owner_id"`
	ContinuationID  string     `json:"continuation_id"`
	ParentGoal      string     `json:"parent_goal,omitempty"`
	After           []string   `json:"after,omitempty"`
	Corrects        string     `json:"corrects,omitempty"`
}

type Edge struct {
	From     string `json:"from"`
	Relation string `json:"relation"`
	To       string `json:"to"`
}

type Graph struct {
	NextCursor string `json:"next_cursor,omitempty"`
	Revision   int    `json:"revision"`
	Nodes      []Node `json:"nodes"`
	Edges      []Edge `json:"edges"`
}

type Receipt struct {
	ID             string `json:"id"`
	ContinuationID string `json:"continuation_id"`
	State          string `json:"state"`
	Revision       int    `json:"revision"`
	Code           string `json:"code,omitempty"`
	Message        string `json:"message,omitempty"`
	Operation      int    `json:"operation,omitempty"`
	UpdatedAt      string `json:"updated_at"`
}

type Service struct {
	db     *store.DB
	scanMu sync.Mutex
	scans  map[string]*mailboxScan
}

func NewService(db *store.DB) *Service { return &Service{db: db} }

func boardIdentity(c owner.Contract) (string, string, error) {
	if err := c.Validate(); err != nil {
		return "", "", err
	}
	if c.IsTask() {
		return "project", c.ProjectID, nil
	}
	return "session", c.SessionID, nil
}

func (s *Service) Apply(ctx context.Context, c owner.Contract, continuation string, u Update) (Receipt, error) {
	kind, id, err := boardIdentity(c)
	if err != nil {
		return Receipt{}, err
	}
	if continuation == "" || u.Schema != Schema || u.Sequence < 1 || u.ID != fmt.Sprintf("intent_%08d", u.Sequence) {
		return Receipt{}, errors.New("invalid FGS envelope")
	}
	raw, err := json.Marshal(u)
	if err != nil {
		return Receipt{}, err
	}
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.Rollback()
	// Acquire the SQLite writer before reading versions or checking replay.
	if _, err = tx.ExecContext(ctx, `INSERT INTO fgs_boards(kind,owner_id) VALUES(?,?) ON CONFLICT(kind,owner_id) DO UPDATE SET revision=revision`, kind, id); err != nil {
		return Receipt{}, err
	}
	var oldHash, oldReceipt string
	err = tx.QueryRowContext(ctx, `SELECT request_hash,receipt_json FROM fgs_receipts WHERE owner_kind=? AND owner_id=? AND continuation_id=? AND intent_id=?`, c.Kind, c.ID, continuation, u.ID).Scan(&oldHash, &oldReceipt)
	if err == nil {
		if hash != oldHash {
			return Receipt{}, errors.New("FGS intent identity was reused with different content")
		}
		var r Receipt
		err = json.Unmarshal([]byte(oldReceipt), &r)
		return r, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, err
	}
	var blockerRaw string
	blockerErr := tx.QueryRowContext(ctx, `SELECT receipt_json FROM fgs_receipts WHERE owner_kind=? AND owner_id=? AND json_extract(receipt_json,'$.state')='action_required' AND json_extract(payload_json,'$.resolves') IS NULL ORDER BY rowid LIMIT 1`, c.Kind, c.ID).Scan(&blockerRaw)
	var blocker Receipt
	var resolutionError string
	if blockerErr == nil {
		if err = json.Unmarshal([]byte(blockerRaw), &blocker); err != nil {
			return Receipt{}, err
		}
		if u.Resolves == nil {
			return Receipt{}, ErrBlocked
		}
		if u.Resolves.ContinuationID != blocker.ContinuationID || u.Resolves.IntentID != blocker.ID {
			resolutionError = fmt.Sprintf("Repair must target the original rejected update %s/%s, not a failed repair", blocker.ContinuationID, blocker.ID)
		}
	} else if !errors.Is(blockerErr, sql.ErrNoRows) {
		return Receipt{}, blockerErr
	} else if u.Resolves != nil {
		resolutionError = "Resolution target is not the current rejected update; read accepted state and status before publishing a fresh update"
	}
	if u.Resolves == nil {
		// A repair on a later scan page cannot let newer ordinary updates
		// overtake earlier files that are still waiting for their first receipt.
		var pending bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS (
            SELECT 1 FROM fgs_outbox_inventory i
            WHERE i.owner_kind=? AND i.owner_id=? AND i.continuation_id=? AND i.name<?
            AND NOT EXISTS (SELECT 1 FROM fgs_receipts r
                WHERE r.owner_kind=i.owner_kind AND r.owner_id=i.owner_id
                AND r.continuation_id=i.continuation_id
                AND r.intent_id=substr(i.name,1,length(i.name)-5)))`, c.Kind, c.ID, continuation, u.ID+".json").Scan(&pending)
		if err != nil {
			return Receipt{}, err
		}
		if pending {
			return Receipt{}, ErrBlocked
		}
	}
	graph, err := readGraph(ctx, tx, kind, id)
	if err != nil {
		return Receipt{}, err
	}
	nodes := map[string]Node{}
	for _, n := range graph.Nodes {
		nodes[n.Key] = n
	}
	r := Receipt{ID: u.ID, ContinuationID: continuation, State: "applied", Revision: graph.Revision + 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	withdrawal := u.Resolves != nil && strings.TrimSpace(u.WithdrawalReason) != "" && len(u.Operations) == 0
	if (len(u.Operations) == 0 && !withdrawal) || len(u.Operations) > 100 || len(raw) > MaxUpdateSize || (u.WithdrawalReason != "" && !withdrawal) {
		r.State = "action_required"
		r.Code = "invalid_update"
		r.Message = "update requires 1 to 100 operations and at most 1 MiB"
		r.Revision = graph.Revision
	}
	if resolutionError != "" {
		r.State = "action_required"
		r.Code = "invalid_resolution"
		r.Message = resolutionError
		r.Revision = graph.Revision
	}
	changed := []Node{}
	if withdrawal {
		r.Revision = graph.Revision
	}
	for i, op := range u.Operations {
		if r.State != "applied" {
			break
		}
		n, e := applyOperation(nodes, op)
		if e != nil {
			r.State = "action_required"
			r.Code = "invalid_operation"
			r.Message = e.Error()
			r.Operation = i
			r.Revision = graph.Revision
			break
		}
		n.Version++
		n.AcceptedAt = r.UpdatedAt
		n.OwnerKind = c.Kind
		n.OwnerID = c.ID
		n.ContinuationID = continuation
		nodes[n.Key] = n
		changed = append(changed, n)
	}
	if r.State == "applied" {
		if e := validateGraph(nodes); e != nil {
			r.State = "action_required"
			r.Code = "invalid_graph"
			r.Message = e.Error()
			r.Revision = graph.Revision
		}
	}
	if r.State == "applied" {
		for _, n := range changed {
			body, e := json.Marshal(n)
			if e != nil {
				return Receipt{}, e
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO fgs_nodes VALUES(?,?,?,?) ON CONFLICT(board_kind,board_id,node_key) DO UPDATE SET body_json=excluded.body_json`, kind, id, n.Key, string(body)); err != nil {
				return Receipt{}, err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO fgs_history VALUES(?,?,?,?,?)`, kind, id, n.Key, n.Version, string(body)); err != nil {
				return Receipt{}, err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE fgs_boards SET revision=? WHERE kind=? AND owner_id=?`, r.Revision, kind, id); err != nil {
			return Receipt{}, err
		}
		if u.Resolves != nil {
			blocker.State = "superseded"
			blocker.UpdatedAt = r.UpdatedAt
			encoded, e := json.Marshal(blocker)
			if e != nil {
				return Receipt{}, e
			}
			if _, err = tx.ExecContext(ctx, `UPDATE fgs_receipts SET receipt_json=? WHERE owner_kind=? AND owner_id=? AND continuation_id=? AND intent_id=?`, string(encoded), c.Kind, c.ID, blocker.ContinuationID, blocker.ID); err != nil {
				return Receipt{}, err
			}
		}
	}
	if r.State == "action_required" {
		r.Message += ". No operations from this update were applied. Read accepted state and resend the complete corrected batch."
	}
	receiptRaw, err := json.Marshal(r)
	if err != nil {
		return Receipt{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO fgs_receipts VALUES(?,?,?,?,?,?,?,?)`, c.Kind, c.ID, continuation, u.ID, u.Sequence, hash, string(raw), string(receiptRaw)); err != nil {
		return Receipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return Receipt{}, err
	}
	return r, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readGraph(ctx context.Context, q queryer, kind, id string) (Graph, error) {
	return readGraphWindow(ctx, q, kind, id, "", 0)
}

func readGraphWindow(ctx context.Context, q queryer, kind, id, cursor string, limit int) (Graph, error) {
	g := Graph{Nodes: []Node{}, Edges: []Edge{}}
	err := q.QueryRowContext(ctx, `SELECT revision FROM fgs_boards WHERE kind=? AND owner_id=?`, kind, id).Scan(&g.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return g, nil
	}
	if err != nil {
		return g, err
	}
	query := `SELECT body_json FROM fgs_nodes WHERE board_kind=? AND board_id=? AND node_key>? ORDER BY node_key`
	args := []any{kind, id, cursor}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit+1)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return g, err
	}
	defer rows.Close()
	bytesRead := 0
	for rows.Next() {
		var body string
		if err = rows.Scan(&body); err != nil {
			return g, err
		}
		if limit > 0 && (len(g.Nodes) >= limit || (len(g.Nodes) > 0 && bytesRead+len(body) > 2<<20)) {
			g.NextCursor = g.Nodes[len(g.Nodes)-1].Key
			break
		}
		bytesRead += len(body)
		var n Node
		if err = json.Unmarshal([]byte(body), &n); err != nil {
			return g, err
		}
		g.Nodes = append(g.Nodes, n)
		if n.Goal != "" {
			g.Edges = append(g.Edges, Edge{n.Key, "toward", n.Goal})
		}
		if n.Step != "" {
			g.Edges = append(g.Edges, Edge{n.Step, "produces", n.Key})
		}
		for _, f := range n.Facts {
			g.Edges = append(g.Edges, Edge{f, "satisfies", n.Key})
		}
		for _, f := range n.Inputs {
			g.Edges = append(g.Edges, Edge{n.Key, "uses", f})
		}
		if n.ParentGoal != "" {
			g.Edges = append(g.Edges, Edge{n.Key, "part_of", n.ParentGoal})
		}
		if n.Corrects != "" {
			g.Edges = append(g.Edges, Edge{n.Key, "corrects", n.Corrects})
		}
		for _, step := range n.After {
			g.Edges = append(g.Edges, Edge{n.Key, "depends_on", step})
		}
	}
	sort.Slice(g.Edges, func(i, j int) bool {
		a, b := g.Edges[i], g.Edges[j]
		return a.From+"|"+a.Relation+"|"+a.To < b.From+"|"+b.Relation+"|"+b.To
	})
	return g, rows.Err()
}

func (s *Service) Read(ctx context.Context, c owner.Contract) (Graph, error) {
	kind, id, err := boardIdentity(c)
	if err != nil {
		return Graph{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Graph{}, err
	}
	defer tx.Rollback()
	return readGraph(ctx, tx, kind, id)
}

func (s *Service) ReadPage(ctx context.Context, c owner.Contract, cursor string, limit int) (Graph, error) {
	if limit < 1 || limit > 200 {
		return Graph{}, errors.New("FGS page limit must be 1 to 200")
	}
	kind, id, err := boardIdentity(c)
	if err != nil {
		return Graph{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Graph{}, err
	}
	defer tx.Rollback()
	return readGraphWindow(ctx, tx, kind, id, cursor, limit)
}

func (s *Service) Receipt(ctx context.Context, c owner.Contract, continuation, id string) (Receipt, error) {
	if err := c.Validate(); err != nil {
		return Receipt{}, err
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT receipt_json FROM fgs_receipts WHERE owner_kind=? AND owner_id=? AND continuation_id=? AND intent_id=?`, c.Kind, c.ID, continuation, id).Scan(&raw)
	if err != nil {
		return Receipt{}, err
	}
	var r Receipt
	err = json.Unmarshal([]byte(raw), &r)
	return r, err
}

func applyOperation(nodes map[string]Node, op Operation) (Node, error) {
	n, exists := nodes[op.Key]
	if err := validateOperation(op); err != nil {
		return n, err
	}
	switch op.Op {
	case "goal.create":
		if exists || op.Key == "" || op.Title == "" || op.SuccessCriteria == "" {
			return n, errors.New("Goal requires a new key, title, and success criteria")
		}
		if op.ParentGoal != "" && nodes[op.ParentGoal].Type != "goal" {
			return n, errors.New("parent Goal does not exist")
		}
		return Node{Key: op.Key, Type: "goal", Title: op.Title, SuccessCriteria: op.SuccessCriteria, State: "open", ParentGoal: op.ParentGoal}, nil
	case "step.create":
		if exists || op.Key == "" || op.Action == "" || nodes[op.Goal].Type != "goal" {
			return n, errors.New("Step requires a new key, action, and Goal")
		}
		for _, f := range op.Inputs {
			if nodes[f].Type != "fact" {
				return n, errors.New("Step input must be an existing Fact")
			}
		}
		for _, step := range op.After {
			if nodes[step].Type != "step" {
				return n, errors.New("Step dependency does not exist")
			}
		}
		return Node{Key: op.Key, Type: "step", Goal: op.Goal, Action: op.Action, State: "open", Inputs: op.Inputs, After: op.After, Priority: op.Priority, Executor: op.Executor}, nil
	case "fact.append":
		if exists || op.Key == "" || op.Summary == "" || nodes[op.Step].Type != "step" {
			return n, errors.New("Fact requires a new key, summary, and producing Step")
		}
		if op.Corrects != "" && nodes[op.Corrects].Type != "fact" {
			return n, errors.New("corrected Fact does not exist")
		}
		return Node{Key: op.Key, Type: "fact", Step: op.Step, Summary: op.Summary, Body: op.Body, Corrects: op.Corrects, DataRefs: op.DataRefs}, nil
	case "step.transition":
		if n.Type != "step" || n.State != op.From || !stepTransition(op.From, op.To) {
			return n, errors.New("invalid Step state transition; retry a terminal Step with a new Step")
		}
		if (op.To == "blocked" || op.To == "cancelled") && op.Reason == "" {
			return n, errors.New("Step state requires a reason")
		}
		if op.To == "done" && len(op.Outputs) == 0 {
			return n, errors.New("Step completion requires current state and output Facts")
		}
		for _, f := range op.Outputs {
			if nodes[f].Type != "fact" || nodes[f].Step != n.Key {
				return n, errors.New("output Fact belongs to another Step")
			}
		}
		n.State = op.To
		n.Outputs = op.Outputs
		n.Reason = op.Reason
		if op.Executor != "" {
			n.Executor = op.Executor
		}
		return n, nil
	case "goal.transition":
		if n.Type != "goal" || n.State != op.From {
			return n, errors.New("Goal state does not match")
		}
		if (op.From == "done" || op.From == "abandoned") && op.To == "open" && op.Reason != "" {
			n.State = "open"
			n.Reason = op.Reason
			n.Facts = nil
			n.Summary = ""
			return n, nil
		}
		if op.From != "open" && op.From != "active" {
			return n, errors.New("invalid Goal transition")
		}
		if op.From == "open" && op.To == "active" {
			n.State = "active"
			return n, nil
		}
		if op.To == "abandoned" && op.Reason != "" {
			n.State = op.To
			n.Reason = op.Reason
			return n, nil
		}
		if op.To != "done" || len(op.Facts) == 0 || op.Summary == "" {
			return n, errors.New("Goal completion requires current state, Facts, and summary")
		}
		for _, f := range op.Facts {
			if nodes[f].Type != "fact" || !underGoal(nodes, nodes[nodes[f].Step].Goal, n.Key) {
				return n, errors.New("completion Fact belongs to another Goal")
			}
		}
		for _, step := range nodes {
			if step.Goal == n.Key && step.State != "done" && step.State != "cancelled" {
				return n, errors.New("Goal has unfinished Steps")
			}
		}
		n.State = op.To
		n.Facts = op.Facts
		n.Summary = op.Summary
		return n, nil
	case "goal.describe", "step.describe":
		if (op.Op == "goal.describe" && n.Type != "goal") || (op.Op == "step.describe" && n.Type != "step") {
			return n, errors.New("description node type does not match")
		}
		fields := map[string]*string{}
		if n.Type == "goal" {
			fields["title"] = &n.Title
			fields["success_criteria"] = &n.SuccessCriteria
		} else {
			fields["action"] = &n.Action
			fields["priority"] = &n.Priority
		}
		updates := map[string]string{"title": op.Title, "success_criteria": op.SuccessCriteria, "action": op.Action, "priority": op.Priority}
		count := 0
		for name, value := range updates {
			if value == "" {
				continue
			}
			target, ok := fields[name]
			old, has := op.Expected[name]
			if !ok || !has || *target != old {
				return n, errors.New("description has a stale or invalid expected value")
			}
			if name == "success_criteria" && n.State == "done" {
				return n, errors.New("reopen Goal before changing completion criteria")
			}
			*target = value
			count++
		}
		if count == 0 || len(op.Expected) != count {
			return n, errors.New("description requires exactly the changed fields and their expected values")
		}
		return n, nil
	default:
		return n, fmt.Errorf("unsupported FGS operation %q", op.Op)
	}
}

func validateOperation(op Operation) error {
	if op.Op == "transport.invalid" {
		return errors.New("malformed FGS envelope; publish a corrected update or withdraw this identity")
	}
	if op.Priority != "" && op.Priority != "low" && op.Priority != "normal" && op.Priority != "high" {
		return errors.New("priority must be low, normal, or high")
	}
	if len(op.DataRefs) > 100 {
		return errors.New("Fact has too many data references")
	}
	for _, ref := range op.DataRefs {
		if strings.TrimSpace(ref) == "" || len(ref) > 2048 {
			return errors.New("invalid Fact data reference")
		}
	}

	if !keyPattern.MatchString(op.Key) {
		return errors.New("invalid Blackboard key")
	}
	fields := map[string]string{
		"goal.create":     "title success_criteria parent_goal",
		"goal.transition": "from to facts summary reason",
		"goal.describe":   "title success_criteria expected",
		"step.create":     "goal action inputs after priority executor",
		"step.transition": "from to outputs reason executor",
		"step.describe":   "action priority expected",
		"fact.append":     "step summary body corrects data_refs",
	}
	allowed, ok := fields[op.Op]
	if !ok {
		return fmt.Errorf("unsupported FGS operation %q", op.Op)
	}
	raw, _ := json.Marshal(op)
	var values map[string]json.RawMessage
	_ = json.Unmarshal(raw, &values)
	for k, v := range values {
		if k != "op" && k != "key" && !strings.Contains(" "+allowed+" ", " "+k+" ") {
			return fmt.Errorf("field %s is not allowed for %s", k, op.Op)
		}
		var text string
		if json.Unmarshal(v, &text) == nil && strings.TrimSpace(text) == "" {
			return fmt.Errorf("field %s must not be blank", k)
		}
	}
	if len(op.Body) > 64*1024 {
		return errors.New("Fact body exceeds 64 KiB")
	}
	for _, keys := range [][]string{op.Inputs, op.Outputs, op.Facts, op.After} {
		seen := map[string]bool{}
		for _, k := range keys {
			if !keyPattern.MatchString(k) || seen[k] {
				return errors.New("invalid or duplicate node reference")
			}
			seen[k] = true
		}
	}
	return nil
}

func underGoal(nodes map[string]Node, key, ancestor string) bool {
	for key != "" {
		if key == ancestor {
			return true
		}
		key = nodes[key].ParentGoal
	}
	return false
}

func validateGraph(nodes map[string]Node) error {
	for _, n := range nodes {
		if n.Type != "goal" || n.State != "done" {
			continue
		}
		for _, child := range nodes {
			if child.Type == "goal" && child.Key != n.Key && underGoal(nodes, child.Key, n.Key) && child.State != "done" && child.State != "abandoned" {
				return errors.New("done Goal has an unfinished child Goal")
			}
			if child.Type == "step" && underGoal(nodes, child.Goal, n.Key) && child.State != "done" && child.State != "cancelled" {
				return errors.New("done Goal has unfinished Steps")
			}
		}
	}
	return nil
}

func stepTransition(from, to string) bool {
	switch from {
	case "open":
		return to == "running" || to == "blocked" || to == "done" || to == "cancelled"
	case "running":
		return to == "blocked" || to == "done" || to == "cancelled"
	case "blocked":
		return to == "open" || to == "done" || to == "cancelled"
	}
	return false
}

func (s *Service) History(ctx context.Context, c owner.Contract, key string) ([]Node, error) {
	return s.historyWindow(ctx, c, key, 0, 0)
}
func (s *Service) HistoryPage(ctx context.Context, c owner.Contract, key string, before, limit int) ([]Node, error) {
	if before < 0 || limit < 1 || limit > 100 {
		return nil, errors.New("invalid FGS history page")
	}
	return s.historyWindow(ctx, c, key, before, limit)
}
func (s *Service) historyWindow(ctx context.Context, c owner.Contract, key string, before, limit int) ([]Node, error) {
	kind, id, err := boardIdentity(c)
	if err != nil {
		return nil, err
	}
	query := `SELECT body_json FROM fgs_history WHERE board_kind=? AND board_id=? AND node_key=?`
	args := []any{kind, id, key}
	if before > 0 {
		query += " AND version<?"
		args = append(args, before)
	}
	query += " ORDER BY version"
	if limit > 0 {
		query += " DESC LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Node{}
	bytesRead := 0
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if limit > 0 && len(result) > 0 && bytesRead+len(raw) > 2<<20 {
			break
		}
		bytesRead += len(raw)
		var n Node
		if err = json.Unmarshal([]byte(raw), &n); err != nil {
			return nil, err
		}
		result = append(result, n)
	}
	return result, rows.Err()
}
