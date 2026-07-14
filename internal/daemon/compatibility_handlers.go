package daemon

import (
	"encoding/json"
	"errors"
	"net/http"

	"pentest/internal/blackboard"
	"pentest/internal/blackboardcompat"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
)

const compatibilityActorID = "local-operator"

func setCompatibilityHeaders(response http.ResponseWriter) {
	response.Header().Set("Deprecation", "true")
	response.Header().Set("Link", `<https://github.com/n1majne3/CyberPenda/blob/main/docs/blackboard-graph-migration.md>; rel="deprecation"`)
	response.Header().Set("CyberPenda-Compatibility", "legacy_blackboard_v1")
}

func (server *Server) requireCompatibilityProject(response http.ResponseWriter, request *http.Request, projectID string) bool {
	if server.compatibility == nil {
		return server.requireProject(response, projectID)
	}
	setCompatibilityHeaders(response)
	if _, err := server.projects.Get(projectID); err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeCompatibilityError(response, projectinterface.ValidationError(projectinterface.ErrCodeProjectNotFound, "Project not found", "project_id"))
		} else {
			writeCompatibilityError(response, projectinterface.InternalError("load compatibility Project: "+err.Error()))
		}
		return false
	}
	return true
}

func (server *Server) decodeCompatibilityJSON(response http.ResponseWriter, request *http.Request, target any) bool {
	if err := json.NewDecoder(request.Body).Decode(target); err != nil {
		if server.compatibility != nil {
			setCompatibilityHeaders(response)
			writeCompatibilityError(response, projectinterface.ValidationError(projectinterface.ErrCodeInvalidRequest, "invalid JSON body", "body"))
		} else {
			writeError(response, http.StatusBadRequest, "invalid JSON body")
		}
		return false
	}
	return true
}

func compatibilityPrincipal(projectID string) (projectinterface.Principal, error) {
	return projectinterface.OperatorPrincipal(projectID, compatibilityActorID)
}

func (server *Server) requestCompatibilityPrincipal(request *http.Request, projectID string) (projectinterface.Principal, error) {
	token := projectinterface.BearerToken(request)
	if token != "" && token != server.authToken {
		return server.projectInterface.Authenticate(request.Context(), token, projectID)
	}
	return compatibilityPrincipal(projectID)
}

func writeCompatibilityError(response http.ResponseWriter, err error) {
	interfaceErr := projectinterface.AsError(err)
	var validation *blackboard.ValidationError
	if errors.As(err, &validation) {
		interfaceErr = &projectinterface.Error{
			ProtocolVersion: projectinterface.RuntimeProtocolVersion,
			Code:            validation.Code, Message: validation.Message, Path: validation.Path,
			Retryable: false, Details: validation.Details,
		}
	}
	if interfaceErr == nil {
		interfaceErr = projectinterface.InternalError(err.Error())
	}
	status := projectinterface.HTTPStatusForError(interfaceErr)
	if interfaceErr.Code == blackboardcompat.ErrCodeCompatibilityRemoved {
		status = http.StatusGone
	}
	if interfaceErr.Code == blackboardcompat.ErrCodeLegacyRelationNotGraphRepresentable ||
		interfaceErr.Code == blackboardcompat.ErrCodeCompatibilityAttemptRequired ||
		interfaceErr.Code == blackboard.ErrCodeProjectKindMismatch ||
		interfaceErr.Code == blackboard.ErrCodeProjectionTooLarge {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(response, status, struct {
		Error *projectinterface.Error `json:"error"`
	}{Error: interfaceErr})
}
