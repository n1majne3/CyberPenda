package blackboardv2

import (
	"context"
	"errors"
	"testing"
)

func TestSemanticChangeKernelFailsClosedWhenOwnerLacksAnOperation(t *testing.T) {
	_, err := applySemanticChangeSet(context.Background(), nil, 0, []Change{{Op: "create"}}, "now", semanticChangeBackend{unsupported: "operation unavailable"})
	var semantic *Error
	if !errors.As(err, &semantic) || semantic.Code != "semantic_validation" {
		t.Fatalf("missing owner operation error = %v", err)
	}
}
