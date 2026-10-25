package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cpave3/engram/internal/store"
)

func (a *app) identityNew(in input) error {
	if len(in.args) == 0 {
		return usagef("identity new needs a name")
	}
	name := in.args[0]
	seed, err := a.text(in.args[1:], in.str("file"), "seed text")
	if err != nil {
		return err
	}
	if !store.ValidIdentityName(name) {
		return usagef("invalid identity name %q: use lowercase letters, digits and hyphens", name)
	}
	if err := a.store.CreateIdentity(name, seed, a.env.Now()); err != nil {
		return err
	}
	a.printf("issued identity %s at %s\n", name, a.store.IdentityDir(name))
	return nil
}

func (a *app) identityList(in input) error {
	if len(in.args) > 0 {
		return usagef("identity list takes no arguments")
	}
	all, err := a.store.Identities()
	if err != nil {
		return err
	}
	if in.on("json") {
		type row struct {
			Name    string    `json:"name"`
			Created time.Time `json:"created"`
			Summary string    `json:"summary"`
		}
		rows := make([]row, 0, len(all))
		for _, id := range all {
			rows = append(rows, row{Name: id.Name, Created: id.Created, Summary: firstLine(id.Seed)})
		}
		return json.NewEncoder(a.env.Stdout).Encode(rows)
	}
	if len(all) == 0 {
		a.printf("No identities yet. Issue one with `engram identity new <name>`.\n")
		return nil
	}
	for _, id := range all {
		if summary := firstLine(id.Seed); summary != "" {
			a.printf("%s — %s\n", id.Name, summary)
		} else {
			a.printf("%s\n", id.Name)
		}
	}
	return nil
}

func (a *app) identityShow(in input) error {
	if len(in.args) != 1 {
		return usagef("identity show needs exactly one name")
	}
	id, err := a.knownIdentity(in.args[0])
	if err != nil {
		return err
	}
	a.printf("# %s\n\nIssued %s. Files: %s\n", id.Name, id.Created.Format(time.DateOnly), a.store.IdentityDir(id.Name))
	a.printf("\n## Issued identity\n%s\n", orPlaceholder(id.Seed, "(no seed text)"))
	if id.Self != "" {
		a.printf("\n## Self-description\n%s\n", id.Self)
	}
	return nil
}

// acting resolves who a command acts as: --as, then ENGRAM_IDENTITY.
func (a *app) acting(in input) (store.Identity, error) {
	name := in.str("as")
	if name == "" {
		name = a.env.Getenv("ENGRAM_IDENTITY")
	}
	if name == "" {
		return store.Identity{}, usagef("no identity: pass --as <name> or set ENGRAM_IDENTITY%s", a.knownNames())
	}
	return a.knownIdentity(name)
}

// knownIdentity loads an identity, naming the alternatives when it is missing.
func (a *app) knownIdentity(name string) (store.Identity, error) {
	id, err := a.store.Identity(name)
	if errors.Is(err, store.ErrNotFound) {
		return store.Identity{}, fmt.Errorf("unknown identity %q%s", name, a.knownNames())
	}
	return id, err
}

func (a *app) knownNames() string {
	all, err := a.store.Identities()
	if err != nil || len(all) == 0 {
		return " (none issued yet; run `engram identity new <name>`)"
	}
	names := make([]string, len(all))
	for i, id := range all {
		names[i] = id.Name
	}
	return " (known: " + strings.Join(names, ", ") + ")"
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(strings.TrimLeft(line, "# "))
}

func orPlaceholder(s, placeholder string) string {
	if s == "" {
		return placeholder
	}
	return s
}
