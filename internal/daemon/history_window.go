package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"pentest/internal/timeline"
	"pentest/internal/transcript"
)

// History window bounds for Runtime Owner Timeline and Transcript projections
// (#202). The initial read returns the newest recent window instead of the
// complete history; older content stays reachable through backward paging with
// an owner-local `before` cursor, and one oversized item cannot defeat the
// serialized byte bound because it is truncated to a bounded preview with an
// explicit detail reference.
const (
	// historyWindowMaxItems bounds the window and each backward page by item
	// count.
	historyWindowMaxItems = 200
	// historyWindowMaxBytes bounds the window and each backward page by
	// serialized size. An item whose full serialized form exceeds this budget
	// is returned as a truncated preview plus a detail reference instead of
	// forcing its full payload into the page.
	historyWindowMaxBytes = 256 << 10
	// historyPreviewChars is the per-field cap used when truncating an
	// oversized item to its bounded preview.
	historyPreviewChars = 4000
	// historyPreviewMapEntries caps the number of map entries retained in a
	// truncated preview so a pathological payload cannot grow the preview
	// without bound.
	historyPreviewMapEntries = 200
	// historyPreviewKeyChars caps map keys retained in a truncated preview.
	historyPreviewKeyChars = 512
	// historyEventQueryLimit bounds the storage rows read to build one owner
	// history response. Projection filtering happens in SQL before this limit.
	historyEventQueryLimit = historyWindowMaxItems * 4
)

// historyRequest describes one Timeline or Transcript read. Cursor presence is
// separate from cursor numeric value: an explicit `after=0` is a live-tail
// delta after the synthetic seq-0 goal row, never a new full read, while an
// absent `after` is the initial bounded window.
type historyRequest struct {
	afterSet  bool
	after     int
	beforeSet bool
	before    int
}

// parseHistoryRequest reads the optional after and before cursors. `before`
// (backward paging) takes precedence when both are present; the client never
// sends both.
func parseHistoryRequest(request *http.Request) historyRequest {
	var req historyRequest
	if raw := strings.TrimSpace(request.URL.Query().Get("after")); raw != "" {
		if cursor, err := strconv.Atoi(raw); err == nil && cursor >= 0 {
			req.afterSet = true
			req.after = cursor
		}
	}
	if raw := strings.TrimSpace(request.URL.Query().Get("before")); raw != "" {
		if cursor, err := strconv.Atoi(raw); err == nil && cursor > 0 {
			req.beforeSet = true
			req.before = cursor
		}
	}
	return req
}

// historyPage is the shared Timeline/Transcript page result.
type historyPage[T any] struct {
	items    []T
	cursor   int
	hasOlder bool
}

type timelineEventChunk struct {
	events     []timeline.Event
	cursor     int
	hasOlder   bool
	hasNewer   bool
	scanCursor int
}

// collectTimelineItems reads bounded Event chunks until it has enough visible
// Timeline items. Provider progress records can be valid retained Events that
// intentionally project to no Timeline item. Such records must not make an
// initial or backward page appear empty while older visible history exists.
func collectTimelineItems(req historyRequest, fetch func(historyRequest) (timelineEventChunk, error)) ([]timeline.Item, timelineEventChunk, error) {
	scanReq := req
	items := make([]timeline.Item, 0, historyWindowMaxItems)
	var result timelineEventChunk
	for {
		chunk, err := fetch(scanReq)
		if err != nil {
			return nil, timelineEventChunk{}, err
		}
		result = chunk
		projected := timeline.Build(chunk.events)
		if req.afterSet {
			items = append(items, projected...)
		} else {
			items = append(projected, items...)
		}

		if req.afterSet {
			if len(items) > 0 || !chunk.hasNewer || chunk.scanCursor <= scanReq.after {
				break
			}
			scanReq.afterSet = true
			scanReq.after = chunk.scanCursor
			continue
		}
		if len(items) >= historyWindowMaxItems || !chunk.hasOlder || len(chunk.events) == 0 {
			break
		}
		scanReq.beforeSet = true
		scanReq.before = chunk.events[0].Seq
		scanReq.afterSet = false
	}
	return items, result, nil
}

