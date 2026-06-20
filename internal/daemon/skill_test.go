package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"pentest/internal/daemon"
	"pentest/internal/skill"
)

func TestSkillsPublishListOptOutDeleteAndAuditHTTP(t *testing.T) {
	server := newDaemon(t)
	profileID := createRuntimeProfile(t, server, `{"name":"Codex","provider":"codex"}`)

	putSkill(t, server, "recon-helper", `{
		"name":"Recon Helper",
		"description":"Reusable recon workflow",
		"credential_refs":["recon-api-key"],
		"files":{"SKILL.md":"# Recon Helper\nUse approved recon tools.","scripts/probe.sh":"#!/bin/sh\n"}
	}`)

	getReq := httptest.NewRequest(http.MethodGet, "/api/skills/recon-helper", nil)
	getResp := httptest.NewRecorder()
	server.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("expected get status 200, got %d with body %s", getResp.Code, getResp.Body.String())
	}
	var gotSkill struct {
		Files map[string]string `json:"files"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&gotSkill); err != nil {
		t.Fatalf("decode get skill: %v", err)
	}
	if gotSkill.Files["SKILL.md"] == "" || gotSkill.Files["scripts/probe.sh"] == "" {
		t.Fatalf("expected editable bundle files, got %#v", gotSkill.Files)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/skills?runtime_profile_id="+profileID, nil)
	listResp := httptest.NewRecorder()
	server.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d with body %s", listResp.Code, listResp.Body.String())
	}
	listBody := listResp.Body.Bytes()
	var listed struct {
		Skills []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(listBody, &listed); err != nil {
		t.Fatalf("decode skills list: %v", err)
	}
	if len(listed.Skills) != 1 || listed.Skills[0].ID != "recon-helper" || !listed.Skills[0].Enabled {
		t.Fatalf("unexpected skills list: %#v", listed.Skills)
	}
	var listedRaw struct {
		Skills []map[string]any `json:"skills"`
	}
	if err := json.Unmarshal(listBody, &listedRaw); err != nil {
		t.Fatalf("decode raw skills list: %v", err)
	}
	if _, ok := listedRaw.Skills[0]["credential_refs"]; ok {
		t.Fatalf("skills should not expose credential_refs, got %#v", listedRaw.Skills[0])
	}

	optOutReq := httptest.NewRequest(http.MethodPut, "/api/skills/recon-helper/profiles/"+profileID+"/opt-out", nil)
	optOutResp := httptest.NewRecorder()
	server.ServeHTTP(optOutResp, optOutReq)
	if optOutResp.Code != http.StatusNoContent {
		t.Fatalf("expected opt-out status 204, got %d with body %s", optOutResp.Code, optOutResp.Body.String())
	}

	listResp = httptest.NewRecorder()
	server.ServeHTTP(listResp, listReq)
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode skills list after opt-out: %v", err)
	}
	if len(listed.Skills) != 1 || listed.Skills[0].Enabled {
		t.Fatalf("expected skill disabled for profile after opt-out, got %#v", listed.Skills)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/skills/recon-helper", nil)
	deleteResp := httptest.NewRecorder()
	server.ServeHTTP(deleteResp, deleteReq)
	if deleteResp.Code != http.StatusNoContent {
		t.Fatalf("expected delete of fully opted-out skill status 204, got %d with body %s", deleteResp.Code, deleteResp.Body.String())
	}

	auditReq := httptest.NewRequest(http.MethodGet, "/api/audit-log", nil)
	auditResp := httptest.NewRecorder()
	server.ServeHTTP(auditResp, auditReq)
	if auditResp.Code != http.StatusOK {
		t.Fatalf("expected global audit status 200, got %d with body %s", auditResp.Code, auditResp.Body.String())
	}
	var audit struct {
		Entries []struct {
			Kind string `json:"kind"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(auditResp.Body).Decode(&audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if !auditHasKind(audit.Entries, "skill_published") || !auditHasKind(audit.Entries, "skill_opt_out_changed") || !auditHasKind(audit.Entries, "skill_deleted") {
		t.Fatalf("expected skill audit events, got %#v", audit.Entries)
	}
}

func TestControlledSkillImportPublishesBundle(t *testing.T) {
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          filepath.Join(t.TempDir(), "runs"),
		SkillsRoot:           filepath.Join(t.TempDir(), "skills"),
		SkillImporter:        fakeSkillImporter{},
		DisableBuiltinSkills: true,
	})

	importReq := httptest.NewRequest(http.MethodPost, "/api/skills/import", bytes.NewReader([]byte(`{
		"source_kind":"npm",
		"package":"@acme/recon-skill",
		"ref":"1.2.3"
	}`)))
	importReq.Header.Set("Content-Type", "application/json")
	importResp := httptest.NewRecorder()
	server.ServeHTTP(importResp, importReq)
	if importResp.Code != http.StatusCreated {
		t.Fatalf("expected import status 201, got %d with body %s", importResp.Code, importResp.Body.String())
	}
	var imported struct {
		ID     string `json:"id"`
		Source struct {
			Kind    string `json:"kind"`
			Package string `json:"package"`
			Ref     string `json:"ref"`
		} `json:"source_provenance"`
	}
	if err := json.NewDecoder(importResp.Body).Decode(&imported); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if imported.ID != "imported-recon" || imported.Source.Package != "@acme/recon-skill" || imported.Source.Ref != "1.2.3" {
		t.Fatalf("unexpected imported skill: %#v", imported)
	}

	rawCommandReq := httptest.NewRequest(http.MethodPost, "/api/skills/import", bytes.NewReader([]byte(`{
		"command":"npx skills install @acme/recon-skill"
	}`)))
	rawCommandReq.Header.Set("Content-Type", "application/json")
	rawCommandResp := httptest.NewRecorder()
	server.ServeHTTP(rawCommandResp, rawCommandReq)
	if rawCommandResp.Code != http.StatusBadRequest {
		t.Fatalf("expected raw command import to be rejected, got %d with body %s", rawCommandResp.Code, rawCommandResp.Body.String())
	}
}

