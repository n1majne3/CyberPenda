package hostedcontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"pentest/internal/transcript"
)

const maskedHostedCredential = "[REDACTED]"

type transcriptPage struct {
	TaskID   string             `json:"task_id"`
	Entries  []transcript.Entry `json:"entries"`
	Cursor   int                `json:"cursor"`
	HasOlder bool               `json:"has_older"`
}

// streamInitialTranscript emits the complete retained Transcript as one
// stable snapshot. The initial response cursor is the snapshot boundary.
// Backward-page cursors are deliberately ignored because live Events can
// advance them while older pages are being read.
func (app *HTTPApp) streamInitialTranscript(ctx context.Context, run RunRef, output io.Writer, masker *exactMasker) (int, error) {
	base := taskTranscriptPath(run)
	var initial transcriptPage
	if err := app.request(ctx, http.MethodGet, base, nil, &initial); err != nil {
		return 0, fmt.Errorf("read initial hosted Transcript: %w", err)
	}
	entries := append([]transcript.Entry(nil), initial.Entries...)
	if err := validateTranscriptOrder(entries, -1); err != nil {
		return 0, err
	}
	if len(entries) > 0 && entries[len(entries)-1].Seq > initial.Cursor {
		return 0, errorsInvalidTranscriptPage("initial cursor precedes a returned entry")
	}
	seen := transcriptEntryIDs(entries)
	oldest := oldestTranscriptSeq(entries)
	hasOlder := initial.HasOlder
	for hasOlder {
		if len(entries) == 0 || oldest <= 0 {
			return 0, errorsInvalidTranscriptPage("older history has no valid cursor")
		}
		var page transcriptPage
		path := base + "?before=" + strconv.Itoa(oldest)
		if err := app.request(ctx, http.MethodGet, path, nil, &page); err != nil {
			return 0, fmt.Errorf("read older hosted Transcript: %w", err)
		}
		if len(page.Entries) == 0 {
			return 0, errorsInvalidTranscriptPage("older history returned an empty page")
		}
		if err := validateTranscriptOrder(page.Entries, -1); err != nil {
			return 0, err
		}
		older := make([]transcript.Entry, 0, len(page.Entries))
		for _, entry := range page.Entries {
			if entry.Seq >= oldest {
				return 0, errorsInvalidTranscriptPage("older history did not move backward")
			}
			if _, duplicate := seen[entry.ID]; duplicate {
				continue
			}
			seen[entry.ID] = struct{}{}
			older = append(older, entry)
		}
		if len(older) == 0 {
			return 0, errorsInvalidTranscriptPage("older history contained only duplicate entries")
		}
		entries = append(older, entries...)
		oldest = oldestTranscriptSeq(older)
		hasOlder = page.HasOlder
	}
	if err := app.emitTranscriptEntries(ctx, run, output, masker, entries); err != nil {
		return 0, err
	}
	return initial.Cursor, nil
}

// drainTranscript follows the committed live-tail cursor until one successful
// page has no entries and cannot advance the cursor. The caller commits a page
// only after every entry in it reaches stdout.
func (app *HTTPApp) drainTranscript(ctx context.Context, run RunRef, output io.Writer, masker *exactMasker, cursor int) (int, error) {
	base := taskTranscriptPath(run)
	for {
		var page transcriptPage
		path := base + "?after=" + strconv.Itoa(cursor)
		if err := app.request(ctx, http.MethodGet, path, nil, &page); err != nil {
			return cursor, fmt.Errorf("read hosted Transcript delta: %w", err)
		}
		if page.Cursor < cursor {
			return cursor, errorsInvalidTranscriptPage("live-tail cursor regressed")
		}
		if err := validateTranscriptOrder(page.Entries, cursor); err != nil {
			return cursor, err
		}
		if len(page.Entries) > 0 && page.Entries[len(page.Entries)-1].Seq > page.Cursor {
			return cursor, errorsInvalidTranscriptPage("live-tail cursor precedes a returned entry")
		}
		if err := app.emitTranscriptEntries(ctx, run, output, masker, page.Entries); err != nil {
			return cursor, err
		}
		previous := cursor
		cursor = page.Cursor
		if len(page.Entries) == 0 && cursor == previous {
			return cursor, nil
		}
	}
}

