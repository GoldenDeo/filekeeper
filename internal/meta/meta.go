// Package meta stores file records in SQLite. A record ("attachment") points
// at an immutable content blob by sha256; blobs are shared between records,
// so blob deletion is refcounted by the caller via CountBySHA.
package meta

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a file record does not exist.
var ErrNotFound = errors.New("meta: not found")

type File struct {
	ID         string    `json:"id"`
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	MIME       string    `json:"mime"`
	Filename   string    `json:"filename,omitempty"`
	Visibility string    `json:"visibility"` // "public" | "private"
	Service    string    `json:"service"`    // uploading service name
	CreatedAt  time.Time `json:"created_at"`
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	// WAL: readers never block the single writer; busy_timeout instead of
	// immediate SQLITE_BUSY under concurrent handlers.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	const schema = `
CREATE TABLE IF NOT EXISTS files (
	id         TEXT PRIMARY KEY,
	sha256     TEXT NOT NULL,
	size       INTEGER NOT NULL,
	mime       TEXT NOT NULL,
	filename   TEXT NOT NULL DEFAULT '',
	visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public','private')),
	service    TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_files_sha256 ON files(sha256);`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Insert(ctx context.Context, f File) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO files (id, sha256, size, mime, filename, visibility, service, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.SHA256, f.Size, f.MIME, f.Filename, f.Visibility, f.Service,
		f.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert %s: %w", f.ID, err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, id string) (File, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, sha256, size, mime, filename, visibility, service, created_at
		 FROM files WHERE id = ?`, id)
	var f File
	var created string
	err := row.Scan(&f.ID, &f.SHA256, &f.Size, &f.MIME, &f.Filename, &f.Visibility, &f.Service, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, fmt.Errorf("get %s: %w", id, err)
	}
	f.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return f, nil
}

// Delete removes the record and returns it, so the caller can decide whether
// the underlying blob became unreferenced.
func (s *Store) Delete(ctx context.Context, id string) (File, error) {
	f, err := s.Get(ctx, id)
	if err != nil {
		return File{}, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, id); err != nil {
		return File{}, fmt.Errorf("delete %s: %w", id, err)
	}
	return f, nil
}

// CountBySHA reports how many records reference a content blob.
func (s *Store) CountBySHA(ctx context.Context, sha string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE sha256 = ?`, sha).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count sha %s: %w", sha, err)
	}
	return n, nil
}
