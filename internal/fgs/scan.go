package fgs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"pentest/internal/owner"
)

type mailboxScan struct {
	root      *os.Root
	directory *os.File
	cursor    string
}

type ScanResult struct {
	DrainResult
	Complete bool `json:"complete"`
}

// ReceivePage bounds directory discovery and settlement separately. Discovery
// must reach EOF before settlement: directory iteration order is not sequence
// order. The durable inventory supplies sorted pages without a full in-memory
// directory listing. Restart repeats discovery; accepted receipts remain valid.
func (s *Service) ReceivePage(ctx context.Context, c owner.Contract, continuation string, limit int) (ScanResult, error) {
	result := ScanResult{DrainResult: DrainResult{Receipts: []Receipt{}}}
	if limit < 1 || limit > 64 {
		return result, errors.New("FGS scan page must be 1 to 64 entries")
	}
	if err := c.Validate(); err != nil {
		return result, err
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.scans == nil {
		s.scans = map[string]*mailboxScan{}
	}
	key := string(c.Kind) + ":" + c.ID + ":" + continuation
	scan := s.scans[key]
	outbox := filepath.Join("graph", "outbox", continuation)
	if scan == nil {
		if len(s.scans) >= 64 {
			return result, nil
		}
		root, err := openMailbox(c, continuation)
		if err != nil {
			return result, err
		}
		dir, err := root.Open(outbox)
		if err != nil {
			root.Close()
			return result, err
		}
		scan = &mailboxScan{root: root, directory: dir}
		s.scans[key] = scan
	}
	fail := func(err error) (ScanResult, error) { scan.close(); delete(s.scans, key); return result, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if scan.directory != nil {
		entries, err := scan.directory.ReadDir(limit)
		if err != nil && !errors.Is(err, io.EOF) {
			return fail(err)
		}
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return fail(e)
		}
		for _, entry := range entries {
			name := entry.Name()
			if name == ".publish.lock" || strings.HasSuffix(name, ".tmp") {
				continue
			}
			if !filenamePattern.MatchString(name) {
				tx.Rollback()
				return fail(fmt.Errorf("invalid FGS Outbox entry %s", name))
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO fgs_outbox_inventory(owner_kind,owner_id,continuation_id,name) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, c.Kind, c.ID, continuation, name); e != nil {
				tx.Rollback()
				return fail(e)
			}
		}
		if e = tx.Commit(); e != nil {
			return fail(e)
		}
		if !errors.Is(err, io.EOF) {
			return result, nil
		}
		scan.close()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM fgs_outbox_inventory WHERE owner_kind=? AND owner_id=? AND continuation_id=? AND name>? ORDER BY name LIMIT ?`, c.Kind, c.ID, continuation, scan.cursor, limit)
	if err != nil {
		return fail(err)
	}
	files := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		files = append(files, name)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return fail(err)
	}
	if rowErr != nil {
		return fail(rowErr)
	}
	root, err := openMailbox(c, continuation)
	if err != nil {
		return fail(err)
	}
	defer root.Close()
	release, err := mailboxLock(ctx, root, filepath.Join("graph", ".settle.lock"))
	if err != nil {
		return fail(err)
	}
	defer release()
	result.DrainResult, err = s.drainFiles(ctx, c, continuation, root, outbox, files)
	if err != nil {
		return fail(err)
	}
	if len(files) > 0 {
		scan.cursor = files[len(files)-1]
	}
	if len(files) < limit {
		result.Complete = true
		delete(s.scans, key)
	}
	return result, nil
}

func (scan *mailboxScan) close() {
	if scan.directory != nil {
		scan.directory.Close()
		scan.directory = nil
	}
	if scan.root != nil {
		scan.root.Close()
		scan.root = nil
	}
}

// CloseScans releases directory handles when the receiver shuts down.
func (s *Service) CloseScans() {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	for _, scan := range s.scans {
		scan.close()
	}
	s.scans = nil
}
