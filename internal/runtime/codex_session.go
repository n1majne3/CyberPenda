package runtime

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
		return NativeSessionMetadata{}, nil
	}

	sessionID, err := readCodexSessionID(newestPath)
	if err != nil {
		return NativeSessionMetadata{}, err
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
