package session

import (
	"fmt"
	"io"
	"path/filepath"
	"pentest/internal/store"
	"strings"
	"testing"
)

func TestOutputBarrierCleanupCannotRemoveReplacementRun(t *testing.T) {
	service := &Service{}
	oldCalled, newCalled := 0, 0
	oldCleanup := service.RegisterRuntimeOutputFlush("owner", func() error { oldCalled++; return nil })
	newCleanup := service.RegisterRuntimeOutputFlush("owner", func() error {
		newCalled++
		otherCleanup := service.RegisterRuntimeOutputFlush("other", func() error { return nil })
		otherCleanup()
		return nil
	})
	oldCleanup()
	if err := service.FlushRuntimeOutputs("owner"); err != nil {
		t.Fatal(err)
	}
	if oldCalled != 0 || newCalled != 1 {
		t.Fatalf("callbacks: old=%d new=%d", oldCalled, newCalled)
	}
	newCleanup()
	newCleanup()
	if err := service.FlushRuntimeOutputs("owner"); err != nil {
		t.Fatal(err)
	}
	if newCalled != 1 {
		t.Fatal("cleanup left stale barrier")
	}
}

func TestAttachmentStagingFlushesNewOutputBeforeInputTransaction(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement=%t", replacement), func(t *testing.T) {
			root := t.TempDir()
			db, err := store.Open(filepath.Join(root, "staging.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s := NewService(db, filepath.Join(root, "sessions"))
			created, err := s.Create(CreateRequest{Input: "Inspect"})
			if err != nil {
				t.Fatal(err)
			}
			c, err := s.CreateContinuation(created.ID, "profile:one", "codex", RunnerHost)
			if err != nil {
				t.Fatal(err)
			}
			pending := false
			cleanup := s.RegisterRuntimeOutputFlush(created.ID, func() error {
				if !pending {
					return nil
				}
				_, err := s.AppendRuntimeOutputs(created.ID, []EventPayload{{"text": "during-copy"}})
				if err == nil {
					pending = false
				}
				return err
			})
			defer cleanup()
			attachment := Attachment{Name: "probe.txt", Size: 4, Open: func() (io.ReadCloser, error) { pending = true; return io.NopCloser(strings.NewReader("data")), nil }}
			if replacement {
				_, err = s.CreateReplacementContinuationWithInput(c, nil, ConversationInput{Role: "user", Text: "B", Attachments: []Attachment{attachment}})
			} else {
				_, _, err = s.AppendConversationInput(created.ID, c.ID, "user", "B", []Attachment{attachment})
			}
			if err != nil {
				t.Fatal(err)
			}
			events, err := s.Events(created.ID)
			if err != nil {
				t.Fatal(err)
			}
			output, input := 0, 0
			for _, event := range events {
				if event.Payload["text"] == "during-copy" {
					output = event.Seq
				}
				if event.Payload["text"] == "B" {
					input = event.Seq
				}
			}
			if output == 0 || input <= output {
				t.Fatalf("output emitted during attachment staging committed after input: output=%d input=%d", output, input)
			}
		})
	}
}
