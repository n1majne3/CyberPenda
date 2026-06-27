package runtime_test

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

func TestReadBoundedLineReturnsCompleteLine(t *testing.T) {
	line, truncated, err := runtime.ReadBoundedLine(bufio.NewReader(strings.NewReader("hello\nworld\n")), 1024)
	if err != nil || truncated || line != "hello" {
		t.Fatalf("line=%q truncated=%v err=%v", line, truncated, err)
	}
}

func TestReadBoundedLineTruncatesOversizedLineAndContinues(t *testing.T) {
	var b strings.Builder
	b.WriteString(strings.Repeat("x", 200))
	b.WriteByte('\n')
	b.WriteString("next\n")
	br := bufio.NewReader(bytes.NewReader([]byte(b.String())))

	line, truncated, err := runtime.ReadBoundedLine(br, 64)
	if err != nil || !truncated {
		t.Fatalf("line=%q truncated=%v err=%v", line, truncated, err)
	}
	if len(line) != 64 || !strings.HasPrefix(line, "xxx") {
		t.Fatalf("expected 64-byte prefix, got len=%d line=%q", len(line), line)
	}

	line, truncated, err = runtime.ReadBoundedLine(br, 64)
	if err != nil || truncated || line != "next" {
		t.Fatalf("second line=%q truncated=%v err=%v", line, truncated, err)
	}
}

func TestScanOutputEmitsTruncatedLineAndKeepsReading(t *testing.T) {
	payload := strings.Repeat("y", 128) + "\nok\n"
	var emitted []task.EventPayload
	runtime.ScanOutput(bytes.NewReader([]byte(payload)), "stdout", 64, func(_ task.EventKind, payload task.EventPayload) {
		emitted = append(emitted, payload)
	})

	if len(emitted) != 2 {
		t.Fatalf("expected 2 emitted events, got %d: %#v", len(emitted), emitted)
	}
	if emitted[0]["truncated"] != true {
		t.Fatalf("expected first line truncated, got %#v", emitted[0])
	}
	if text, _ := emitted[0]["text"].(string); len(text) != 64 {
		t.Fatalf("expected truncated text len 64, got %d", len(text))
	}
	if text, _ := emitted[1]["text"].(string); text != "ok" {
		t.Fatalf("expected second line ok, got %q", text)
	}
}

func TestReadBoundedLineReturnsEOFWithoutErrorForEmptyStream(t *testing.T) {
	line, truncated, err := runtime.ReadBoundedLine(bufio.NewReader(strings.NewReader("")), 1024)
	if err != io.EOF || truncated || line != "" {
		t.Fatalf("line=%q truncated=%v err=%v", line, truncated, err)
	}
}