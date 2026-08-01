package skill

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

const (
	maxArchiveFiles         = 1024
	maxArchiveFileBytes     = 16 << 20
	maxArchiveExpandedBytes = 64 << 20
)

func (s *Service) ImportArchive(ctx context.Context, filename string, raw []byte) (Skill, error) {
	files, rootName, err := readArchiveBundle(filename, raw)
	if err != nil {
		return Skill{}, err
	}
	metadata, err := archiveMetadata(filename, rootName, files["SKILL.md"])
	if err != nil {
		return Skill{}, err
	}
	return s.Publish(ctx, PublishRequest{Metadata: metadata, Files: files})
}

func readArchiveBundle(filename string, raw []byte) (map[string]string, string, error) {
	lower := strings.ToLower(strings.TrimSpace(filename))
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return readZIPBundle(raw)
	case strings.HasSuffix(lower, ".tar"):
		return readTARBundle(bytes.NewReader(raw))
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		reader, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, "", fmt.Errorf("%w: invalid gzip-compressed TAR archive", ErrInvalidSkill)
		}
		defer reader.Close()
		return readTARBundle(reader)
	default:
		return nil, "", fmt.Errorf("%w: archive must be .zip, .tar, .tar.gz, or .tgz", ErrInvalidSkill)
	}
}

func readZIPBundle(raw []byte) (map[string]string, string, error) {
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, "", fmt.Errorf("%w: invalid ZIP archive", ErrInvalidSkill)
	}
	if len(reader.File) > maxArchiveFiles {
		return nil, "", fmt.Errorf("%w: archive contains too many files", ErrInvalidSkill)
	}
	archivedFiles := make(map[string]string, len(reader.File))
	var expandedBytes int64
	for _, entry := range reader.File {
		entryPath := strings.TrimSuffix(entry.Name, "/")
		if entryPath == "" {
			continue
		}
		if err := ValidateRelativeBundlePath(entryPath); err != nil {
			return nil, "", err
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return nil, "", fmt.Errorf("%w: archive must not contain symlink %q", ErrInvalidSkill, entry.Name)
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		if !entry.Mode().IsRegular() {
			return nil, "", fmt.Errorf("%w: archive entry %q must be a regular file", ErrInvalidSkill, entry.Name)
		}
		if entry.UncompressedSize64 > maxArchiveFileBytes {
			return nil, "", fmt.Errorf("%w: archive file %q is too large", ErrInvalidSkill, entry.Name)
		}
		expandedBytes += int64(entry.UncompressedSize64)
		if expandedBytes > maxArchiveExpandedBytes {
			return nil, "", fmt.Errorf("%w: archive expands beyond the allowed size", ErrInvalidSkill)
		}
		file, err := entry.Open()
		if err != nil {
			return nil, "", fmt.Errorf("%w: open archive file %q: %v", ErrInvalidSkill, entry.Name, err)
		}
		content, readErr := io.ReadAll(io.LimitReader(file, maxArchiveFileBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, "", fmt.Errorf("%w: read archive file %q: %v", ErrInvalidSkill, entry.Name, readErr)
		}
		if closeErr != nil {
			return nil, "", fmt.Errorf("%w: close archive file %q: %v", ErrInvalidSkill, entry.Name, closeErr)
		}
		if len(content) > maxArchiveFileBytes {
			return nil, "", fmt.Errorf("%w: archive file %q is too large", ErrInvalidSkill, entry.Name)
		}
		if _, exists := archivedFiles[entryPath]; exists {
			return nil, "", fmt.Errorf("%w: duplicate archive path %q", ErrInvalidSkill, entry.Name)
		}
		archivedFiles[entryPath] = string(content)
	}
	return normalizeArchivedBundle(archivedFiles)
}

func readTARBundle(source io.Reader) (map[string]string, string, error) {
	reader := tar.NewReader(source)
	archivedFiles := map[string]string{}
	var expandedBytes int64
	entries := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("%w: invalid TAR archive", ErrInvalidSkill)
		}
		entryPath := strings.TrimSuffix(header.Name, "/")
		if entryPath == "" {
			continue
		}
		entries++
		if entries > maxArchiveFiles {
			return nil, "", fmt.Errorf("%w: archive contains too many files", ErrInvalidSkill)
		}
		if err := ValidateRelativeBundlePath(entryPath); err != nil {
			return nil, "", err
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, "", fmt.Errorf("%w: archive entry %q must be a regular file", ErrInvalidSkill, header.Name)
		}
		if header.Size < 0 || header.Size > maxArchiveFileBytes {
			return nil, "", fmt.Errorf("%w: archive file %q is too large", ErrInvalidSkill, header.Name)
		}
		expandedBytes += header.Size
		if expandedBytes > maxArchiveExpandedBytes {
			return nil, "", fmt.Errorf("%w: archive expands beyond the allowed size", ErrInvalidSkill)
		}
		content, err := io.ReadAll(io.LimitReader(reader, maxArchiveFileBytes+1))
		if err != nil {
			return nil, "", fmt.Errorf("%w: read archive file %q: %v", ErrInvalidSkill, header.Name, err)
		}
		if len(content) > maxArchiveFileBytes {
			return nil, "", fmt.Errorf("%w: archive file %q is too large", ErrInvalidSkill, header.Name)
		}
		if _, exists := archivedFiles[entryPath]; exists {
			return nil, "", fmt.Errorf("%w: duplicate archive path %q", ErrInvalidSkill, header.Name)
		}
		archivedFiles[entryPath] = string(content)
	}
	return normalizeArchivedBundle(archivedFiles)
}

