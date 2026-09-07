package fgs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"pentest/internal/owner"
)

var continuationPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)
var filenamePattern = regexp.MustCompile(`^intent_([0-9]{8})\.json$`)

type DrainResult struct {
	Receipts []Receipt `json:"receipts"`
	Blocked  bool      `json:"blocked"`
}

// Emit publishes a local update. It does not report Blackboard acceptance.
func Emit(ctx context.Context, c owner.Contract, continuation string, operations []Operation) (Update, error) {
	return emit(ctx, c, continuation, operations, nil, "")
}

func EmitResolution(ctx context.Context, c owner.Contract, continuation string, target Identity, operations []Operation, withdrawalReason string) (Update, error) {
	return emit(ctx, c, continuation, operations, &target, withdrawalReason)
}

func emit(ctx context.Context, c owner.Contract, continuation string, operations []Operation, target *Identity, withdrawalReason string) (Update, error) {
	root, err := openMailbox(c, continuation)
	if err != nil {
		return Update{}, err
	}
	defer root.Close()
	outbox := filepath.Join("graph", "outbox", continuation)
	release, err := mailboxLock(ctx, root, filepath.Join(outbox, ".publish.lock"))
	if err != nil {
		return Update{}, err
	}
	defer release()
	files, err := intentFiles(root, outbox)
	if err != nil {
		return Update{}, err
	}
	next := 1
	if len(files) > 0 {
		n, _ := strconv.Atoi(filenamePattern.FindStringSubmatch(files[len(files)-1])[1])
		next = n + 1
	}
	if next > 99999999 {
		return Update{}, errors.New("FGS Outbox sequence exhausted")
	}
	u := Update{Schema: Schema, ID: fmt.Sprintf("intent_%08d", next), Sequence: next, Operations: operations, Resolves: target, WithdrawalReason: withdrawalReason}
	raw, err := json.Marshal(u)
	if err != nil {
		return Update{}, err
	}
	if (len(operations) == 0 && (target == nil || strings.TrimSpace(withdrawalReason) == "")) || len(operations) > 100 || len(raw) > MaxUpdateSize {
		return Update{}, errors.New("FGS update exceeds operation or byte limits")
	}
	for _, op := range operations {
		if err = validateOperation(op); err != nil {
			return Update{}, err
		}
	}
	if err = publish(root, filepath.Join(outbox, u.ID+".json"), raw, false); err != nil {
		return Update{}, err
	}
	return u, nil
}

// Drain accepts canonical files in order and restores missing Receipt files.
// Lifecycle adapters must keep calls to this boundary owner-scoped.
func (s *Service) Drain(ctx context.Context, c owner.Contract, continuation string) (DrainResult, error) {
	result := DrainResult{Receipts: []Receipt{}}
	root, err := openMailbox(c, continuation)
	if err != nil {
		return result, err
	}
	defer root.Close()
	outbox := filepath.Join("graph", "outbox", continuation)
	release, err := mailboxLock(ctx, root, filepath.Join("graph", ".settle.lock"))
	if err != nil {
		return result, err
	}
	defer release()
	files, err := intentFiles(root, outbox)
	if err != nil {
		return result, err
	}
	return s.drainFiles(ctx, c, continuation, root, outbox, files)
}