func TestSkillResponsesHideBuiltinSourceDetails(t *testing.T) {
	server := newDaemon(t)
	putSkill(t, server, "strix-vulnerabilities-xss", `{
		"name":"XSS",
		"source_provenance":{
			"kind":"builtin",
			"package":"usestrix/strix",
			"ref":"old-commit",
			"source_url":"https://github.com/usestrix/strix"
		},
		"files":{
			"SKILL.md":"# user edit",
			"UPSTREAM.md":"old source details"
		}
	}`)

	listReq := httptest.NewRequest(http.MethodGet, "/api/skills", nil)
	listResp := httptest.NewRecorder()
	server.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d with body %s", listResp.Code, listResp.Body.String())
	}
	var listed struct {
		Skills []struct {
			ID     string `json:"id"`
			Source struct {
				Kind      string `json:"kind"`
				Package   string `json:"package"`
				Ref       string `json:"ref"`
				SourceURL string `json:"source_url"`
			} `json:"source_provenance"`
		} `json:"skills"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode skills list: %v", err)
	}
	if len(listed.Skills) != 1 || listed.Skills[0].Source.Kind != "builtin" || listed.Skills[0].Source.Package != "" || listed.Skills[0].Source.Ref != "" || listed.Skills[0].Source.SourceURL != "" {
		t.Fatalf("expected builtin source details hidden in list, got %#v", listed.Skills)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/skills/strix-vulnerabilities-xss", nil)
	getResp := httptest.NewRecorder()
	server.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("expected get status 200, got %d with body %s", getResp.Code, getResp.Body.String())
	}
	var got struct {
		Source struct {
			Kind      string `json:"kind"`
			Package   string `json:"package"`
			Ref       string `json:"ref"`
			SourceURL string `json:"source_url"`
		} `json:"source_provenance"`
		Files map[string]string `json:"files"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decode get skill: %v", err)
	}
	if got.Source.Kind != "builtin" || got.Source.Package != "" || got.Source.Ref != "" || got.Source.SourceURL != "" {
		t.Fatalf("expected builtin source details hidden in get, got %#v", got.Source)
	}
	if _, ok := got.Files["UPSTREAM.md"]; ok {
		t.Fatalf("expected UPSTREAM.md hidden from get response, got %#v", got.Files)
	}
	if got.Files["SKILL.md"] != "# user edit" {
		t.Fatalf("expected normal files retained, got %#v", got.Files)
	}
}

