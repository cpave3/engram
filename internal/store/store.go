// Package store is the file-backed source of truth for engram.
//
// Every record is a plain file under one identity's directory. Records are
// written once, under a name no other writer will choose, so concurrent
// sessions never contend for a lock. Nothing in this package touches SQLite.
package store

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
)

// Store reads and writes the engram directory tree rooted at one path.
type Store struct{ root string }

func Open(root string) *Store { return &Store{root: root} }

// Root is the directory holding every identity.
func (s *Store) Root() string { return s.root }

// IdentityDir is where one identity's files live.
func (s *Store) IdentityDir(name string) string {
	return filepath.Join(s.root, "identities", name)
}

const idAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// newID returns n random characters that are unambiguous to read and retype.
func newID(n int) string {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("engram: reading random bytes: %v", err))
	}
	for i, b := range raw {
		raw[i] = idAlphabet[int(b)%len(idAlphabet)]
	}
	return string(raw)
}

// writeFile replaces path with data in one step, so readers never see a
// partial file.
func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

const fence = "---\n"

// encodeDoc renders a markdown document with YAML frontmatter.
func encodeDoc(meta any, body string) ([]byte, error) {
	head, err := yaml.Marshal(meta)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString(fence)
	buf.Write(head)
	buf.WriteString(fence)
	if body = strings.TrimSpace(body); body != "" {
		buf.WriteString("\n" + body + "\n")
	}
	return buf.Bytes(), nil
}

// decodeDoc splits a document into its frontmatter and body. A file without
// frontmatter is all body, so hand-written files still load.
func decodeDoc(data []byte, meta any) (string, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, fence) {
		return strings.TrimSpace(text), nil
	}
	rest := text[len(fence):]
	end := strings.Index(rest, "\n"+fence)
	if end < 0 {
		if !strings.HasSuffix(rest, "\n---") {
			return "", errors.New("frontmatter is not closed with ---")
		}
		end = len(rest) - len("\n---")
		rest += "\n"
	}
	if err := yaml.Unmarshal([]byte(rest[:end]), meta); err != nil {
		return "", fmt.Errorf("frontmatter: %w", err)
	}
	return strings.TrimSpace(rest[end+len("\n"+fence):]), nil
}

func readDoc(path string, meta any) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	body, err := decodeDoc(data, meta)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return body, nil
}
