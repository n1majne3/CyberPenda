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

// ProviderSessionAttemptResultValidationErrorCode is the bounded reason a
// provider result was rejected before reaching the validated result seam.
// It deliberately does not preserve decoder detail.
type ProviderSessionAttemptResultValidationErrorCode string

const (
	// ProviderSessionAttemptResultInvalid reports that provider output did not
	// satisfy the closed semantic conclusion contract.
	ProviderSessionAttemptResultInvalid ProviderSessionAttemptResultValidationErrorCode = "semantic_conclusion_invalid_result"
)

// ProviderSessionAttemptResultValidationFailure identifies one rejected
// semantic conclusion without carrying provider bytes, decoder text, or
// reasoning.
type ProviderSessionAttemptResultValidationFailure struct {
	RequestID           string
	SessionID           string
	ProviderTurnID      string
	ValidationErrorCode ProviderSessionAttemptResultValidationErrorCode
}

// ProviderSessionAttemptResultValidationFailureSink receives a bounded
// notification for a rejected result. Implementors invoke it without holding
// provider-session locks.
type ProviderSessionAttemptResultValidationFailureSink func(ProviderSessionAttemptResultValidationFailure)

// ProviderSessionAttemptResultSource is implemented by sessions that can
// extract the closed runtime-attempt-result/v1 contract from provider-native
// control output.
type ProviderSessionAttemptResultSource interface {
	SetAttemptResultSink(ProviderSessionAttemptResultSink)
	SetAttemptResultValidationFailureSink(ProviderSessionAttemptResultValidationFailureSink)
}
