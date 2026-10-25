package cli

import (
	"crypto/sha256"
	"encoding/hex"
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
			if strong {
				return found, true, nil
			}
			events, err := a.store.Events(identity, found.ID)
			if err != nil {
				return store.Session{}, false, err
			}
			if last := lastEvent(events); last.Type != store.EventHandoff && a.env.Now().Sub(last.At) < weakKeyWindow {
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