func (s *Service) drainFiles(ctx context.Context, c owner.Contract, continuation string, root *os.Root, outbox string, files []string) (DrainResult, error) {
	result := DrainResult{Receipts: []Receipt{}}
	var err error
	replayedRepairs := map[string]bool{}
	for pass := 0; pass <= len(files); pass++ {
		result = DrainResult{Receipts: []Receipt{}}
		repaired := false
		for _, name := range files {
			if err = ctx.Err(); err != nil {
				return result, err
			}
			raw, err := readRegular(root, filepath.Join(outbox, name), MaxUpdateSize)
			if err != nil {
				info, statErr := root.Lstat(filepath.Join(outbox, name))
				if statErr != nil {
					return result, err
				}
				if info.Mode().IsRegular() && info.Size() <= MaxUpdateSize {
					return result, err
				}
				// Do not read a symlink target or oversized body. Fingerprint only the
				// entry metadata so its immutable identity can be rejected and withdrawn.
				raw = []byte(fmt.Sprintf("invalid-file:%s:%d:%d", info.Mode(), info.Size(), info.ModTime().UnixNano()))
			}
			u, err := DecodeUpdate(raw)
			if err != nil || u.ID+".json" != name || u.Schema != Schema || u.ID != fmt.Sprintf("intent_%08d", u.Sequence) {
				// Retain a stable fingerprint in a rejected update. The published file
				// remains unchanged; repair and withdrawal use the normal receipt protocol.
				sequence, _ := strconv.Atoi(filenamePattern.FindStringSubmatch(name)[1])
				digest := sha256.Sum256(raw)
				u = Update{Schema: Schema, ID: strings.TrimSuffix(name, ".json"), Sequence: sequence, Operations: []Operation{{Op: "transport.invalid", Key: "transport:invalid", Body: hex.EncodeToString(digest[:])}}}
			}
			r, err := s.Apply(ctx, c, continuation, u)
			if errors.Is(err, ErrBlocked) {
				result.Blocked = true
				// Keep scanning: a later file can repair this rejected head.
				continue
			}
			if err != nil {
				return result, err
			}
			// Apply and its durable receipt commit before file delivery.
			receipts := filepath.Join("graph", "receipts", continuation)
			if err = mkdirUnder(root, receipts); err != nil {
				return result, err
			}
			body, err := json.Marshal(r)
			if err != nil {
				return result, err
			}
			if err = publish(root, filepath.Join(receipts, name), body, true); err != nil {
				return result, err
			}
			result.Receipts = append(result.Receipts, r)
			if r.State == "action_required" && u.Resolves == nil {
				result.Blocked = true
			}
			if r.State == "applied" && u.Resolves != nil && !replayedRepairs[r.ID] {
				// Repeat once per applied repair to retry earlier queued work
				// and refresh the original rejected receipt after supersession.
				replayedRepairs[r.ID] = true
				repaired = true
				break
			}
		}
		if !repaired {
			return result, nil
		}
	}
	return result, errors.New("FGS repair scan did not settle")
}

func DecodeUpdate(raw []byte) (Update, error) {
	var u Update
	if len(raw) > MaxUpdateSize {
		return u, errors.New("FGS update exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&u); err != nil {
		return u, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return u, errors.New("FGS update must contain one JSON object")
	}
	return u, nil
}

func openMailbox(c owner.Contract, continuation string) (*os.Root, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !continuationPattern.MatchString(continuation) || !filepath.IsAbs(c.Workdir) {
		return nil, errors.New("FGS requires an absolute workdir and a safe Continuation id")
	}
	// The workdir is a server binding. Internal directories are opened below it.
	info, err := os.Lstat(c.Workdir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("FGS workdir must be a real directory")
	}
	root, err := os.OpenRoot(c.Workdir)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, errors.New("FGS workdir changed while opening")
	}
	for _, p := range []string{filepath.Join("graph", "outbox", continuation), filepath.Join("graph", "receipts")} {
		if err = mkdirUnder(root, p); err != nil {
			root.Close()
			return nil, err
		}
	}
	return root, nil
}

func mkdirUnder(root *os.Root, path string) error {
	current := ""
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := root.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("FGS path is not a real directory: %s", current)
		}
	}
	return nil
}

func intentFiles(root *os.Root, path string) ([]string, error) {
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range entries {
		name := e.Name()
		if name == ".publish.lock" || strings.HasSuffix(name, ".tmp") {
			continue
		}
		if !filenamePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid FGS Outbox entry %s", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func readRegular(root *os.Root, path string, limit int) ([]byte, error) {
	before, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("FGS path is not a regular file: %s", path)
	}
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, errors.New("FGS file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, errors.New("FGS file exceeds byte limit")
	}
	return raw, nil
}

func publish(root *os.Root, path string, body []byte, replace bool) error {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	temp := path + "." + hex.EncodeToString(token) + ".tmp"
	f, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	if _, err = f.Write(append(body, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if replace {
		err = root.Rename(temp, path)
	} else {
		err = root.Link(temp, path)
	}
	if err != nil {
		return err
	}
	// Only our staged file is removed. Published intents are never replaced.
	if !replace {
		if err = root.Remove(temp); err != nil {
			return err
		}
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := root.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func mailboxLock(ctx context.Context, root *os.Root, path string) (func(), error) {
	info, err := root.Lstat(path)
	if err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("FGS lock must be a regular file")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := root.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if errors.Is(err, os.ErrExist) {
		f, err = root.OpenFile(path, os.O_RDWR, 0600)
	}
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("invalid FGS lock file")
	}
	if err = lockFile(ctx, f); err != nil {
		f.Close()
		return nil, err
	}
	return func() { unlockFile(f); f.Close() }, nil
}
