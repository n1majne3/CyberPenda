package runtime

import (
	"context"
	"errors"
	"strings"
)

func providerTurnSettled(metadata map[string]any) bool {
	status := strings.ToLower(providerJSONValue(metadata, "status", "turn_status", "turnStatus"))
	if status == "" {
		if turn, ok := metadata["turn"].(map[string]any); ok {
			status = strings.ToLower(providerJSONValue(turn, "status", "turn_status", "turnStatus"))
		}
	}
	switch status {
	case "aborted", "cancelled", "canceled", "completed", "failed", "interrupted", "rejected", "stopped":
		return true
	default:
		return false
	}
}

func providerSettlementKey(sessionID, turnID string) string {
	return strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(turnID)
}

func (s *providerSessionAdapter) waitForSettlement(ctx context.Context, mode ProviderSessionMode, sessionID, turnID string, baseline uint64) error {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(turnID) == "" {
		return &ProviderSessionOperationError{Mode: mode, Cause: errors.New("provider settlement correlation is incomplete")}
	}
	key := providerSettlementKey(sessionID, turnID)
	for {
		s.mu.Lock()
		if settlement, ok := s.settlements[key]; ok && settlement.seq > baseline {
			delete(s.settlements, key)
			s.mu.Unlock()
			return nil
		}
		changed := s.settlementChanged
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return &ProviderSessionOperationError{Mode: mode, Cause: ctx.Err()}
		case <-changed:
		}
	}
}
