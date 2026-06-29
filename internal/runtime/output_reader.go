package runtime

import (
	"bufio"
	"io"
	"strings"

	"pentest/internal/adapters"
	"pentest/internal/runtimeoutput"
	"pentest/internal/task"
)

// maxRuntimeOutputLineBytes matches multica's codex stdout scanner cap so a
// single stream-json line cannot abort the reader goroutine and stall the
// provider on a full pipe.
const maxRuntimeOutputLineBytes = 10 * 1024 * 1024

// ReadBoundedLine reads one line from br, without the newline terminator.
// Lines longer than maxBytes are truncated for emission and the remainder is
// discarded up to the next newline so reading can continue. Callers that read
// multiple lines must reuse the same *bufio.Reader.
func ReadBoundedLine(br *bufio.Reader, maxBytes int) (line string, truncated bool, err error) {
	if maxBytes <= 0 {
		return "", false, io.ErrShortBuffer
	}
	if br == nil {
		return "", false, io.ErrUnexpectedEOF
	}
	var buf strings.Builder
	for {
		b, err := br.ReadByte()
		if err != nil {
			if err == io.EOF {
				if buf.Len() == 0 {
					return "", false, io.EOF
				}
				return buf.String(), truncated, nil
			}
			return buf.String(), truncated, err
		}
		if b == '\n' {
			return strings.TrimRight(buf.String(), "\r"), truncated, nil
		}
		if buf.Len() >= maxBytes {
			truncated = true
			for b != '\n' {
				b, err = br.ReadByte()
				if err != nil {
					if err == io.EOF {
						return buf.String(), truncated, nil
					}
					return buf.String(), truncated, err
				}
			}
			return strings.TrimRight(buf.String(), "\r"), truncated, nil
		}
		buf.WriteByte(b)
	}
}

// ScanOutput reads provider stdout/stderr line-by-line and emits runtime_output
// events. It keeps draining after oversized lines instead of failing the
// reader with bufio.Scanner's default token cap.
func ScanOutput(reader io.Reader, stream string, maxLineBytes int, emit func(task.EventKind, task.EventPayload)) {
	if maxLineBytes <= 0 {
		maxLineBytes = maxRuntimeOutputLineBytes
	}
	br := bufio.NewReader(reader)
	for {
		line, truncated, err := ReadBoundedLine(br, maxLineBytes)
		if line != "" {
			if !runtimeoutput.ShouldIgnoreForStorage(line) {
				payload := task.EventPayload{
					"stream": stream,
					"text":   line,
				}
				if truncated {
					payload["truncated"] = true
				}
				emit(task.EventKindRuntimeOutput, adapters.Redact(payload))
			}
		}
		if err != nil {
			if err != io.EOF {
				emit(task.EventKindRuntimeOutput, adapters.Redact(task.EventPayload{
					"stream": stream,
					"text":   "read " + stream + ": " + err.Error(),
				}))
			}
			return
		}
	}
}