// initialHistoryPage returns the newest bounded recent window of the full
// projection in chronological order. cursor is the maximum Seq of the full
// projection so live-tail `after` polls never miss items between the window
// edge and the tail. hasOlder reports whether items older than the window
// still exist. Each selected item passes through bounded so an oversized item
// becomes a bounded preview in the page itself.
func initialHistoryPage[T any](items []T, seqOf func(T) int, bounded func(T) (T, int)) historyPage[T] {
	cursor := 0
	for _, item := range items {
		if itemSeq := seqOf(item); itemSeq > cursor {
			cursor = itemSeq
		}
	}
	if len(items) == 0 {
		return historyPage[T]{items: []T{}, cursor: cursor}
	}
	newest := boundedNewest(items, len(items), seqOf, bounded)
	return historyPage[T]{
		items:    reversed(newest),
		cursor:   cursor,
		hasOlder: len(newest) < len(items),
	}
}

// olderHistoryPage returns the newest bounded page of items with Seq strictly
// below before, in chronological order. hasOlder reports whether items older
// than the returned page still exist.
func olderHistoryPage[T any](items []T, before int, seqOf func(T) int, bounded func(T) (T, int)) historyPage[T] {
	cursor := 0
	for _, item := range items {
		if itemSeq := seqOf(item); itemSeq > cursor {
			cursor = itemSeq
		}
	}
	limit := len(items)
	for limit > 0 && seqOf(items[limit-1]) >= before {
		limit--
	}
	newest := boundedNewest(items[:limit], limit, seqOf, bounded)
	return historyPage[T]{
		items:    reversed(newest),
		cursor:   cursor,
		hasOlder: len(newest) < limit,
	}
}

// boundedNewest scans the newest items first and keeps them until the item
// count or serialized byte bound is reached. Each kept item is replaced by its
// bounded form (an oversized item becomes a bounded preview with a detail
// reference), so the byte accounting matches the bytes actually returned. It
// returns the selected items newest-first; the caller reverses them into
// chronological order. A page boundary never splits one event's same-Seq group
// (several transcript entries can share the Seq of one parsed provider
// record), so backward paging cannot lose part of an event.
func boundedNewest[T any](items []T, limit int, seqOf func(T) int, bounded func(T) (T, int)) []T {
	if limit == 0 {
		return nil
	}
	selected := make([]T, 0, min(limit, historyWindowMaxItems)+1)
	totalBytes := 0
	for index := limit - 1; index >= 0; index-- {
		if len(selected) >= historyWindowMaxItems {
			break
		}
		item, size := bounded(items[index])
		// The newest item is never silently dropped from the page: its preview
		// is per-field bounded, and when the page is still empty the item is
		// included even if its preview alone fills the whole byte budget.
		if totalBytes+size > historyWindowMaxBytes && len(selected) > 0 {
			break
		}
		selected = append(selected, item)
		totalBytes += size
	}
	// Extend across the full same-Seq group of the oldest selected item so
	// the boundary never falls inside one event. Group atomicity wins over
	// the exact count and byte bounds; the bounded preview still applies.
	if len(selected) > 0 {
		groupSeq := seqOf(selected[len(selected)-1])
		for index := limit - 1 - len(selected); index >= 0; index-- {
			if seqOf(items[index]) != groupSeq {
				break
			}
			item, _ := bounded(items[index])
			selected = append(selected, item)
		}
	}
	return selected
}

func reversed[T any](items []T) []T {
	out := make([]T, len(items))
	for index := range items {
		out[len(items)-1-index] = items[index]
	}
	return out
}

// historyResponseFor selects the projection page for one request: an explicit
// `after` is a live-tail delta, an explicit `before` is a backward page, and
// neither is the initial bounded window.
func historyResponseFor[T any](items []T, req historyRequest, seqOf func(T) int, bounded func(T) (T, int)) historyPage[T] {
	if req.beforeSet {
		return olderHistoryPage(items, req.before, seqOf, bounded)
	}
	if req.afterSet {
		delta, cursor := projectionDelta(items, req.after, seqOf)
		return historyPage[T]{items: delta, cursor: cursor}
	}
	return initialHistoryPage(items, seqOf, bounded)
}

