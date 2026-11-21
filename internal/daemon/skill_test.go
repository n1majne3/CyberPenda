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
}

func TestSkillsBulkOptOutAndEnableForRuntimeProfileHTTP(t *testing.T) {
	server := newDaemon(t)
	profileID := createRuntimeProfile(t, server, `{"name":"Codex","provider":"codex"}`)

	putSkill(t, server, "recon-helper", `{
		"name":"Recon Helper",
		"files":{"SKILL.md":"# Recon Helper"}
	}`)
	putSkill(t, server, "report-helper", `{
		"name":"Report Helper",
		"files":{"SKILL.md":"# Report Helper"}
	}`)

	bulkPath := "/api/skills/profiles/" + profileID + "/opt-out"
	disableReq := httptest.NewRequest(http.MethodPut, bulkPath, nil)
	disableResp := httptest.NewRecorder()
	server.ServeHTTP(disableResp, disableReq)
	if disableResp.Code != http.StatusNoContent {
		t.Fatalf("expected bulk opt-out status 204, got %d with body %s", disableResp.Code, disableResp.Body.String())
	}
	assertListedSkillEnablement(t, server, profileID, map[string]bool{
		"recon-helper":  false,
		"report-helper": false,
	})

	enableReq := httptest.NewRequest(http.MethodDelete, bulkPath, nil)
	enableResp := httptest.NewRecorder()
	server.ServeHTTP(enableResp, enableReq)
	if enableResp.Code != http.StatusNoContent {
		t.Fatalf("expected bulk enable status 204, got %d with body %s", enableResp.Code, enableResp.Body.String())
	}
	assertListedSkillEnablement(t, server, profileID, map[string]bool{
		"recon-helper":  true,
		"report-helper": true,
	})
}

func TestSkillGlobalOptOutHTTP(t *testing.T) {
	server := newDaemon(t)
	profileID := createRuntimeProfile(t, server, `{"name":"Codex","provider":"codex"}`)

	putSkill(t, server, "recon-helper", `{
		"name":"Recon Helper",
		"files":{"SKILL.md":"# Recon Helper"}
	}`)

	profileOptOutReq := httptest.NewRequest(http.MethodPut, "/api/skills/recon-helper/profiles/"+profileID+"/opt-out", nil)
	profileOptOutResp := httptest.NewRecorder()
	server.ServeHTTP(profileOptOutResp, profileOptOutReq)
	if profileOptOutResp.Code != http.StatusNoContent {
		t.Fatalf("expected Profile Skill Opt-Out status 204, got %d with body %s", profileOptOutResp.Code, profileOptOutResp.Body.String())
	}

	optOutReq := httptest.NewRequest(http.MethodPut, "/api/skills/recon-helper/opt-out", nil)
	optOutResp := httptest.NewRecorder()
	server.ServeHTTP(optOutResp, optOutReq)
	if optOutResp.Code != http.StatusNoContent {
		t.Fatalf("expected global opt-out status 204, got %d with body %s", optOutResp.Code, optOutResp.Body.String())
	}

	for _, item := range []struct {
		path            string
		profileOptedOut bool
	}{
		{path: "/api/skills", profileOptedOut: false},
		{path: "/api/skills?runtime_profile_id=" + profileID, profileOptedOut: true},
	} {
		path := item.path
		listReq := httptest.NewRequest(http.MethodGet, path, nil)
		listResp := httptest.NewRecorder()
		server.ServeHTTP(listResp, listReq)
		if listResp.Code != http.StatusOK {
			t.Fatalf("expected list status 200 for %s, got %d with body %s", path, listResp.Code, listResp.Body.String())
		}
		var listed struct {
			Skills []struct {
				ID               string `json:"id"`
				Enabled          bool   `json:"enabled"`
				GloballyOptedOut bool   `json:"globally_opted_out"`
				ProfileOptedOut  bool   `json:"profile_opted_out"`
			} `json:"skills"`
		}
		if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
			t.Fatalf("decode skills list for %s: %v", path, err)
		}
		if len(listed.Skills) != 1 || listed.Skills[0].ID != "recon-helper" || listed.Skills[0].Enabled || !listed.Skills[0].GloballyOptedOut || listed.Skills[0].ProfileOptedOut != item.profileOptedOut {
			t.Fatalf("unexpected globally opted-out list for %s: %#v", path, listed.Skills)
		}
	}

	enableReq := httptest.NewRequest(http.MethodDelete, "/api/skills/recon-helper/opt-out", nil)
	enableResp := httptest.NewRecorder()
	server.ServeHTTP(enableResp, enableReq)
	if enableResp.Code != http.StatusNoContent {
		t.Fatalf("expected global enable status 204, got %d with body %s", enableResp.Code, enableResp.Body.String())
	}
	assertListedSkillEnablement(t, server, profileID, map[string]bool{"recon-helper": false})

	profileEnableReq := httptest.NewRequest(http.MethodDelete, "/api/skills/recon-helper/profiles/"+profileID+"/opt-out", nil)
	profileEnableResp := httptest.NewRecorder()
	server.ServeHTTP(profileEnableResp, profileEnableReq)
	if profileEnableResp.Code != http.StatusNoContent {
		t.Fatalf("expected Profile Skill enable status 204, got %d with body %s", profileEnableResp.Code, profileEnableResp.Body.String())
	}
	assertListedSkillEnablement(t, server, profileID, map[string]bool{"recon-helper": true})
}

