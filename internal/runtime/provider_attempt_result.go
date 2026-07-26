package runtime

import "pentest/internal/blackboardconclusion"

// ProviderSessionAttemptResult is one strictly validated semantic result from
// a correlated Harness control Turn. Unvalidated provider bytes have no
// representation on this seam.
type ProviderSessionAttemptResult struct {
	RequestID      string
	SessionID      string
	ProviderTurnID string
	Validated      blackboardconclusion.ValidatedResult
}

// ProviderSessionAttemptResultSink receives a validated result. Implementors
// invoke it without holding provider-session locks.
type ProviderSessionAttemptResultSink func(ProviderSessionAttemptResult)

// ProviderSessionAttemptResultSource is implemented by sessions that can
// extract the closed runtime-attempt-result/v1 contract from provider-native
// control output.
type ProviderSessionAttemptResultSource interface {
	SetAttemptResultSink(ProviderSessionAttemptResultSink)
}
