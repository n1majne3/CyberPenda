package runtime

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type NativeSessionMetadata struct {
	ContainerID       string
	NativeSessionID   string
	NativeSessionPath string
}

// DiscoverCodexSession returns the newest saved Codex session under providerHome.
// It is best-effort: when no session exists yet it returns zero values.
func DiscoverCodexSession(providerHome string) (NativeSessionMetadata, error) {
	sessionsRoot := filepath.Join(providerHome, "sessions")
	var newestPath string
	var newestMod int64

	err := filepath.WalkDir(sessionsRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mod := info.ModTime().UnixNano()
		if newestPath == "" || mod > newestMod {
			newestPath = path
			newestMod = mod
		}
		return nil
	})
	if err != nil {
		return NativeSessionMetadata{}, err
	}
	if newestPath == "" {
		return discoverCodexSQLiteSession(providerHome)
	}

	sessionID, err := readCodexSessionID(newestPath)
	if err != nil {
		return NativeSessionMetadata{}, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return discoverCodexSQLiteSession(providerHome)
	}
	return NativeSessionMetadata{
		NativeSessionID:   sessionID,
		NativeSessionPath: newestPath,
	}, nil
}

func readCodexSessionID(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var line struct {
			Type    string `json:"type"`
			Payload struct {
				SessionID string `json:"session_id"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		if line.Type == "session_meta" && strings.TrimSpace(line.Payload.SessionID) != "" {
			return line.Payload.SessionID, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", nil
}

func discoverCodexSQLiteSession(providerHome string) (NativeSessionMetadata, error) {
	paths, err := filepath.Glob(filepath.Join(providerHome, "state*.sqlite"))
	if err != nil {
		return NativeSessionMetadata{}, err
	}
	var newest NativeSessionMetadata
	var newestUpdated int64
	for _, path := range paths {
		sessionID, updated, err := readCodexSQLiteThread(path)
		if err != nil || strings.TrimSpace(sessionID) == "" {
			continue
		}
		if newest.NativeSessionID == "" || updated > newestUpdated {
			newest = NativeSessionMetadata{
				NativeSessionID:   sessionID,
				NativeSessionPath: path,
			}
			newestUpdated = updated
		}
	}
	return newest, nil
}

func readCodexSQLiteThread(path string) (string, int64, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", 0, err
	}
	defer db.Close()
	queries := []string{
		`SELECT id, updated_at_ms FROM threads WHERE trim(id) <> '' ORDER BY updated_at_ms DESC LIMIT 1`,
		`SELECT id, updated_at FROM threads WHERE trim(id) <> '' ORDER BY updated_at DESC LIMIT 1`,
		`SELECT id, created_at_ms FROM threads WHERE trim(id) <> '' ORDER BY created_at_ms DESC LIMIT 1`,
		`SELECT id, created_at FROM threads WHERE trim(id) <> '' ORDER BY created_at DESC LIMIT 1`,
		`SELECT id, 0 FROM threads WHERE trim(id) <> '' LIMIT 1`,
	}
	for _, query := range queries {
		var sessionID string
		var updated int64
		err := db.QueryRow(query).Scan(&sessionID, &updated)
		if err == nil {
			return strings.TrimSpace(sessionID), updated, nil
		}
		if err == sql.ErrNoRows {
			return "", 0, nil
		}
	}
	return "", 0, nil
}