func TestDaemonSeedsBuiltinSkills(t *testing.T) {
	server := newDaemonWithConfig(t, daemon.Config{
		Version: "test-version",
		DBPath:  filepath.Join(t.TempDir(), "pentest.db"),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/skills", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected skills status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var listed struct {
		Skills []struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
			Source  struct {
				Kind    string `json:"kind"`
				Package string `json:"package"`
				Ref     string `json:"ref"`
				URL     string `json:"source_url"`
			} `json:"source_provenance"`
		} `json:"skills"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode builtin skills list: %v", err)
	}
	if !hasBuiltinSkill(listed.Skills, "vulnerabilities-xss") {
		t.Fatalf("expected Strix builtin skill, got %#v", listed.Skills)
	}
	if !hasBuiltinSkill(listed.Skills, "api-security-testing") {
		t.Fatalf("expected CyberStrikeAI builtin skill, got %#v", listed.Skills)
	}
	detailReq := httptest.NewRequest(http.MethodGet, "/api/skills/"+listed.Skills[0].ID, nil)
	detailResp := httptest.NewRecorder()
	server.ServeHTTP(detailResp, detailReq)
	if detailResp.Code != http.StatusOK {
		t.Fatalf("expected listed builtin skill to be readable, got %d with body %s", detailResp.Code, detailResp.Body.String())
	}
	var detail struct {
		ID    string            `json:"id"`
		Files map[string]string `json:"files"`
	}
	if err := json.NewDecoder(detailResp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode builtin skill detail: %v", err)
	}
	if detail.ID != listed.Skills[0].ID || detail.Files["SKILL.md"] == "" {
		t.Fatalf("unexpected builtin skill detail: %#v", detail)
	}
}

func newDaemonWithConfig(t *testing.T, config daemon.Config) *daemon.Server {
	t.Helper()
	server, err := daemon.NewServer(config)
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})
	return server
}

type fakeSkillImporter struct{}

func (fakeSkillImporter) ImportSkill(ctx context.Context, request skill.ImportRequest) (skill.ImportedBundle, error) {
	return skill.ImportedBundle{
		Metadata: skill.Metadata{ID: "imported-recon", Name: "Imported Recon"},
		Files:    map[string]string{"SKILL.md": "# Imported Recon"},
	}, nil
}

func putSkill(t *testing.T, server *daemon.Server, id, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/skills/"+id, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated && resp.Code != http.StatusOK {
		t.Fatalf("expected put skill status 2xx, got %d with body %s", resp.Code, resp.Body.String())
	}
}

func auditHasKind(entries []struct {
	Kind string `json:"kind"`
}, kind string) bool {
	for _, entry := range entries {
		if entry.Kind == kind {
			return true
		}
	}
	return false
}

func hasBuiltinSkill(skills []struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Source  struct {
		Kind    string `json:"kind"`
		Package string `json:"package"`
		Ref     string `json:"ref"`
		URL     string `json:"source_url"`
	} `json:"source_provenance"`
}, id string) bool {
	for _, got := range skills {
		if got.ID == id && got.Enabled && got.Source.Kind == "builtin" && got.Source.Package == "" && got.Source.Ref == "" && got.Source.URL == "" {
			return true
		}
	}
	return false
}
