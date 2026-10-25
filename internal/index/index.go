// Package index is a disposable full-text index over one identity's memories.
//
// The memory files are the source of truth. This database is derived from
// them, is only ever written while reconciling against them, and can be
// deleted at any time: the next search rebuilds it.
package index

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"modernc.org/sqlite"
)

// Bump when the tables change; an index at another version is rebuilt.
const schemaVersion = 1

const (
	sqliteBusy   = 5
	sqliteLocked = 6
)

// File is a source file and the state it was in when listed.
type File struct {
	Name    string
	Size    int64
	ModTime int64
}

// Doc is the searchable form of one memory.
type Doc struct {
	ID      string
	Kind    string
	Scope   string
	Source  string
	Anchor  string
	Tags    []string
	Created time.Time
	Body    string
}

// Hit is a matching memory. A higher Score is a better match.
type Hit struct {
	Doc
	Score float64
}

// Query selects memories by words, optionally limited to scopes and a kind.
type Query struct {
	Text   string
	Scopes []string // empty means every scope
	Kind   string
	Limit  int
}

type Index struct{ db *sql.DB }

// Open opens the index in dir, creating or rebuilding it when needed.
func Open(dir string) (*Index, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// A store kept in version control should not carry its derived index.
	if ignore := filepath.Join(dir, ".gitignore"); !exists(ignore) {
		if err := os.WriteFile(ignore, []byte("*\n"), 0o600); err != nil {
			return nil, err
		}
	}
	file := url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(dir, "memories.sqlite"))}
	// Every write transaction takes the write lock up front. A deferred
	// transaction that later upgrades fails at once with SQLITE_BUSY, without
	// waiting for busy_timeout.
	dsn := file.String() + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	ix := &Index{db: db}
	if err := retryBusy(ix.migrate); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening index: %w", err)
	}
	return ix, nil
}

