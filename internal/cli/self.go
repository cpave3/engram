package cli

import (
	"path/filepath"

	"github.com/cpave3/engram/internal/store"
)

func (a *app) selfShow(in input) error {
	if len(in.args) > 0 {
		return usagef("self show takes no arguments")
	}
	id, err := a.acting(in)
	if err != nil {
		return err
	}
	if id.Self == "" {
		a.printf("No self-description yet. Write one with `engram self write`; see `engram help self write`.\n")
		return nil
	}
	a.printf("%s\n", id.Self)
	return nil
}

func (a *app) selfWrite(in input) error {
	text, err := a.required(in.args, in.str("file"), "self-description")
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
	if err := a.store.WriteSelf(id.Name, text, a.env.Now()); err != nil {
		return err
	}
	if err := a.store.Append(id.Name, sess.ID, store.Event{At: a.env.Now(), Type: store.EventSelf}); err != nil {
		return err
	}
	earlier, err := a.store.EarlierSelves(id.Name)
	if err != nil {
		return err
	}
	a.printf("self-description saved · %s kept in %s\n",
		plural(earlier, "earlier version", "earlier versions"), filepath.Join(a.store.IdentityDir(id.Name), "self-history"))
	return nil
}
