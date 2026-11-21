package daemon_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/daemon"
)

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
