package conclusion

import (
	"errors"
	"fmt"
	"testing"
)

func TestMapSentinelsPreservesMessageAndChain(t *testing.T) {
	engineSentinel := ErrOwnerNotFound
	ownerSentinel := errors.New("owner not found")

	original := fmt.Errorf("load continuation: %w", engineSentinel)
	mapped := MapSentinels(original, engineSentinel, ownerSentinel)

	if !errors.Is(mapped, ownerSentinel) {
		t.Fatalf("mapped error must satisfy errors.Is for the owner sentinel, got %v", mapped)
	}
	if !errors.Is(mapped, engineSentinel) {
		t.Fatalf("mapped error must keep the engine sentinel in its chain, got %v", mapped)
	}
	if mapped.Error() != original.Error() {
		t.Fatalf("mapped message changed: %q != %q", mapped.Error(), original.Error())
	}
}

func TestMapSentinelsLeavesUnrelatedErrorsAlone(t *testing.T) {
	unrelated := fmt.Errorf("ordinary failure: %w", ErrOwnerNotFound)
	same := MapSentinels(unrelated, ErrInvalidBlackboardConclusionReceipt, ErrOwnerNotFound)
	if same != unrelated {
		t.Fatalf("unrelated error must pass through unchanged, got %v", same)
	}
	if MapSentinels(nil, ErrOwnerNotFound, ErrOwnerNotFound) != nil {
		t.Fatalf("nil must stay nil")
	}
}
