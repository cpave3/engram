package cli

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/cpave3/engram/internal/store"
)

func (a *app) threadOpen(in input) error {
	title, err := a.text(in.args, "", "title")
	if err != nil {
		return err
	}
	if title == "" {
		return usagef("thread open needs a title")
	}
	body := in.str("body")
	if file := in.str("file"); file != "" {
		if body != "" {
			return usagef("pass the body with --body or --file, not both")
		}
		if body, err = a.text(nil, file, "body"); err != nil {
			return err
		}
	}
	scope, err := a.scopeFlag(in.str("scope"))
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
	t, err := a.store.OpenThread(id.Name, store.Thread{
		Title: title, Body: body, Scope: scope, Opened: a.env.Now(), Session: sess.ID,
	})
	if err != nil {
		return err
	}
	if err := a.store.Append(id.Name, sess.ID, store.Event{At: t.Opened, Type: store.EventThreadOpen, Ref: t.ID}); err != nil {
		return err
	}
	a.printf("opened thread %s\n", t.ID)
	return nil
}

func (a *app) threadList(in input) error {
	if len(in.args) > 0 {
		return usagef("thread list takes no arguments")
	}
	id, err := a.acting(in)
	if err != nil {
		return err
	}
	open, err := a.openThreads(id.Name)
	if err != nil {
		return err
	}
	if in.on("json") {
		type row struct {
			ID     string    `json:"id"`
			Title  string    `json:"title"`
			Scope  string    `json:"scope"`
			Opened time.Time `json:"opened"`
			Body   string    `json:"body,omitempty"`
		}
		rows := make([]row, 0, len(open))
		for _, t := range open {
			rows = append(rows, row{ID: t.ID, Title: t.Title, Scope: t.Scope, Opened: t.Opened, Body: t.Body})
		}
		return json.NewEncoder(a.env.Stdout).Encode(rows)
	}
	if len(open) == 0 {
		a.printf("No open threads.\n")
		return nil
	}
	a.printf("%s\n", plural(len(open), "open thread", "open threads"))
	for _, t := range open {
		a.printf("\n%s · %s · opened %s\n  %s\n", t.ID, a.scopeLabel(t.Scope), age(a.env.Now(), t.Opened), t.Title)
		if t.Body != "" {
			a.printf("%s\n", indent(t.Body))
		}
	}
	return nil
}

// openThreads returns the identity's unfinished threads, those for the
// current repository first, then oldest first.
func (a *app) openThreads(identity string) ([]store.Thread, error) {
	all, err := a.store.Threads(identity)
	if err != nil {
		return nil, err
	}
	var open []store.Thread
	for _, t := range all {
		if t.Closed == nil {
			open = append(open, t)
		}
	}
	sort.SliceStable(open, func(i, j int) bool {
		return a.isHere(open[i].Scope) && !a.isHere(open[j].Scope)
	})
	return open, nil
}

func (a *app) threadClose(in input) error {
	if len(in.args) == 0 {
		return usagef("thread close needs a thread id; `engram thread list` prints them")
	}
	threadID := in.args[0]
	resolution, err := a.text(in.args[1:], in.str("file"), "resolution")
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
	closure := store.Closure{Closed: a.env.Now(), Session: sess.ID, Resolution: resolution}
	if _, err := a.store.CloseThread(id.Name, threadID, closure); err != nil {
		return err
	}
	if err := a.store.Append(id.Name, sess.ID, store.Event{At: closure.Closed, Type: store.EventThreadClose, Ref: threadID}); err != nil {
		return err
	}
	a.printf("closed thread %s\n", threadID)
	return nil
}
