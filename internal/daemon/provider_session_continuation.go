package daemon

import (
	"pentest/internal/owner"
	"pentest/internal/session"
	"pentest/internal/task"
)

func ownerContinuationFromTask(continuation task.TaskContinuation) owner.Continuation {
	return owner.Continuation{
		ID: continuation.ID, OwnerID: continuation.TaskID, Number: continuation.Number,
		RuntimeProfileID: continuation.RuntimeProfileID, RuntimeProvider: continuation.RuntimeProvider,
		Runner: string(continuation.Runner), Status: string(continuation.Status),
		ContainerID: continuation.ContainerID, NativeSessionID: continuation.NativeSessionID,
		NativeSessionPath: continuation.NativeSessionPath, RuntimeConfigVersionID: continuation.RuntimeConfigVersionID,
		StartedAt: continuation.StartedAt, UpdatedAt: continuation.UpdatedAt, EndedAt: continuation.EndedAt,
	}
}

func ownerContinuationFromSession(continuation session.Continuation) owner.Continuation {
	return owner.Continuation{
		ID: continuation.ID, OwnerID: continuation.SessionID, Number: continuation.Number,
		RuntimeProfileID: continuation.RuntimeProfileID, RuntimeProvider: continuation.RuntimeProvider,
		Runner: string(continuation.Runner), Status: string(continuation.Status),
		ContainerID: continuation.ContainerID, NativeSessionID: continuation.NativeSessionID,
		NativeSessionPath: continuation.NativeSessionPath, RuntimeConfigVersionID: continuation.RuntimeConfigID,
		StartedAt: continuation.StartedAt, UpdatedAt: continuation.UpdatedAt, EndedAt: continuation.EndedAt,
	}
}
