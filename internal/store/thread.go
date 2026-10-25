package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var ErrClosed = errors.New("already closed")

// Thread is unfinished work: something to decide, test or come back to.
type Thread struct {
	ID      string    `yaml:"id"`
	Title   string    `yaml:"title"`
	Scope   string    `yaml:"scope"`
	Opened  time.Time `yaml:"opened"`
	Session string    `yaml:"session,omitempty"`
	Body    string    `yaml:"-"`
	Closed  *Closure  `yaml:"-"`
	Path    string    `yaml:"-"` // where the thread was loaded from
}

// Closure records how a thread ended. It is a separate file beside the
// thread, so closing never rewrites what another session opened.
type Closure struct {
	Closed     time.Time `yaml:"closed"`
	Session    string    `yaml:"session,omitempty"`
	Resolution string    `yaml:"-"`
}

const closedSuffix = ".closed.md"

func (s *Store) threadDir(identity string) string {
	return filepath.Join(s.IdentityDir(identity), "threads")
}

// OpenThread writes a new thread and returns it with its id assigned.
func (s *Store) OpenThread(identity string, t Thread) (Thread, error) {
	dir := s.threadDir(identity)
	t.Opened = t.Opened.UTC().Truncate(time.Second)
	id, err := unusedID(dir)
	if err != nil {
		return Thread{}, err
	}
	t.ID = id
	doc, err := encodeDoc(t, t.Body)
	if err != nil {
		return Thread{}, err
	}
	name := t.Opened.Format(dateLayout) + "-" + t.ID + ".md"
	return t, writeFile(filepath.Join(dir, name), doc)
}

// Threads lists every thread, oldest first, with its closure when it has one.
func (s *Store) Threads(identity string) ([]Thread, error) {
	dir := s.threadDir(identity)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") || strings.HasSuffix(name, closedSuffix) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var threads []Thread
	for _, name := range names {
		path := filepath.Join(dir, name)
		var t Thread
		body, err := readDoc(path, &t)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		t.Body = body
		t.Path = path
		if t.ID == "" {
			t.ID = datePrefix.ReplaceAllString(strings.TrimSuffix(name, ".md"), "")
		}
		if t.Title == "" {
			t.Title = t.ID
		}
		if t.Scope == "" {
			t.Scope = ScopeGlobal
		}
		var closure Closure
		resolution, err := readDoc(closurePath(path), &closure)
		switch {
		case err == nil:
			closure.Resolution = resolution
			t.Closed = &closure
		case !errors.Is(err, os.ErrNotExist):
			return nil, err
		}
		threads = append(threads, t)
	}
	return threads, nil
}

func closurePath(threadPath string) string {
	return strings.TrimSuffix(threadPath, ".md") + closedSuffix
}

// CloseThread records how an open thread ended.
func (s *Store) CloseThread(identity, id string, closure Closure) (Thread, error) {
	threads, err := s.Threads(identity)
	if err != nil {
		return Thread{}, err
	}
	for _, t := range threads {
		if t.ID != id {
			continue
		}
		if t.Closed != nil {
			return Thread{}, fmt.Errorf("thread %s: %w", id, ErrClosed)
		}
		closure.Closed = closure.Closed.UTC().Truncate(time.Second)
		doc, err := encodeDoc(closure, closure.Resolution)
		if err != nil {
			return Thread{}, err
		}
		t.Closed = &closure
		return t, writeFile(closurePath(t.Path), doc)
	}
	return Thread{}, fmt.Errorf("thread %s: %w", id, ErrNotFound)
}