// projectionDelta returns the ordered projection items strictly after the
// committed cursor plus the new cursor, which is the maximum Seq of the full
// projection and never regresses below the committed cursor. Unlike the
// initial window, an explicit after=0 request returns only later items and
// never resends the synthetic seq-0 goal row.
func projectionDelta[T any](items []T, after int, seq func(T) int) ([]T, int) {
	cursor := after
	delta := make([]T, 0, len(items))
	for _, item := range items {
		if itemSeq := seq(item); itemSeq > cursor {
			cursor = itemSeq
		}
		if seq(item) > after {
			delta = append(delta, item)
		}
	}
	return delta, cursor
}

// boundedTimelineItem returns the item and its serialized byte size, truncating
// an oversized item to a bounded preview with an explicit truncation marker and
// an owner-authorized detail reference. The complete retained content stays
// available through the referenced detail endpoint.
func boundedTimelineItem(item timeline.Item, detailBase string) (timeline.Item, int) {
	raw, err := json.Marshal(item)
	if err != nil || len(raw) <= historyWindowMaxBytes {
		return item, len(raw)
	}
	preview := item
	preview.Content = truncateField(preview.Content)
	preview.Output = truncateField(preview.Output)
	preview.Input = truncateMap(preview.Input)
	preview.Truncated = true
	detailID := item.ID
	if detailID == "" {
		detailID = strconv.Itoa(item.Seq)
	}
	preview.Detail = fmt.Sprintf("%s/%s", detailBase, url.PathEscape(detailID))
	sized, _ := json.Marshal(preview)
	return preview, len(sized)
}

// boundedTranscriptEntry is the transcript counterpart of boundedTimelineItem.
func boundedTranscriptEntry(entry transcript.Entry, detailBase string) (transcript.Entry, int) {
	raw, err := json.Marshal(entry)
	if err != nil || len(raw) <= historyWindowMaxBytes {
		return entry, len(raw)
	}
	preview := entry
	preview.Text = truncateField(preview.Text)
	preview.Details = truncateMap(preview.Details)
	preview.Truncated = true
	preview.Detail = fmt.Sprintf("%s/%s", detailBase, entry.ID)
	sized, _ := json.Marshal(preview)
	return preview, len(sized)
}

// truncateField caps one text field at the preview length, keeping a visible
// marker so the bounded summary reads as a truncation.
func truncateField(text string) string {
	if len(text) <= historyPreviewChars {
		return text
	}
	return text[:historyPreviewChars] + "\n… (truncated)"
}

// truncateKey caps one payload map key so a pathological key cannot grow the
// serialized preview without bound.
func truncateKey(key string) string {
	if len(key) <= historyPreviewKeyChars {
		return key
	}
	return key[:historyPreviewKeyChars] + "…"
}

// truncateMap bounds every string value in a payload map, recursively, and
// caps the number of retained entries and key lengths so a pathological
// payload cannot defeat the preview bound.
func truncateMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return values
	}
	out := make(map[string]any, min(len(values), historyPreviewMapEntries))
	kept := 0
	for key, value := range values {
		if kept >= historyPreviewMapEntries {
			break
		}
		out[truncateKey(key)] = truncateValue(value)
		kept++
	}
	return out
}

func truncateSlice(values []any) []any {
	out := make([]any, 0, min(len(values), historyPreviewMapEntries))
	for _, value := range values {
		if len(out) >= historyPreviewMapEntries {
			break
		}
		out = append(out, truncateValue(value))
	}
	return out
}

func truncateValue(value any) any {
	switch typed := value.(type) {
	case string:
		return truncateField(typed)
	case map[string]any:
		return truncateMap(typed)
	case []any:
		return truncateSlice(typed)
	default:
		return typed
	}
}
