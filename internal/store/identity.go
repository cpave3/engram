package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Identity is one individual: a seed issued by the user, and a
// self-description the identity maintains.
type Identity struct {
	Name    string
	Created time.Time
	Seed    string // identity.md, issued by the user
	Self    string // self.md, written by the identity
}

type identityMeta struct {
	Name    string    `yaml:"name"`
	Created time.Time `yaml:"created"`
}

var identityName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidIdentityName reports whether name can be used as a directory and typed
// without quoting.
func ValidIdentityName(name string) bool { return identityName.MatchString(name) }

// CreateIdentity issues a new identity with the given seed text.
func (s *Store) CreateIdentity(name, seed string, now time.Time) error {
	if !ValidIdentityName(name) {
		return fmt.Errorf("invalid identity name %q: use lowercase letters, digits and hyphens", name)
	}
	dir := s.IdentityDir(name)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("identity %q: %w", name, ErrExists)
		}
		return err
	}
	doc, err := encodeDoc(identityMeta{Name: name, Created: now.UTC().Truncate(time.Second)}, seed)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, "identity.md"), doc)
}

// Identity loads one identity by name.
func (s *Store) Identity(name string) (Identity, error) {
	if !ValidIdentityName(name) {
		return Identity{}, fmt.Errorf("identity %q: %w", name, ErrNotFound)
	}
	dir := s.IdentityDir(name)
	var meta identityMeta
	seed, err := readDoc(filepath.Join(dir, "identity.md"), &meta)
	if errors.Is(err, os.ErrNotExist) {
		return Identity{}, fmt.Errorf("identity %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return Identity{}, err
	}
	self, err := readDoc(filepath.Join(dir, "self.md"), &struct{}{})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Identity{}, err
	}
	return Identity{Name: name, Created: meta.Created, Seed: seed, Self: self}, nil
}

// Identities lists every issued identity by name.
func (s *Store) Identities() ([]Identity, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "identities"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var all []Identity
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id, err := s.Identity(entry.Name())
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		all = append(all, id)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all, nil
}

func (s *Store) selfHistoryDir(identity string) string {
	return filepath.Join(s.IdentityDir(identity), "self-history")
}

// WriteSelf replaces the identity's self-description. Every version written
// is also kept in self-history, as is a version someone edited by hand, so a
// session that overwrites another loses nothing.
func (s *Store) WriteSelf(identity, text string, at time.Time) error {
	current := filepath.Join(s.IdentityDir(identity), "self.md")
	old, err := os.ReadFile(current)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(bytes.TrimSpace(old)) > 0 {
		kept, err := s.selfHistory(identity)
		if err != nil {
			return err
		}
		if len(kept) == 0 || !bytes.Equal(kept[len(kept)-1], old) {
			if err := s.keepSelf(identity, old, at.Add(-time.Second)); err != nil {
				return err
			}
		}
	}
	next := []byte(strings.TrimSpace(text) + "\n")
	if err := s.keepSelf(identity, next, at); err != nil {
		return err
	}
	return writeFile(current, next)
}

func (s *Store) keepSelf(identity string, version []byte, at time.Time) error {
	name := at.UTC().Format(sessionIDLayout) + "-" + newID(4) + ".md"
	return writeFile(filepath.Join(s.selfHistoryDir(identity), name), version)
}

// selfHistory returns every kept version, oldest first.
func (s *Store) selfHistory(identity string) ([][]byte, error) {
	paths, err := filepath.Glob(filepath.Join(s.selfHistoryDir(identity), "*.md"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var versions [][]byte
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		versions = append(versions, data)
	}
	return versions, nil
}

// EarlierSelves counts the kept versions that are not the current one.
func (s *Store) EarlierSelves(identity string) (int, error) {
	kept, err := s.selfHistory(identity)
	if err != nil || len(kept) == 0 {
		return 0, err
	}
	return len(kept) - 1, nil
}
