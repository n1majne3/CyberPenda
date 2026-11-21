package blackboardv2

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEvidenceTempAdoptionChecksDestinationTruncate(t *testing.T) {
	for _, name := range []string{"writable", "read-only", "no-candidate"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			payload := []byte("retained proof\n")
			digest := sha256.Sum256(payload)
			candidatePath := filepath.Join(directory, "proof.partial")
			if name != "no-candidate" {
				if err := os.WriteFile(candidatePath, payload, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			targetPath := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(targetPath, []byte("keep prior bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			flags := os.O_RDWR
			if name == "read-only" {
				flags = os.O_RDONLY
			}
			target, err := os.OpenFile(targetPath, flags, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			adopted, err := sweepEvidenceRecoveryTemps(root, func() (*os.File, error) {
				return target, nil
			}, hex.EncodeToString(digest[:]), int64(len(payload)), 0, func(name string) (bool, error) {
				return name == "proof.partial", nil
			})
			want := payload
			switch name {
			case "read-only":
				var pathError *os.PathError
				if adopted || !errors.As(err, &pathError) || pathError.Op != "truncate" {
					t.Fatalf("destination truncate failure = adopted %t, %v", adopted, err)
				}
				if _, err := os.Stat(candidatePath); err != nil {
					t.Fatalf("candidate lost after failed adoption: %v", err)
				}
				want = []byte("keep prior bytes")
			case "no-candidate":
				if adopted || err != nil {
					t.Fatalf("empty recovery = %t, %v", adopted, err)
				}
				want = []byte("keep prior bytes")
			default:
				if !adopted || err != nil {
					t.Fatalf("valid recovery = %t, %v", adopted, err)
				}
				if _, err := os.Stat(candidatePath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("adopted candidate remains: %v", err)
				}
			}
			got, err := os.ReadFile(targetPath)
			if err != nil || string(got) != string(want) {
				t.Fatalf("destination bytes = %q, %v; want %q", got, err, want)
			}
		})
	}
}
