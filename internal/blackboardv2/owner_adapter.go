package blackboardv2

import (
	"context"

	"pentest/internal/owner"
)

// ApplyForOwner dispatches the shared semantic kernel through an explicit
// owner capability contract. A Session contract can never fall through to the
// Project tables, and a Task contract preserves the existing Project adapter.
func (s *Service) ApplyForOwner(ctx context.Context, contract owner.Contract, batch ChangeBatch) (ChangeResult, error) {
	if err := validateOwnerContract(contract); err != nil {
		return ChangeResult{}, err
	}
	if contract.IsSession() {
		return s.ApplyForSession(ctx, contract.SessionID, batch)
	}
	return s.Apply(ctx, contract.ProjectID, batch)
}

func (s *Service) ApplyForOwnerAtRevision(ctx context.Context, contract owner.Contract, expectedBaseRevision int, batch ChangeBatch) (ChangeResult, error) {
	if err := validateOwnerContract(contract); err != nil {
		return ChangeResult{}, err
	}
	if contract.IsSession() {
		return s.ApplyForSessionAtRevision(ctx, contract.SessionID, expectedBaseRevision, batch)
	}
	return s.apply(ctx, contract.ProjectID, "", batch, &expectedBaseRevision)
}

func (s *Service) ReadCurrentForOwner(ctx context.Context, contract owner.Contract, key string) (CurrentDetail, error) {
	if err := validateOwnerContract(contract); err != nil {
		return CurrentDetail{}, err
	}
	if contract.IsSession() {
		return s.ReadSessionCurrent(ctx, contract.SessionID, key)
	}
	return s.ReadCurrent(ctx, contract.ProjectID, key)
}

func (s *Service) ReadHistoryForOwner(ctx context.Context, contract owner.Contract, key string, options HistoryOptions) (SemanticHistory, error) {
	if err := validateOwnerContract(contract); err != nil {
		return SemanticHistory{}, err
	}
	if contract.IsSession() {
		return s.ReadSessionHistory(ctx, contract.SessionID, key, options)
	}
	return s.ReadHistory(ctx, contract.ProjectID, key, options)
}

func (s *Service) RuntimeSnapshotForOwner(ctx context.Context, contract owner.Contract) (RuntimeSnapshot, error) {
	if err := validateOwnerContract(contract); err != nil {
		return RuntimeSnapshot{}, err
	}
	if contract.IsSession() {
		return s.SessionRuntimeSnapshot(ctx, contract.SessionID)
	}
	return s.RuntimeSnapshot(ctx, contract.ProjectID)
}

func validateOwnerContract(contract owner.Contract) error {
	if err := contract.Validate(); err != nil {
		return semanticError("authority_denied", "owner capability contract is invalid", "owner", nil)
	}
	return nil
}
