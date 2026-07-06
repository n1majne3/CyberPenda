package runtime

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DiscoverPiSession returns the newest saved Pi session under providerHome.
// It is best-effort: when no session exists yet it returns zero values.
func DiscoverPiSession(providerHome string) (NativeSessionMetadata, error) {
	sessionsRoot := filepath.Join(providerHome, "agent", "sessions")
	type candidate struct {
		path string
		mod  int64
	}
	var candidates []candidate

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
		candidates = append(candidates, candidate{path: path, mod: info.ModTime().UnixNano()})
		return nil
	})
	if err != nil {
		return NativeSessionMetadata{}, err
	}

	for len(candidates) > 0 {
		newestIndex := 0
		for i := 1; i < len(candidates); i++ {
			if candidates[i].mod > candidates[newestIndex].mod {
				newestIndex = i
			}
		}
		newest := candidates[newestIndex]
		candidates = append(candidates[:newestIndex], candidates[newestIndex+1:]...)

		sessionID, err := readPiSessionID(newest.path)
		if err != nil {
			return NativeSessionMetadata{}, err
		}
		if strings.TrimSpace(sessionID) == "" {
			continue
		}
		return NativeSessionMetadata{
			NativeSessionID:   sessionID,
			NativeSessionPath: newest.path,
		}, nil
	}
	return NativeSessionMetadata{}, nil
}

func readPiSessionID(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		metadata := NativeSessionMetadataFromRuntimeLine(scanner.Text())
		if strings.TrimSpace(metadata.NativeSessionID) != "" {
			return metadata.NativeSessionID, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", nil
}