func normalizeArchivedBundle(archivedFiles map[string]string) (map[string]string, string, error) {
	instructionDocuments := 0
	for filePath := range archivedFiles {
		if filePath == "SKILL.md" || strings.HasSuffix(filePath, "/SKILL.md") {
			instructionDocuments++
		}
	}
	if instructionDocuments != 1 {
		return nil, "", fmt.Errorf("%w: archive must contain exactly one Skill bundle", ErrInvalidSkill)
	}
	if _, ok := archivedFiles["SKILL.md"]; ok {
		return archivedFiles, "", nil
	}
	rootName := ""
	for filePath := range archivedFiles {
		first, _, found := strings.Cut(filePath, "/")
		if !found {
			return nil, "", fmt.Errorf("%w: archive must contain one Skill bundle", ErrInvalidSkill)
		}
		if rootName == "" {
			rootName = first
		} else if rootName != first {
			return nil, "", fmt.Errorf("%w: archive must contain one Skill bundle", ErrInvalidSkill)
		}
	}
	if rootName == "" {
		return nil, "", fmt.Errorf("%w: archive is empty", ErrInvalidSkill)
	}
	prefix := rootName + "/"
	files := make(map[string]string, len(archivedFiles))
	for filePath, content := range archivedFiles {
		files[strings.TrimPrefix(filePath, prefix)] = content
	}
	if _, ok := files["SKILL.md"]; !ok {
		return nil, "", fmt.Errorf("%w: SKILL.md instruction document is required", ErrInvalidSkill)
	}
	return files, rootName, nil
}

func archiveMetadata(filename, rootName, instruction string) (Metadata, error) {
	name, description := parseSkillFrontMatter(instruction)
	id := strings.TrimSpace(name)
	if !idPattern.MatchString(id) {
		id = strings.TrimSpace(rootName)
	}
	if !idPattern.MatchString(id) {
		id = archiveBaseName(filename)
	}
	if !idPattern.MatchString(id) {
		return Metadata{}, fmt.Errorf("%w: archive needs a valid skill name in SKILL.md front matter or bundle directory", ErrInvalidSkill)
	}
	if strings.TrimSpace(name) == "" {
		name = id
	}
	return Metadata{
		ID:          id,
		Name:        name,
		Description: description,
		Source:      SourceProvenance{Kind: "archive"},
	}, nil
}

func archiveBaseName(filename string) string {
	base := path.Base(strings.TrimSpace(filename))
	lower := strings.ToLower(base)
	for _, suffix := range []string{".tar.gz", ".tgz", ".zip", ".tar"} {
		if strings.HasSuffix(lower, suffix) {
			return base[:len(base)-len(suffix)]
		}
	}
	return base
}
