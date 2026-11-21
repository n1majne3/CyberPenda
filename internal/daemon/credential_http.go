package daemon

import (
	"net/http"

	"pentest/internal/credential"
)

// credentialBindingInput decodes the shared shape used by both global and
// project-scoped PUT handlers.
type credentialBindingInput struct {
	CredentialRef string            `json:"credential_ref"`
	Source        credential.Source `json:"source"`
	Disabled      bool              `json:"disabled"`
}

// writeCredentialError maps credential service errors to HTTP statuses. Today
// every documented service error is a client/validation problem, so all map to
// 400. The helper exists so later non-validation errors can be distinguished
// without touching every handler.
func writeCredentialError(response http.ResponseWriter, err error) {
	writeError(response, http.StatusBadRequest, err.Error())
}
