package daemon

import (
	"context"
	"net/http"
	"strings"

	"pentest/internal/blackboardv2"
	"pentest/internal/report"
)

func (server *Server) handleBlackboardV2PentestReport(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, false)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	format, formatErr := blackboardV2ReportFormat(request)
	if formatErr != nil {
		writeBlackboardV2Error(response, formatErr, nil)
		return
	}
	// Report-only response: conditional/auth + revision ETag, but never attach
	// continuation synchronization / full Runtime Snapshot sibling.
	server.serveBlackboardV2Conditional(response, request, principal, true, false, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, int, error) {
		projection, revision, err := server.blackboardV2.ProjectPentestReport(ctx, principal.projectID)
		if err != nil {
			return nil, 0, err
		}
		if format == "json" {
			return projection, revision, nil
		}
		return blackboardv2.NewReportMarkdown(report.RenderV2Markdown(projection)), revision, nil
	})
}

func (server *Server) handleBlackboardV2CTFSolution(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, false)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	format, formatErr := blackboardV2ReportFormat(request)
	if formatErr != nil {
		writeBlackboardV2Error(response, formatErr, nil)
		return
	}
	// CTF solution is report-only: never attach sync / full Runtime Snapshot.
	server.serveBlackboardV2Conditional(response, request, principal, true, false, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, int, error) {
		projection, revision, err := server.blackboardV2.ProjectCTFSolution(ctx, principal.projectID)
		if err != nil {
			return nil, 0, err
		}
		if format == "json" {
			return projection, revision, nil
		}
		return blackboardv2.NewReportMarkdown(report.RenderCTFV2Markdown(projection)), revision, nil
	})
}

func blackboardV2ReportFormat(request *http.Request) (string, *blackboardv2.Error) {
	format := strings.TrimSpace(request.URL.Query().Get("format"))
	if format == "" {
		format = "markdown"
	}
	if format != "markdown" && format != "json" {
		return "", blackboardV2HTTPError("invalid_schema", "format must be markdown or json", "format")
	}
	return format, nil
}
