package owner

import "testing"

func TestTaskAndSessionContractsKeepCapabilitiesSeparate(t *testing.T) {
	task := NewTaskContract("task-1", "project-1", "/runs/task-1/workdir")
	if err := task.Validate(); err != nil {
		t.Fatalf("validate Task contract: %v", err)
	}
	if !task.IsTask() || task.IsSession() || !task.Capabilities.ProjectScope || !task.Capabilities.ProjectBlackboard || task.Capabilities.SessionBlackboard {
		t.Fatalf("Task contract capabilities = %#v", task)
	}

	session := NewSessionContract("session-1", "/runs/sessions/session-1")
	if err := session.Validate(); err != nil {
		t.Fatalf("validate Session contract: %v", err)
	}
	if !session.IsSession() || session.IsTask() || session.ProjectID != "" || session.TaskID != "" || !session.Capabilities.SessionBlackboard || session.Capabilities.ProjectScope || session.Capabilities.ProjectArtifacts || session.Capabilities.ProjectBlackboard {
		t.Fatalf("Session contract capabilities = %#v", session)
	}

	foreignProject := session
	foreignProject.ProjectID = "project-1"
	if err := foreignProject.Validate(); err == nil {
		t.Fatal("expected Session contract with Project identity to be rejected")
	}

	missingTaskCapability := task
	missingTaskCapability.Capabilities.ProjectArtifacts = false
	if err := missingTaskCapability.Validate(); err == nil {
		t.Fatal("expected Task contract with missing Project capability to be rejected")
	}

	wrongTaskIdentity := task
	wrongTaskIdentity.TaskID = "another-task"
	if err := wrongTaskIdentity.Validate(); err == nil {
		t.Fatal("expected Task contract with mismatched identity to be rejected")
	}
}