func (app *HTTPApp) emitTranscriptEntries(ctx context.Context, run RunRef, output io.Writer, masker *exactMasker, entries []transcript.Entry) error {
	for _, preview := range entries {
		entry := preview
		if preview.Truncated {
			complete, err := app.transcriptEntryDetail(ctx, run, preview)
			if err != nil {
				return err
			}
			entry = complete
		}
		line, err := masker.marshal(entry)
		if err != nil {
			return fmt.Errorf("encode hosted Transcript entry: %w", err)
		}
		line = append(line, '\n')
		written, err := output.Write(line)
		if err != nil {
			return fmt.Errorf("write hosted Transcript to stdout: %w", err)
		}
		if written != len(line) {
			return fmt.Errorf("write hosted Transcript to stdout: %w", io.ErrShortWrite)
		}
	}
	return nil
}

func (app *HTTPApp) transcriptEntryDetail(ctx context.Context, run RunRef, preview transcript.Entry) (transcript.Entry, error) {
	prefix := taskTranscriptPath(run) + "/entries/"
	detail := strings.TrimSpace(preview.Detail)
	parsed, err := url.Parse(detail)
	if err != nil || parsed.IsAbs() || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.HasPrefix(parsed.Path, prefix) {
		return transcript.Entry{}, errorsInvalidTranscriptPage("truncated entry has an invalid detail reference")
	}
	var complete transcript.Entry
	if err := app.request(ctx, http.MethodGet, parsed.EscapedPath(), nil, &complete); err != nil {
		return transcript.Entry{}, fmt.Errorf("read hosted Transcript entry detail: %w", err)
	}
	if complete.ID != preview.ID || complete.Seq != preview.Seq || complete.Truncated {
		return transcript.Entry{}, errorsInvalidTranscriptPage("Transcript detail does not match its preview")
	}
	return complete, nil
}

func taskTranscriptPath(run RunRef) string {
	return "/api/projects/" + url.PathEscape(run.ProjectID) + "/tasks/" + url.PathEscape(run.TaskID) + "/transcript"
}

func transcriptEntryIDs(entries []transcript.Entry) map[string]struct{} {
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		seen[entry.ID] = struct{}{}
	}
	return seen
}

func validateTranscriptOrder(entries []transcript.Entry, after int) error {
	last := after
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.ID) == "" {
			return errorsInvalidTranscriptPage("entry has no stable ID")
		}
		if _, duplicate := seen[entry.ID]; duplicate {
			return errorsInvalidTranscriptPage("entry ID is duplicated within one page")
		}
		seen[entry.ID] = struct{}{}
		if entry.Seq < last || (after >= 0 && entry.Seq <= after) {
			return errorsInvalidTranscriptPage("entries are not in sequence order")
		}
		last = entry.Seq
	}
	return nil
}

func oldestTranscriptSeq(entries []transcript.Entry) int {
	if len(entries) == 0 {
		return 0
	}
	oldest := entries[0].Seq
	for _, entry := range entries[1:] {
		if entry.Seq < oldest {
			oldest = entry.Seq
		}
	}
	return oldest
}

func errorsInvalidTranscriptPage(detail string) error {
	return fmt.Errorf("invalid hosted Transcript page: %s", detail)
}

// exactMasker masks only the two known hosted credential values. It does not
// apply shape-based secret detection because unrelated Transcript content must
// remain unchanged. JSON conversion provides a deep copy before replacement.
type exactMasker struct {
	replacer *strings.Replacer
}

func newExactMasker(secrets []string) *exactMasker {
	unique := make(map[string]struct{}, len(secrets))
	kept := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if _, exists := unique[secret]; exists {
			continue
		}
		unique[secret] = struct{}{}
		kept = append(kept, secret)
	}
	sort.SliceStable(kept, func(i, j int) bool { return len(kept[i]) > len(kept[j]) })
	pairs := make([]string, 0, len(kept)*2)
	for _, secret := range kept {
		pairs = append(pairs, secret, maskedHostedCredential)
	}
	return &exactMasker{replacer: strings.NewReplacer(pairs...)}
}

func (masker *exactMasker) marshal(entry transcript.Entry) ([]byte, error) {
	raw, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	document = masker.mask(document)
	return json.Marshal(document)
}

func (masker *exactMasker) mask(value any) any {
	switch typed := value.(type) {
	case string:
		return masker.replacer.Replace(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = masker.mask(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[masker.replacer.Replace(key)] = masker.mask(item)
		}
		return out
	default:
		return typed
	}
}
