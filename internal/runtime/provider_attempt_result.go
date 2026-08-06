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
// reasoning. Reason, FieldPath, and Expected are the bounded validation detail
// extracted from the closed-result contract.
type ProviderSessionAttemptResultValidationFailure struct {
	RequestID           string
	SessionID           string
	ProviderTurnID      string
	ValidationErrorCode ProviderSessionAttemptResultValidationErrorCode
	Reason              blackboardconclusion.ValidationReason
	FieldPath           string
	Expected            string
}

// ProviderSessionAttemptResultValidationFailureSink receives a bounded
// notification for a rejected result. Implementors invoke it without holding
// provider-session locks.
type ProviderSessionAttemptResultValidationFailureSink func(ProviderSessionAttemptResultValidationFailure)

// attemptResultValidationFailure assembles the bounded notification for one
// rejected result. A Decode error contributes its closed validation detail;
// when no error exists (provider-declared invalid or transport-oversized
// output), the caller supplies the bounded reason directly.
func attemptResultValidationFailure(requestID, sessionID, providerTurnID string, err error, reason blackboardconclusion.ValidationReason) ProviderSessionAttemptResultValidationFailure {
	failure := ProviderSessionAttemptResultValidationFailure{
		RequestID: requestID, SessionID: sessionID, ProviderTurnID: providerTurnID,
		ValidationErrorCode: ProviderSessionAttemptResultInvalid,
	}
	if err == nil {
		failure.Reason = reason
		return failure
	}
	detail := blackboardconclusion.DecodeDetailOf(err)
	failure.Reason, failure.FieldPath, failure.Expected = detail.Reason, detail.FieldPath, detail.Expected
	return failure
}

// ProviderSessionAttemptResultSource is implemented by sessions that can
// extract the closed runtime-attempt-result/v1 contract from provider-native
// control output.
type ProviderSessionAttemptResultSource interface {
	SetAttemptResultSink(ProviderSessionAttemptResultSink)
	SetAttemptResultValidationFailureSink(ProviderSessionAttemptResultValidationFailureSink)
}
