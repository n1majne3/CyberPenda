package daemon

import (
	"errors"
	"net/http"

	"pentest/internal/project"
)

func (server *Server) handleDashboard(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if projectID == "" {
		writeError(response, http.StatusNotFound, "project not found")
		return
	}

	found, err := server.projects.Get(projectID)
	if errors.Is(err, project.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load project")
		return
	}

	scope := found.Scope
	// ready means the scope declares at least one named target asset, so
	// meaningful testing can proceed.
	namedAssets := len(scope.Domains) + len(scope.IPs) + len(scope.CIDRs) + len(scope.URLs) + len(scope.Ports)
	summary := struct {
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
		Scope     struct {
			Domains          int  `json:"domains"`
			IPs              int  `json:"ips"`
			CIDRs            int  `json:"cidrs"`
			URLs             int  `json:"urls"`
			Ports            int  `json:"ports"`
			Excluded         int  `json:"excluded"`
			HasTestingLimits bool `json:"has_testing_limits"`
			HasNotes         bool `json:"has_notes"`
			Ready            bool `json:"ready"`
		} `json:"scope"`
		Counts struct {
			Goals    int `json:"goals"`
			Steps    int `json:"steps"`
			Tasks    int `json:"tasks"`
			Facts    int `json:"facts"`
			Findings int `json:"findings"`
			Evidence int `json:"evidence"`
		} `json:"counts"`
	}{
		ProjectID: found.ID,
		Name:      found.Name,
	}
	summary.Scope.Domains = len(scope.Domains)
	summary.Scope.IPs = len(scope.IPs)
	summary.Scope.CIDRs = len(scope.CIDRs)
	summary.Scope.URLs = len(scope.URLs)
	summary.Scope.Ports = len(scope.Ports)
	summary.Scope.Excluded = len(scope.Excluded)
	summary.Scope.HasTestingLimits = len(scope.TestingLimits) > 0
	summary.Scope.HasNotes = scope.Notes != ""
	summary.Scope.Ready = namedAssets > 0

	tasks, err := server.tasks.ListForProject(found.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "count tasks")
		return
	}
	var factCount, findingCount, evidenceCount int
	if found.BlackboardProtocol == "fgs" {
		err := server.db.QueryRowContext(request.Context(), `SELECT COALESCE(SUM(json_extract(body_json,'$.type')='goal'),0),COALESCE(SUM(json_extract(body_json,'$.type')='step'),0),COALESCE(SUM(json_extract(body_json,'$.type')='fact'),0) FROM fgs_nodes WHERE board_kind='project' AND board_id=?`, found.ID).Scan(&summary.Counts.Goals, &summary.Counts.Steps, &factCount)
		if err != nil {
			writeError(response, http.StatusInternalServerError, "count FGS nodes")
			return
		}
	} else if server.blackboardV2 != nil {
		projection, snapshotErr := server.blackboardV2.ProjectRuntimeSnapshot(request.Context(), found.ID)
		if snapshotErr != nil {
			writeError(response, http.StatusInternalServerError, "read Blackboard snapshot")
			return
		}
		factCount = len(projection.Snapshot.Knowledge.Facts)
		findingCount = len(projection.Snapshot.Knowledge.Findings)
		evidenceCount = len(projection.Snapshot.Knowledge.Evidence)
	}
	summary.Counts.Tasks = len(tasks)
	summary.Counts.Facts = factCount
	summary.Counts.Findings = findingCount
	summary.Counts.Evidence = evidenceCount

	writeJSON(response, http.StatusOK, summary)
}
