package blackboardv2

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"testing"

	"modernc.org/sqlite"
	"pentest/internal/store"
)

type auditRecordingDriver struct {
	conclusionRecordingDriver
}

type auditRecordingConn struct {
	conclusionRecordingConn
}

func (d auditRecordingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.conclusionRecordingDriver.Open(name)
	if err != nil {
		return nil, err
	}
	return auditRecordingConn{conn.(conclusionRecordingConn)}, nil
}

func (c auditRecordingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func newAuditQueryStore(t *testing.T) (*store.DB, *conclusionQueryRecorder) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit-queries.db")
	baseline, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := baseline.Close(); err != nil {
		t.Fatal(err)
	}
	recorder := &conclusionQueryRecorder{}
	name := fmt.Sprintf("audit-query-%d", conclusionQueryDriverSequence.Add(1))
	sql.Register(name, auditRecordingDriver{conclusionRecordingDriver{inner: &sqlite.Driver{}, recorder: recorder}})
	db, err := sql.Open(name, path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return &store.DB{DB: db}, recorder
}
