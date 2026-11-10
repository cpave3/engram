package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/cpave3/engram/internal/store"
)

// A session found through a weak key is only resumed while it is this fresh.
const weakKeyWindow = 12 * time.Hour

// ambientKey identifies the agent process a command belongs to. An agent's
// shell keeps no variables between calls, so the session has to be found from
// what the harness already put in the environment. A strong key names exactly
// one agent run; a weak one names a place that later runs will reuse.
func ambientKey(getenv func(string) string, cwd string) (key string, strong bool) {
	if v := getenv("ENGRAM_SESSION"); v != "" {
		return "explicit:" + v, true
	}
	if v := getenv("LEGATO_EXECUTION_ID"); v != "" {
		return "legato:" + v, true
	}
	if v := getenv("CLAUDE_CODE_SESSION_ID"); v != "" {
		return "claude:" + v, true
	}
	if pane := getenv("TMUX_PANE"); pane != "" {
		// TMUX is "socket,server-pid,session"; pane ids restart with the server.
		server := strings.Split(getenv("TMUX"), ",")
		if len(server) >= 2 {
			return "tmux:" + server[1] + ":" + pane, false
		}
		return "tmux:" + pane, false
	}
	sum := sha256.Sum256([]byte(cwd))
	return "cwd:" + hex.EncodeToString(sum[:6]), false
}

// session returns the session this command belongs to, starting one when the
// agent has none. Any command may be an agent's first, so none requires boot.
func (a *app) session(identity string, forceNew bool) (sess store.Session, resumed bool, err error) {
	key, strong := ambientKey(a.env.Getenv, a.env.Cwd)
	if !forceNew {
		found, ok, err := a.store.SessionForKey(identity, key)
		if err != nil {
			return store.Session{}, false, err
		}
		if ok {
			usable, err := a.resumable(identity, found, strong)
			if err != nil {
				return store.Session{}, false, err
			}
			if usable {
				return found, true, nil
			}
		}
	}
	sess, err = a.store.StartSession(identity, store.Session{
		Key:             key,
		Started:         a.env.Now(),
		Cwd:             a.env.Cwd,
		Scope:           a.here(),
		Harness:         harness(a.env.Getenv),
		Model:           a.env.Getenv("ENGRAM_MODEL"),
		LegatoTask:      a.env.Getenv("LEGATO_TASK_ID"),
		LegatoExecution: a.env.Getenv("LEGATO_EXECUTION_ID"),
	})
	return sess, false, err
}

func (a *app) resumable(identity string, sess store.Session, strong bool) (bool, error) {
	if strong {
		return true, nil
	}
	events, err := a.store.Events(identity, sess.ID)
	if err != nil {
		return false, err
	}
	last := lastEvent(events)
	return !finished(last) && a.env.Now().Sub(last.At) < weakKeyWindow, nil
}

// finished reports whether a session whose last event is ev has stopped: it
// wrote a handoff or was ended, and has done nothing since.
func finished(ev store.Event) bool {
	return ev.Type == store.EventHandoff || ev.Type == store.EventEnd
}

// A place can host multiple agents. Never guess which identity owns a command
// when the ambient key matches more than one resumable session.
func (a *app) sessionIdentity() (string, error) {
	key, strong := ambientKey(a.env.Getenv, a.env.Cwd)
	identities, err := a.store.Identities()
	if err != nil {
		return "", err
	}
	var names []string
	for _, id := range identities {
		sess, ok, err := a.store.SessionForKey(id.Name, key)
		if err != nil {
			return "", err
		}
		if !ok {
			continue
		}
		usable, err := a.resumable(id.Name, sess, strong)
		if err != nil {
			return "", err
		}
		if usable {
			names = append(names, id.Name)
		}
	}
	if len(names) > 1 {
		return "", usagef("multiple identities match this session (%s): pass --as <name> (e.g. `engram note \"<text>\" --as %s`) or set ENGRAM_IDENTITY", strings.Join(names, ", "), names[0])
	}
	if len(names) == 1 {
		return names[0], nil
	}
	return "", nil
}

func harness(getenv func(string) string) string {
	if v := getenv("ENGRAM_HARNESS"); v != "" {
		return v
	}
	if getenv("CLAUDECODE") == "1" {
		return "claude-code"
	}
	return ""
}

func lastEvent(events []store.Event) store.Event {
	if len(events) == 0 {
		return store.Event{}
	}
	return events[len(events)-1]
}

func (a *app) note(in input) error {
	text, err := a.required(in.args, in.str("file"), "note")
	if err != nil {
		return err
	}
	id, err := a.acting(in)
	if err != nil {
		return err
	}
	sess, _, err := a.session(id.Name, false)
	if err != nil {
		return err
	}
	if err := a.store.Append(id.Name, sess.ID, store.Event{At: a.env.Now(), Type: store.EventNote, Text: text}); err != nil {
		return err
	}
	a.printf("noted · session %s\n", sess.ID)
	return nil
}

func (a *app) handoff(in input) error {
	text, err := a.required(in.args, in.str("file"), "handoff")
	if err != nil {
		return err
	}
	id, err := a.acting(in)
	if err != nil {
		return err
	}
	sess, _, err := a.session(id.Name, false)
	if err != nil {
		return err
	}
	if err := a.store.WriteHandoff(id.Name, sess.ID, text, a.env.Now()); err != nil {
		return err
	}
	a.printf("handoff saved · session %s\n", sess.ID)
	return nil
}

// sessionEnd marks this agent run's session finished without a handoff. It
// never starts a session: there is nothing to end when the run has none.
func (a *app) sessionEnd(in input) error {
	if len(in.args) > 0 {
		return usagef("session end takes no arguments")
	}
	id, err := a.acting(in)
	if err != nil {
		return err
	}
	key, strong := ambientKey(a.env.Getenv, a.env.Cwd)
	sess, ok, err := a.store.SessionForKey(id.Name, key)
	if err != nil {
		return err
	}
	var events []store.Event
	if ok {
		if events, err = a.store.Events(id.Name, sess.ID); err != nil {
			return err
		}
		// A weak key can name a place a stale session once ran in. A fresh one
		// that already handed off or ended is still this run's to end.
		ok = strong || a.env.Now().Sub(lastEvent(events).At) < weakKeyWindow
	}
	if !ok {
		return codedError{codeNoSession, exitError, fmt.Sprintf("%s has no session in this agent run to end", id.Name)}
	}
	already := lastEvent(events).Type == store.EventEnd
	if !already {
		if err := a.store.Append(id.Name, sess.ID, store.Event{At: a.env.Now(), Type: store.EventEnd}); err != nil {
			return err
		}
	}
	if in.on("json") {
		return a.writeJSON(map[string]any{"session_id": sess.ID, "identity": id.Name, "already_ended": already})
	}
	if already {
		a.printf("session %s was already ended\n", sess.ID)
		return nil
	}
	a.printf("ended session %s\n", sess.ID)
	return nil
}
