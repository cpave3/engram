package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Session is one run of an identity by one agent process. Several sessions of
// the same identity may be live at once; each writes only inside its own
// directory.
type Session struct {
	ID              string    `json:"id"`
	Key             string    `json:"key"`
	Started         time.Time `json:"started"`
	Cwd             string    `json:"cwd"`
	Scope           string    `json:"scope"`
	Harness         string    `json:"harness,omitempty"`
	Model           string    `json:"model,omitempty"`
	LegatoTask      string    `json:"legato_task_id,omitempty"`
	LegatoExecution string    `json:"legato_execution_id,omitempty"`
}

const (
	EventStart       = "start"
	EventNote        = "note"
	EventRemember    = "remember"
	EventEdit        = "edit"
	EventRemove      = "remove"
	EventThreadOpen  = "thread_open"
	EventThreadClose = "thread_close"
	EventHandoff     = "handoff"
	EventSelf        = "self"
	// EventEnd marks a session finished without a handoff. A later event in
	// the same session, after it is resumed, makes it live again.
	EventEnd = "end"
)

// Event is one line of a session's append-only journal.
type Event struct {
	At   time.Time `json:"at"`
	Type string    `json:"type"`
	Text string    `json:"text,omitempty"`
	Ref  string    `json:"ref,omitempty"`
}

const sessionIDLayout = "20060102-150405"

func (s *Store) sessionsDir(identity string) string {
	return filepath.Join(s.IdentityDir(identity), "sessions")
}

func (s *Store) sessionDir(identity, id string) string {
	return filepath.Join(s.sessionsDir(identity), id)
}

// keyPath is where the session currently bound to an ambient key is recorded.
func (s *Store) keyPath(identity, key string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		}
		return '_'
	}, key)
	return filepath.Join(s.sessionsDir(identity), ".keys", safe)
}

// StartSession creates a session, named so that sorting names sorts by start
// time, and binds it to its key.
func (s *Store) StartSession(identity string, sess Session) (Session, error) {
	sess.Started = sess.Started.UTC().Truncate(time.Second)
	root := s.sessionsDir(identity)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Session{}, err
	}
	for {
		sess.ID = sess.Started.Format(sessionIDLayout) + "-" + newID(4)
		err := os.Mkdir(filepath.Join(root, sess.ID), 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return Session{}, err
		}
	}
	meta, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return Session{}, err
	}
	if err := writeFile(filepath.Join(s.sessionDir(identity, sess.ID), "meta.json"), append(meta, '\n')); err != nil {
		return Session{}, err
	}
	if err := s.Append(identity, sess.ID, Event{At: sess.Started, Type: EventStart}); err != nil {
		return Session{}, err
	}
	return sess, writeFile(s.keyPath(identity, sess.Key), []byte(sess.ID+"\n"))
}

// Session loads one session's metadata.
func (s *Store) Session(identity, id string) (Session, error) {
	data, err := os.ReadFile(filepath.Join(s.sessionDir(identity, id), "meta.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, fmt.Errorf("session %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return Session{}, err
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return Session{}, fmt.Errorf("session %q: %w", id, err)
	}
	return sess, nil
}

// SessionForKey returns the session last bound to an ambient key, if any.
func (s *Store) SessionForKey(identity, key string) (Session, bool, error) {
	data, err := os.ReadFile(s.keyPath(identity, key))
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	sess, err := s.Session(identity, strings.TrimSpace(string(data)))
	if errors.Is(err, ErrNotFound) {
		return Session{}, false, nil
	}
	return sess, err == nil, err
}

// RecentSessions returns up to limit sessions, most recently started first.
func (s *Store) RecentSessions(identity string, limit int) ([]Session, error) {
	entries, err := os.ReadDir(s.sessionsDir(identity))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			ids = append(ids, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	var sessions []Session
	for _, id := range ids {
		if len(sessions) == limit {
			break
		}
		sess, err := s.Session(identity, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

// Append adds one event to a session's journal.
func (s *Store) Append(identity, sessionID string, ev Event) error {
	ev.At = ev.At.UTC().Truncate(time.Second)
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	journal, err := os.OpenFile(filepath.Join(s.sessionDir(identity, sessionID), "journal.jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := journal.Write(append(line, '\n')); err != nil {
		journal.Close()
		return err
	}
	return journal.Close()
}

// Events reads a session's journal, oldest first. A line that does not parse,
// such as one cut short by a crash, is skipped.
func (s *Store) Events(identity, sessionID string) ([]Event, error) {
	data, err := os.ReadFile(filepath.Join(s.sessionDir(identity, sessionID), "journal.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var events []Event
	lines := bufio.NewScanner(bytes.NewReader(data))
	lines.Buffer(nil, len(data)+1)
	for lines.Scan() {
		var ev Event
		if json.Unmarshal(lines.Bytes(), &ev) == nil && ev.Type != "" {
			events = append(events, ev)
		}
	}
	return events, lines.Err()
}

// WriteHandoff saves the session's handoff, replacing any earlier one.
func (s *Store) WriteHandoff(identity, sessionID, text string, at time.Time) error {
	path := filepath.Join(s.sessionDir(identity, sessionID), "handoff.md")
	if err := writeFile(path, []byte(strings.TrimSpace(text)+"\n")); err != nil {
		return err
	}
	return s.Append(identity, sessionID, Event{At: at, Type: EventHandoff})
}

// Handoff returns the session's handoff, or "" when it left none.
func (s *Store) Handoff(identity, sessionID string) (string, error) {
	data, err := os.ReadFile(filepath.Join(s.sessionDir(identity, sessionID), "handoff.md"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(data)), err
}
