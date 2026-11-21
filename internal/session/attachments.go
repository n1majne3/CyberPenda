package session

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type copiedAttachment struct {
	Filename string
	Size     int64
	SHA256   string
}

func copyAttachments(workdir string, input []Attachment) ([]copiedAttachment, error) {
	names, err := resolveAttachmentNames(workdir, input)
	if err != nil {
		return nil, err
	}
	result := make([]copiedAttachment, 0, len(names))
	created := make([]string, 0, len(names))
	reserved := reservedAttachmentNames(names)
	cleanup := func() {
		for _, name := range created {
			_ = os.Remove(name)
		}
	}
	for index, item := range names {
		if item.Source.Open == nil {
			cleanup()
			return nil, fmt.Errorf("%w: %q has no source", ErrInvalidAttachment, item.Filename)
		}
		reader, err := item.Source.Open()
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("open Session attachment %q: %w", item.Filename, err)
		}
		if item.Source.Size > MaxAttachmentFileBytes {
			_ = reader.Close()
			cleanup()
			return nil, fmt.Errorf("%w: attachment %q exceeds the %d MiB limit", ErrInvalidAttachment, item.Filename, MaxAttachmentFileBytes>>20)
		}
		temporary, err := os.CreateTemp(workdir, fmt.Sprintf(".session-attachment-%d-*", index))
		if err != nil {
			_ = reader.Close()
			cleanup()
			return nil, fmt.Errorf("stage Session attachment %q: %w", item.Filename, err)
		}
		_ = temporary.Chmod(0o600)
		digest := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(reader, MaxAttachmentFileBytes+1))
		closeReaderErr := reader.Close()
		closeFileErr := temporary.Close()
		if copyErr != nil || closeReaderErr != nil || closeFileErr != nil || written > MaxAttachmentFileBytes {
			_ = os.Remove(temporary.Name())
			cleanup()
			if written > MaxAttachmentFileBytes {
				return nil, fmt.Errorf("%w: attachment %q exceeds the %d MiB limit", ErrInvalidAttachment, item.Filename, MaxAttachmentFileBytes>>20)
			}
			return nil, fmt.Errorf("write Session attachment %q: %v", item.Filename, firstError(copyErr, closeReaderErr, closeFileErr))
		}
		publishedName, err := publishStagedAttachment(workdir, item.Filename, temporary.Name(), reserved)
		if err != nil {
			_ = os.Remove(temporary.Name())
			cleanup()
			return nil, fmt.Errorf("publish Session attachment %q: %w", item.Filename, err)
		}
		destination := filepath.Join(workdir, publishedName)
		created = append(created, destination)
		result = append(result, copiedAttachment{Filename: publishedName, Size: written, SHA256: hex.EncodeToString(digest.Sum(nil))})
	}
	return result, nil
}

func reservedAttachmentNames(names []namedAttachment) map[string]struct{} {
	reserved := make(map[string]struct{}, len(names))
	for _, item := range names {
		reserved[attachmentNameCollisionKey(item.Filename)] = struct{}{}
	}
	return reserved
}

// publishStagedAttachment reserves the destination with O_EXCL before copying
// staged bytes. A concurrent upload can therefore force a new unique name but
// can never replace an existing attachment.
func publishStagedAttachment(workdir, preferredName, temporaryPath string, reserved map[string]struct{}) (string, error) {
	name := preferredName
	for {
		destination := filepath.Join(workdir, name)
		if filepath.Dir(destination) != filepath.Clean(workdir) {
			return "", fmt.Errorf("%w: %q", ErrInvalidAttachment, name)
		}
		output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			reserved[attachmentNameCollisionKey(name)] = struct{}{}
			name = uniqueAttachmentName(workdir, preferredName, reserved)
			continue
		}
		if err != nil {
			return "", err
		}
		input, openErr := os.Open(temporaryPath)
		if openErr != nil {
			_ = output.Close()
			_ = os.Remove(destination)
			return "", openErr
		}
		_, copyErr := io.Copy(output, input)
		closeInputErr := input.Close()
		closeOutputErr := output.Close()
		if copyErr != nil || closeInputErr != nil || closeOutputErr != nil {
			_ = os.Remove(destination)
			return "", firstError(copyErr, closeInputErr, closeOutputErr)
		}
		if err := os.Remove(temporaryPath); err != nil {
			_ = os.Remove(destination)
			return "", err
		}
		reserved[attachmentNameCollisionKey(name)] = struct{}{}
		return name, nil
	}
}

type namedAttachment struct {
	Source   Attachment
	Filename string
}

func resolveAttachmentNames(workdir string, input []Attachment) ([]namedAttachment, error) {
	result := make([]namedAttachment, 0, len(input))
	used := make(map[string]struct{}, len(input))
	for _, attachment := range input {
		name := sanitizeAttachmentFilename(attachment.Name)
		if name == "" {
			return nil, fmt.Errorf("%w: %q has no usable filename", ErrInvalidAttachment, attachment.Name)
		}
		if attachment.Size < 0 {
			return nil, fmt.Errorf("%w: %q has an invalid size", ErrInvalidAttachment, name)
		}
		final := name
		if _, exists := used[attachmentNameCollisionKey(final)]; exists {
			final = uniqueAttachmentName(workdir, final, used)
		}
		if _, err := os.Stat(filepath.Join(workdir, final)); err == nil {
			final = uniqueAttachmentName(workdir, final, used)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: inspect existing filename %q: %v", ErrInvalidAttachment, final, err)
		}
		if _, exists := used[attachmentNameCollisionKey(final)]; exists {
			final = uniqueAttachmentName(workdir, final, used)
		}
		if _, exists := used[attachmentNameCollisionKey(final)]; exists {
			return nil, fmt.Errorf("%w: filename collision for %q", ErrInvalidAttachment, final)
		}
		used[attachmentNameCollisionKey(final)] = struct{}{}
		result = append(result, namedAttachment{Source: attachment, Filename: final})
	}
	return result, nil
}

func uniqueAttachmentName(workdir, name string, used map[string]struct{}) string {
	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	for suffix := 1; ; suffix++ {
		candidate := fmt.Sprintf("%s-%d%s", stem, suffix, extension)
		if _, exists := used[attachmentNameCollisionKey(candidate)]; exists {
			continue
		}
		if _, err := os.Stat(filepath.Join(workdir, candidate)); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
}

func attachmentNameCollisionKey(name string) string {
	return strings.ToLower(name)
}

func sanitizeAttachmentFilename(name string) string {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	name = path.Base(name)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	return name
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return errors.New("unknown attachment write error")
}
