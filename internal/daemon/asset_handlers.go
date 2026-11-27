package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"pentest/internal/asset"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
)

func (server *Server) registerAssetV2Routes() {
	server.mux.HandleFunc("GET /api/v2/projects/{id}/assets", server.handleListAssetsV2)
	server.mux.HandleFunc("GET /api/v2/projects/{id}/assets/coverage", server.handleAssetCoverageV2)
	server.mux.HandleFunc("POST /api/v2/projects/{id}/assets/upsert", server.handleUpsertAssetsV2)
	server.mux.HandleFunc("POST /api/v2/projects/{id}/assets/{asset_id}/anchors", server.handleAnchorAssetV2)
}

type assetBatchRequest struct {
	Assets []asset.Input `json:"assets"`
}

func decodeAssetBatch(response http.ResponseWriter, request *http.Request) ([]asset.Input, bool) {
	var input assetBatchRequest
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 1<<20)).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return nil, false
	}
	return input.Assets, true
}

func writeAssetError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, project.ErrNotFound), errors.Is(err, asset.ErrNotFound), errors.Is(err, asset.ErrNodeNotFound):
		writeError(response, http.StatusNotFound, err.Error())
	case errors.Is(err, asset.ErrInvalidInput), errors.Is(err, asset.ErrInvalidParent), errors.Is(err, asset.ErrTrustedOrigin):
		writeError(response, http.StatusBadRequest, err.Error())
	default:
		writeError(response, http.StatusInternalServerError, "Asset operation failed")
	}
}

func assetViewsResponse(views []asset.View) struct {
	Assets []asset.View `json:"assets"`
} {
	return struct {
		Assets []asset.View `json:"assets"`
	}{Assets: views}
}

func (server *Server) handleOperatorListAssets(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}
	views, err := server.assets.List(request.Context(), projectID)
	if err != nil {
		writeAssetError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, assetViewsResponse(views))
}

func (server *Server) handleOperatorAssetCoverage(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}
	coverage, err := server.assets.Coverage(request.Context(), projectID)
	if err != nil {
		writeAssetError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, coverage)
}

func (server *Server) handleOperatorUpsertAssets(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}
	inputs, ok := decodeAssetBatch(response, request)
	if !ok {
		return
	}
	actorID := strings.TrimSpace(request.Header.Get(projectinterface.OperatorActorHeader))
	if actorID == "" {
		actorID = "local-operator"
	}
	result, err := server.assets.Upsert(request.Context(), projectID, inputs, asset.Origin{Kind: asset.OriginOperator, ActorID: actorID})
	if err != nil {
		writeAssetError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (server *Server) handleOperatorImportAssets(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}
	var input struct {
		SourceProjectID string   `json:"source_project_id"`
		AssetIDs        []string `json:"asset_ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 1<<20)).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	actorID := strings.TrimSpace(request.Header.Get(projectinterface.OperatorActorHeader))
	if actorID == "" {
		actorID = "local-operator"
	}
	result, err := server.assets.Import(request.Context(), projectID, input.SourceProjectID, input.AssetIDs, asset.Origin{Kind: asset.OriginOperator, ActorID: actorID})
	if err != nil {
		writeAssetError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (server *Server) authenticateAssetV2(response http.ResponseWriter, request *http.Request) (blackboardV2Principal, asset.Origin, bool) {
	principal, authErr := server.authenticateBlackboardV2(request, false)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return blackboardV2Principal{}, asset.Origin{}, false
	}
	if principal.operator {
		return principal, asset.Origin{Kind: asset.OriginOperator, ActorID: principal.actorID}, true
	}
	return principal, asset.Origin{Kind: asset.OriginRuntime, TaskID: principal.taskID, ContinuationID: principal.continuationID}, true
}

func requireAssetV2WriteAccess(response http.ResponseWriter, principal blackboardV2Principal) bool {
	if principal.operator || principal.grant.AllowsWrite() {
		return true
	}
	writeBlackboardV2Error(response, blackboardV2HTTPError("authority_denied", "Continuation Interface capability does not admit new asset writes", "authority"), nil)
	return false
}

func (server *Server) handleListAssetsV2(response http.ResponseWriter, request *http.Request) {
	principal, _, ok := server.authenticateAssetV2(response, request)
	if !ok {
		return
	}
	views, err := server.assets.List(request.Context(), principal.projectID)
	if err != nil {
		writeAssetError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, assetViewsResponse(views))
}

func (server *Server) handleAssetCoverageV2(response http.ResponseWriter, request *http.Request) {
	principal, _, ok := server.authenticateAssetV2(response, request)
	if !ok {
		return
	}
	coverage, err := server.assets.Coverage(request.Context(), principal.projectID)
	if err != nil {
		writeAssetError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, coverage)
}

func (server *Server) handleUpsertAssetsV2(response http.ResponseWriter, request *http.Request) {
	principal, origin, ok := server.authenticateAssetV2(response, request)
	if !ok || !requireAssetV2WriteAccess(response, principal) {
		return
	}
	inputs, valid := decodeAssetBatch(response, request)
	if !valid {
		return
	}
	result, err := server.assets.Upsert(request.Context(), principal.projectID, inputs, origin)
	if err != nil {
		writeAssetError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (server *Server) handleAnchorAssetV2(response http.ResponseWriter, request *http.Request) {
	principal, origin, ok := server.authenticateAssetV2(response, request)
	if !ok || !requireAssetV2WriteAccess(response, principal) {
		return
	}
	var input struct {
		NodeKey string `json:"node_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 1<<20)).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	anchor, err := server.assets.AnchorNode(request.Context(), principal.projectID, request.PathValue("asset_id"), input.NodeKey, origin)
	if err != nil {
		writeAssetError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, anchor)
}