func (ix *Index) Close() error { return ix.db.Close() }

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (ix *Index) migrate() error {
	var version int
	if err := ix.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version == schemaVersion {
		return nil
	}
	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Another process may have migrated while this one waited for the lock.
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version == schemaVersion {
		return nil
	}
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS fts`,
		`DROP TABLE IF EXISTS docs`,
		`CREATE TABLE docs (
			rowid   INTEGER PRIMARY KEY,
			name    TEXT NOT NULL UNIQUE,
			size    INTEGER NOT NULL,
			mtime   INTEGER NOT NULL,
			id      TEXT NOT NULL,
			kind    TEXT NOT NULL,
			scope   TEXT NOT NULL,
			source  TEXT NOT NULL,
			created TEXT NOT NULL,
			anchor  TEXT NOT NULL,
			tags    TEXT NOT NULL,
			body    TEXT NOT NULL
		)`,
		`CREATE VIRTUAL TABLE fts USING fts5(body, tags, tokenize='porter unicode61')`,
		fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion),
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Sync reconciles the index with the listed files, loading only those that
// are new or changed. When nothing changed it takes no write lock. Files that
// fail to load are left out and reported in skipped.
func (ix *Index) Sync(files []File, load func(name string) (Doc, error)) (skipped []error, err error) {
	known, err := ix.known()
	if err != nil {
		return nil, err
	}
	var stale []string
	type entry struct {
		file File
		doc  Doc
	}
	var fresh []entry
	listed := make(map[string]bool, len(files))
	for _, file := range files {
		listed[file.Name] = true
		if known[file.Name] == file {
			continue
		}
		stale = append(stale, file.Name)
		doc, err := load(file.Name)
		if err != nil {
			skipped = append(skipped, err)
			continue
		}
		fresh = append(fresh, entry{file, doc})
	}
	for name := range known {
		if !listed[name] {
			stale = append(stale, name)
		}
	}
	if len(stale) == 0 {
		return skipped, nil
	}
	return skipped, retryBusy(func() error {
		tx, err := ix.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, name := range stale {
			if _, err := tx.Exec(`DELETE FROM fts WHERE rowid IN (SELECT rowid FROM docs WHERE name = ?)`, name); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM docs WHERE name = ?`, name); err != nil {
				return err
			}
		}
		for _, e := range fresh {
			tags := strings.Join(e.doc.Tags, " ")
			res, err := tx.Exec(
				`INSERT INTO docs (name, size, mtime, id, kind, scope, source, created, anchor, tags, body)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				e.file.Name, e.file.Size, e.file.ModTime, e.doc.ID, e.doc.Kind, e.doc.Scope, e.doc.Source,
				e.doc.Created.UTC().Format(time.RFC3339), e.doc.Anchor, tags, e.doc.Body)
			if err != nil {
				return err
			}
			rowid, err := res.LastInsertId()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO fts (rowid, body, tags) VALUES (?, ?, ?)`, rowid, e.doc.Body, tags); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

func (ix *Index) known() (map[string]File, error) {
	rows, err := ix.db.Query(`SELECT name, size, mtime FROM docs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	known := map[string]File{}
	for rows.Next() {
		var file File
		if err := rows.Scan(&file.Name, &file.Size, &file.ModTime); err != nil {
			return nil, err
		}
		known[file.Name] = file
	}
	return known, rows.Err()
}

// Search returns the best matches for the query, best first.
func (ix *Index) Search(q Query) ([]Hit, error) {
	match := matchExpr(q.Text)
	if match == "" {
		return nil, nil
	}
	where := []string{`fts MATCH ?`}
	args := []any{match}
	if len(q.Scopes) > 0 {
		where = append(where, `d.scope IN (?`+strings.Repeat(`, ?`, len(q.Scopes)-1)+`)`)
		for _, scope := range q.Scopes {
			args = append(args, scope)
		}
	}
	if q.Kind != "" {
		where = append(where, `d.kind = ?`)
		args = append(args, q.Kind)
	}
	args = append(args, q.Limit)
	rows, err := ix.db.Query(
		`SELECT d.id, d.kind, d.scope, d.source, d.created, d.anchor, d.tags, d.body, bm25(fts)
		 FROM fts JOIN docs d ON d.rowid = fts.rowid
		 WHERE `+strings.Join(where, ` AND `)+`
		 ORDER BY bm25(fts), d.created DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []Hit
	for rows.Next() {
		var (
			hit           Hit
			created, tags string
			rank          float64
		)
		if err := rows.Scan(&hit.ID, &hit.Kind, &hit.Scope, &hit.Source, &created, &hit.Anchor, &tags, &hit.Body, &rank); err != nil {
			return nil, err
		}
		hit.Created, _ = time.Parse(time.RFC3339, created)
		hit.Tags = strings.Fields(tags)
		hit.Score = -rank // bm25 is smaller for better matches
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

// Words too common to say anything about what a query is after.
var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true, "by": true,
	"can": true, "did": true, "do": true, "does": true, "for": true, "from": true, "has": true,
	"have": true, "how": true, "i": true, "in": true, "is": true, "it": true, "its": true, "me": true,
	"my": true, "of": true, "on": true, "or": true, "that": true, "the": true, "this": true, "to": true,
	"was": true, "we": true, "what": true, "when": true, "where": true, "which": true, "why": true,
	"with": true, "you": true,
}

// matchExpr turns free text into an FTS5 expression matching any of its
// words. Each word is quoted, so punctuation in a query is never syntax.
func matchExpr(text string) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var terms, common []string
	for _, word := range words {
		quoted := `"` + word + `"`
		if stopwords[word] {
			common = append(common, quoted)
		} else {
			terms = append(terms, quoted)
		}
	}
	if len(terms) == 0 {
		terms = common
	}
	return strings.Join(terms, " OR ")
}

// retryBusy retries fn while another process holds the database. busy_timeout
// covers ordinary lock waits; this covers the cases SQLite refuses outright,
// such as two processes creating the database at the same moment.
func retryBusy(fn func() error) error {
	var err error
	for attempt := 0; attempt < 40; attempt++ {
		if err = fn(); !isBusy(err) {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
	return err
}

func isBusy(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code() & 0xff
	return code == sqliteBusy || code == sqliteLocked
}
