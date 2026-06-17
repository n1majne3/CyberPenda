package daemon

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Config struct {
	Version string
	DBPath  string
}

type Server struct {
	mux     *http.ServeMux
	version string
	db      *sql.DB
}

func NewServer(config Config) (*Server, error) {
	dbPath := config.DBPath
	if dbPath == "" {
		dbPath = ":memory:"
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	server := &Server{
		mux:     http.NewServeMux(),
		version: config.Version,
		db:      db,
	}
	server.routes()

	return server, nil
}

func (server *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	server.mux.ServeHTTP(response, request)
}

func (server *Server) Close() error {
	return server.db.Close()
}

func (server *Server) routes() {
	server.mux.HandleFunc("GET /health", server.handleHealth)
	server.mux.HandleFunc("GET /api/projects", server.handleListProjects)
	server.mux.HandleFunc("POST /api/projects", server.handleCreateProject)
	server.mux.HandleFunc("GET /api/projects/", server.handleGetProject)
}

func (server *Server) handleHealth(response http.ResponseWriter, request *http.Request) {
	payload := struct {
		Version  string `json:"version"`
		Database struct {
			Status string `json:"status"`
		} `json:"database"`
	}{
		Version: server.version,
	}
	payload.Database.Status = "ok"

	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(payload)
}

type project struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Scope       projectScope `json:"scope"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

type projectScope struct {
	Domains       []string `json:"domains,omitempty"`
	IPs           []string `json:"ips,omitempty"`
	CIDRs         []string `json:"cidrs,omitempty"`
	URLs          []string `json:"urls,omitempty"`
	Ports         []string `json:"ports,omitempty"`
	Excluded      []string `json:"excluded,omitempty"`
	TestingLimits []string `json:"testing_limits,omitempty"`
	Notes         string   `json:"notes,omitempty"`
}

func (server *Server) handleCreateProject(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Name        string       `json:"name"`
		Description string       `json:"description"`
		Scope       projectScope `json:"scope"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(input.Name) == "" {
		writeError(response, http.StatusBadRequest, "project name is required")
		return
	}

	now := time.Now().UTC()
	created := project{
		ID:          newID(),
		Name:        input.Name,
		Description: input.Description,
		Scope:       input.Scope,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	scopeJSON, err := json.Marshal(created.Scope)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "encode scope")
		return
	}

	_, err = server.db.Exec(
		`INSERT INTO projects (id, name, description, scope_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		created.ID,
		created.Name,
		created.Description,
		string(scopeJSON),
		created.CreatedAt.Format(time.RFC3339Nano),
		created.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "store project")
		return
	}

	writeJSON(response, http.StatusCreated, created)
}

func (server *Server) handleListProjects(response http.ResponseWriter, request *http.Request) {
	rows, err := server.db.Query(
		`SELECT id, name, description, scope_json, created_at, updated_at FROM projects ORDER BY created_at ASC`,
	)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list projects")
		return
	}
	defer rows.Close()

	var projects []project
	for rows.Next() {
		found, err := scanProject(rows)
		if err != nil {
			writeError(response, http.StatusInternalServerError, "scan project")
			return
		}
		projects = append(projects, found)
	}
	if err := rows.Err(); err != nil {
		writeError(response, http.StatusInternalServerError, "list projects")
		return
	}

	writeJSON(response, http.StatusOK, struct {
		Projects []project `json:"projects"`
	}{
		Projects: projects,
	})
}

func (server *Server) handleGetProject(response http.ResponseWriter, request *http.Request) {
	id := strings.TrimPrefix(request.URL.Path, "/api/projects/")
	if id == "" || strings.Contains(id, "/") {
		writeError(response, http.StatusNotFound, "project not found")
		return
	}

	found, err := server.loadProject(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load project")
		return
	}

	writeJSON(response, http.StatusOK, found)
}

func (server *Server) loadProject(id string) (project, error) {
	return scanProject(server.db.QueryRow(
		`SELECT id, name, description, scope_json, created_at, updated_at FROM projects WHERE id = ?`,
		id,
	))
}

type projectScanner interface {
	Scan(dest ...any) error
}

func scanProject(scanner projectScanner) (project, error) {
	var found project
	var scopeJSON string
	var createdAt string
	var updatedAt string
	err := scanner.Scan(&found.ID, &found.Name, &found.Description, &scopeJSON, &createdAt, &updatedAt)
	if err != nil {
		return project{}, err
	}
	if err := json.Unmarshal([]byte(scopeJSON), &found.Scope); err != nil {
		return project{}, err
	}
	found.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return project{}, err
	}
	found.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return project{}, err
	}

	return found, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			scope_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
	`)
	return err
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}

func writeError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, struct {
		Error string `json:"error"`
	}{
		Error: message,
	})
}

func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(bytes[:])
}