func TestSkillsBulkGlobalOptOutAndEnableHTTP(t *testing.T) {
	server := newDaemon(t)
	profileID := createRuntimeProfile(t, server, `{"name":"Codex","provider":"codex"}`)

	putSkill(t, server, "recon-helper", `{
		"name":"Recon Helper",
		"files":{"SKILL.md":"# Recon Helper"}
	}`)
	putSkill(t, server, "report-helper", `{
		"name":"Report Helper",
		"files":{"SKILL.md":"# Report Helper"}
	}`)

	profileOptOutReq := httptest.NewRequest(http.MethodPut, "/api/skills/recon-helper/profiles/"+profileID+"/opt-out", nil)
	profileOptOutResp := httptest.NewRecorder()
	server.ServeHTTP(profileOptOutResp, profileOptOutReq)
	if profileOptOutResp.Code != http.StatusNoContent {
		t.Fatalf("expected Profile Skill Opt-Out status 204, got %d with body %s", profileOptOutResp.Code, profileOptOutResp.Body.String())
	}

	bulkPath := "/api/skills/opt-outs/global"
	disableReq := httptest.NewRequest(http.MethodPut, bulkPath, nil)
	disableResp := httptest.NewRecorder()
	server.ServeHTTP(disableResp, disableReq)
	if disableResp.Code != http.StatusNoContent {
		t.Fatalf("expected global bulk opt-out status 204, got %d with body %s", disableResp.Code, disableResp.Body.String())
	}
	assertListedSkillEnablement(t, server, "", map[string]bool{
		"recon-helper":  false,
		"report-helper": false,
	})
	assertListedSkillEnablement(t, server, profileID, map[string]bool{
		"recon-helper":  false,
		"report-helper": false,
	})

	enableReq := httptest.NewRequest(http.MethodDelete, bulkPath, nil)
	enableResp := httptest.NewRecorder()
	server.ServeHTTP(enableResp, enableReq)
	if enableResp.Code != http.StatusNoContent {
		t.Fatalf("expected global bulk enable status 204, got %d with body %s", enableResp.Code, enableResp.Body.String())
	}
	assertListedSkillEnablement(t, server, "", map[string]bool{
		"recon-helper":  true,
		"report-helper": true,
	})
	assertListedSkillEnablement(t, server, profileID, map[string]bool{
		"recon-helper":  false,
		"report-helper": true,
	})
}

func assertListedSkillEnablement(t *testing.T, server http.Handler, profileID string, want map[string]bool) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/skills?runtime_profile_id="+profileID, nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected skills list status 200, got %d with body %s", response.Code, response.Body.String())
	}
	var body struct {
		Skills []struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		} `json:"skills"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode skills list: %v", err)
	}
	if len(body.Skills) != len(want) {
		t.Fatalf("listed skills = %#v, want enablement %#v", body.Skills, want)
	}
	for _, listed := range body.Skills {
		enabled, ok := want[listed.ID]
		if !ok || listed.Enabled != enabled {
			t.Fatalf("listed skill %q enabled = %t, want %#v", listed.ID, listed.Enabled, want)
		}
	}
}

func TestSkillResponsesHideBuiltinSourceDetails(t *testing.T) {
	server := newDaemon(t)
	putSkill(t, server, "strix-tooling-nmap", `{
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

	getReq := httptest.NewRequest(http.MethodGet, "/api/skills/strix-tooling-nmap", nil)
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
	if !hasBuiltinSkill(listed.Skills, "tooling-nmap") {
		t.Fatalf("expected Strix builtin skill, got %#v", listed.Skills)
	}
	if !hasBuiltinSkill(listed.Skills, "scoreboard-driven-web-challenge") {
		t.Fatalf("expected scoreboard-driven web challenge builtin skill, got %#v", listed.Skills)
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
	if config.ProviderSessionFactory == nil {
		config.ProviderSessionFactory = testScriptProviderSessionFactory{}
	}
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
