package daemon_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestSkillArchiveImportPublishesZIPBundleWithScripts(t *testing.T) {
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          filepath.Join(t.TempDir(), "runs"),
		SkillsRoot:           filepath.Join(t.TempDir(), "skills"),
		DisableBuiltinSkills: true,
	})

	var archive bytes.Buffer
	zipWriter := zip.NewWriter(&archive)
	for path, content := range map[string]string{
		"recon-helper/SKILL.md": `---
name: recon-helper
description: Reusable recon workflow with scripts.
---

# Recon Helper`,
		"recon-helper/scripts/probe.sh": "#!/bin/sh\necho probe\n",
	} {
		entry, err := zipWriter.Create(path)
		if err != nil {
			t.Fatalf("create ZIP entry %s: %v", path, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write ZIP entry %s: %v", path, err)
		}
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatalf("close ZIP archive: %v", err)
	}

	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	part, err := multipartWriter.CreateFormFile("archive", "recon-helper.zip")
	if err != nil {
		t.Fatalf("create archive form file: %v", err)
	}
	if _, err := part.Write(archive.Bytes()); err != nil {
		t.Fatalf("write archive form file: %v", err)
	}
	if err := multipartWriter.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/skills/import", &body)
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("expected ZIP import status 201, got %d with body %s", response.Code, response.Body.String())
	}

	getRequest := httptest.NewRequest(http.MethodGet, "/api/skills/recon-helper", nil)
	getResponse := httptest.NewRecorder()
	server.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("expected imported skill status 200, got %d with body %s", getResponse.Code, getResponse.Body.String())
	}
	var imported struct {
		ID          string            `json:"id"`
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Files       map[string]string `json:"files"`
	}
	if err := json.NewDecoder(getResponse.Body).Decode(&imported); err != nil {
		t.Fatalf("decode imported skill: %v", err)
	}
	if imported.ID != "recon-helper" || imported.Name != "recon-helper" || imported.Description != "Reusable recon workflow with scripts." {
		t.Fatalf("unexpected imported metadata: %#v", imported)
	}
	if imported.Files["scripts/probe.sh"] != "#!/bin/sh\necho probe\n" {
		t.Fatalf("expected nested script to be preserved, got %#v", imported.Files)
	}
}

func TestSkillArchiveImportPublishesTarGzipBundleWithScripts(t *testing.T) {
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          filepath.Join(t.TempDir(), "runs"),
		SkillsRoot:           filepath.Join(t.TempDir(), "skills"),
		DisableBuiltinSkills: true,
	})

	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	for path, content := range map[string]string{
		"recon-helper/SKILL.md": `---
name: recon-helper
description: Reusable recon workflow with scripts.
---

# Recon Helper`,
		"recon-helper/scripts/probe.sh": "#!/bin/sh\necho probe\n",
	} {
		header := &tar.Header{Name: path, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("write TAR header %s: %v", path, err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatalf("write TAR entry %s: %v", path, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close TAR archive: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip archive: %v", err)
	}

	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	part, err := multipartWriter.CreateFormFile("archive", "recon-helper.tar.gz")
	if err != nil {
		t.Fatalf("create archive form file: %v", err)
	}
	if _, err := part.Write(archive.Bytes()); err != nil {
		t.Fatalf("write archive form file: %v", err)
	}
	if err := multipartWriter.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/skills/import", &body)
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("expected TAR.GZ import status 201, got %d with body %s", response.Code, response.Body.String())
	}

	getRequest := httptest.NewRequest(http.MethodGet, "/api/skills/recon-helper", nil)
	getResponse := httptest.NewRecorder()
	server.ServeHTTP(getResponse, getRequest)
	var imported struct {
		Files map[string]string `json:"files"`
	}
	if err := json.NewDecoder(getResponse.Body).Decode(&imported); err != nil {
		t.Fatalf("decode imported skill: %v", err)
	}
	if imported.Files["scripts/probe.sh"] != "#!/bin/sh\necho probe\n" {
		t.Fatalf("expected nested script to be preserved, got %#v", imported.Files)
	}
}

func TestSkillArchiveImportRejectsUnsafeOrInvalidBundles(t *testing.T) {
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          filepath.Join(t.TempDir(), "runs"),
		SkillsRoot:           filepath.Join(t.TempDir(), "skills"),
		DisableBuiltinSkills: true,
	})
	validSkill := "---\nname: recon-helper\n---\n# Recon Helper\n"

	tests := []struct {
		name    string
		entries []zipArchiveEntry
	}{
		{
			name: "path traversal",
			entries: []zipArchiveEntry{
				{name: "../recon-helper/SKILL.md", content: validSkill},
			},
		},
		{
			name: "symlink",
			entries: []zipArchiveEntry{
				{name: "recon-helper/SKILL.md", content: validSkill},
				{name: "recon-helper/scripts/probe.sh", content: "../../outside", mode: os.ModeSymlink | 0o777},
			},
		},
		{
			name: "multiple bundles",
			entries: []zipArchiveEntry{
				{name: "recon-helper/SKILL.md", content: validSkill},
				{name: "other-helper/SKILL.md", content: "---\nname: other-helper\n---\n"},
			},
		},
		{
			name: "root and nested bundles",
			entries: []zipArchiveEntry{
				{name: "SKILL.md", content: validSkill},
				{name: "other-helper/SKILL.md", content: "---\nname: other-helper\n---\n"},
			},
		},
		{
			name: "missing SKILL.md",
			entries: []zipArchiveEntry{
				{name: "recon-helper/scripts/probe.sh", content: "#!/bin/sh\n"},
			},
		},
		{
			name: "oversized expanded file",
			entries: []zipArchiveEntry{
				{name: "recon-helper/SKILL.md", content: validSkill},
				{name: "recon-helper/scripts/large.txt", content: string(make([]byte, (16<<20)+1))},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := postSkillArchive(t, server, "recon-helper.zip", makeZIPArchive(t, test.entries))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected invalid archive status 400, got %d with body %s", response.Code, response.Body.String())
			}
		})
	}
}

type zipArchiveEntry struct {
	name    string
	content string
	mode    os.FileMode
}

func makeZIPArchive(t *testing.T, entries []zipArchiveEntry) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		part, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create ZIP entry %s: %v", entry.name, err)
		}
		if _, err := part.Write([]byte(entry.content)); err != nil {
			t.Fatalf("write ZIP entry %s: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close ZIP archive: %v", err)
	}
	return archive.Bytes()
}

func postSkillArchive(t *testing.T, server http.Handler, filename string, archive []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("archive", filename)
	if err != nil {
		t.Fatalf("create archive form file: %v", err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatalf("write archive form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/skills/import", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
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
