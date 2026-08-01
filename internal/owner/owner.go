// Package owner defines the explicit capability contract shared by owner-
// neutral Runtime and Blackboard work. Task and Non-Project Session owners do
// not masquerade as one another or rely on a nullable Project relationship.
package owner

import "errors"

// Kind identifies the durable aggregate that owns Runtime and semantic state.
type Kind string

const (
	KindTask    Kind = "task"
	KindSession Kind = "session"
)

// BlackboardKind identifies the semantic memory boundary available to an
// owner. Session Blackboard state is never Project Blackboard state.
type BlackboardKind string

const (
	BlackboardProject BlackboardKind = "project"
	BlackboardSession BlackboardKind = "session"
)

// Capabilities are the owner-specific authorities consumed by later kernels.
type Capabilities struct {
	ProjectScope      bool           `json:"project_scope"`
	ProjectArtifacts  bool           `json:"project_artifacts"`
	ProjectHistory    bool           `json:"project_history"`
	ProjectBlackboard bool           `json:"project_blackboard"`
	SessionBlackboard bool           `json:"session_blackboard"`
	Blackboard        BlackboardKind `json:"blackboard"`
}

// Contract is the server-derived owner binding. ProjectID is populated only
// for Task owners; SessionID is populated only for Non-Project Session owners.
type Contract struct {
	Kind         Kind         `json:"kind"`
	ID           string       `json:"id"`
	ProjectID    string       `json:"project_id,omitempty"`
	TaskID       string       `json:"task_id,omitempty"`
	SessionID    string       `json:"session_id,omitempty"`
	Workdir      string       `json:"-"`
	Capabilities Capabilities `json:"capabilities"`
}

// NewTaskContract creates the Project-capable owner contract for one Task.
func NewTaskContract(taskID, projectID, workdir string) Contract {
	return Contract{
		Kind: KindTask, ID: taskID, ProjectID: projectID, TaskID: taskID, Workdir: workdir,
		Capabilities: Capabilities{
			ProjectScope: true, ProjectArtifacts: true, ProjectHistory: true,
			ProjectBlackboard: true, Blackboard: BlackboardProject,
		},
	}
}

// NewSessionContract creates the deliberately Project-free Session contract.
func NewSessionContract(sessionID, workdir string) Contract {
	return Contract{
		Kind: KindSession, ID: sessionID, SessionID: sessionID, Workdir: workdir,
		Capabilities: Capabilities{SessionBlackboard: true, Blackboard: BlackboardSession},
	}
}

// Validate rejects malformed or mixed owner bindings before a later kernel can
// accidentally grant Project capabilities to a Session.
func (c Contract) Validate() error {
	if c.ID == "" || c.Kind == "" {
		return errors.New("owner contract requires kind and id")
	}
	switch c.Kind {
	case KindTask:
		if c.ID != c.TaskID || c.TaskID == "" || c.ProjectID == "" || c.SessionID != "" ||
			c.Capabilities.Blackboard != BlackboardProject ||
			!c.Capabilities.ProjectScope || !c.Capabilities.ProjectArtifacts || !c.Capabilities.ProjectHistory ||
			!c.Capabilities.ProjectBlackboard || c.Capabilities.SessionBlackboard {
			return errors.New("invalid Task owner contract")
		}
	case KindSession:
		if c.ID != c.SessionID || c.SessionID == "" || c.ProjectID != "" || c.TaskID != "" ||
			c.Capabilities.Blackboard != BlackboardSession || !c.Capabilities.SessionBlackboard ||
			c.Capabilities.ProjectScope || c.Capabilities.ProjectArtifacts || c.Capabilities.ProjectHistory || c.Capabilities.ProjectBlackboard {
			return errors.New("invalid Session owner contract")
		}
	default:
		return errors.New("unknown owner contract kind")
	}
	return nil
}

func (c Contract) IsTask() bool    { return c.Kind == KindTask }
func (c Contract) IsSession() bool { return c.Kind == KindSession }
