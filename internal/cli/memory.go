package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cpave3/engram/internal/index"
	"github.com/cpave3/engram/internal/store"
)

var (
	memoryKinds = []string{"fact", "pitfall", "preference", "procedure"}
	// How far a claim was checked: seen in a session, said by the user,
	// concluded from evidence, or known beforehand and not checked.
	memorySources = []string{"observed", "told", "inferred", "prior"}
)

func (a *app) remember(in input) error {
	var drafts []store.Memory
	if batch := in.str("batch"); batch != "" {
		if len(in.args) > 0 || in.str("file") != "" {
			return usagef("--batch replaces the claim argument and --file")
		}
		var err error
		if drafts, err = a.batch(in, batch); err != nil {
			return err
		}
	} else {
		claim, err := a.required(in.args, in.str("file"), "claim")
		if err != nil {
			return err
		}
		draft, err := a.draft(claim, in.str("kind"), in.str("scope"), in.str("source"), in.str("anchor"), splitTags(in.str("tags")))
		if err != nil {
			return err
		}
		drafts = []store.Memory{draft}
	}
	id, err := a.acting(in)
	if err != nil {
		return err
	}
	sess, _, err := a.session(id.Name, false)
	if err != nil {
		return err
	}
	for _, m := range drafts {
		m.Session = sess.ID
		if m, err = a.store.AddMemory(id.Name, m); err != nil {
			return err
		}
		if err := a.store.Append(id.Name, sess.ID, store.Event{At: m.Created, Type: store.EventRemember, Ref: m.ID}); err != nil {
			return err
		}
		where := "global"
		if m.Scope != store.ScopeGlobal {
			where = "here (" + m.Scope + ")"
		}
		a.printf("remembered %s · %s · %s\n", m.ID, m.Kind, where)
	}
	return nil
}

// draft checks one memory's fields and resolves its scope, before anything
// is written.
func (a *app) draft(claim, kind, scope, source, anchor string, tags []string) (store.Memory, error) {
	claim = strings.TrimSpace(claim)
	if claim == "" {
		return store.Memory{}, usagef("a memory needs a claim")
	}
	if !slices.Contains(memoryKinds, kind) {
		return store.Memory{}, usagef("unknown --kind %q: use %s", kind, strings.Join(memoryKinds, ", "))
	}
	if !slices.Contains(memorySources, source) {
		return store.Memory{}, usagef("unknown --source %q: use %s", source, strings.Join(memorySources, ", "))
	}
	resolved, err := a.scopeFlag(scope)
	if err != nil {
		return store.Memory{}, err
	}
	return store.Memory{
		Kind: kind, Scope: resolved, Source: source, Anchor: anchor,
		Tags: tags, Created: a.env.Now(), Body: claim,
	}, nil
}

// batchLine is one memory in a --batch file. A field left out takes the
// value of the command's flag of the same name.
type batchLine struct {
	Claim  string          `json:"claim"`
	Kind   *string         `json:"kind"`
	Scope  *string         `json:"scope"`
	Source *string         `json:"source"`
	Anchor *string         `json:"anchor"`
	Tags   json.RawMessage `json:"tags"`
}

// batch reads memories from a JSON Lines file, or standard input for "-". It
// returns none of them unless all are valid.
func (a *app) batch(in input, path string) ([]store.Memory, error) {
	var (
		data []byte
		err  error
	)
	if path == "-" {
		data, err = io.ReadAll(a.env.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	pick := func(given *string, flag string) string {
		if given != nil {
			return *given
		}
		return in.str(flag)
	}
	var drafts []store.Memory
	lines := bufio.NewScanner(bytes.NewReader(data))
	lines.Buffer(nil, len(data)+1)
	for n := 1; lines.Scan(); n++ {
		if len(bytes.TrimSpace(lines.Bytes())) == 0 {
			continue
		}
		var line batchLine
		dec := json.NewDecoder(bytes.NewReader(lines.Bytes()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&line); err != nil {
			return nil, usagef("--batch line %d: %v (fields are claim, kind, scope, source, anchor, tags)", n, err)
		}
		tags := splitTags(in.str("tags"))
		if len(line.Tags) > 0 {
			var list []string
			var joined string
			switch {
			case json.Unmarshal(line.Tags, &list) == nil:
				tags = splitTags(strings.Join(list, ","))
			case json.Unmarshal(line.Tags, &joined) == nil:
				tags = splitTags(joined)
			default:
				return nil, usagef(`--batch line %d: "tags" must be a list of strings or one comma-separated string`, n)
			}
		}
		draft, err := a.draft(line.Claim, pick(line.Kind, "kind"), pick(line.Scope, "scope"),
			pick(line.Source, "source"), pick(line.Anchor, "anchor"), tags)
		if err != nil {
			return nil, usagef("--batch line %d: %v", n, err)
		}
		drafts = append(drafts, draft)
	}
	if err := lines.Err(); err != nil {
		return nil, err
	}
	if len(drafts) == 0 {
		return nil, usagef("--batch has no memories: give one JSON object per line")
	}
	return drafts, nil
}

func splitTags(raw string) []string {
	var tags []string
	for _, tag := range strings.Split(raw, ",") {
		tag = strings.Join(strings.Fields(strings.ToLower(tag)), "-")
		if tag != "" && !slices.Contains(tags, tag) {
			tags = append(tags, tag)
		}
	}
	return tags
}

func (a *app) recall(in input) error {
	query := strings.TrimSpace(strings.Join(in.args, " "))
	if query == "" {
		return usagef("recall needs a query")
	}
	kind := in.str("kind")
	if kind != "" && !slices.Contains(memoryKinds, kind) {
		return usagef("unknown --kind %q: use %s", kind, strings.Join(memoryKinds, ", "))
	}
	limit, err := in.num("limit", 1)
	if err != nil {
		return err
	}
	id, err := a.acting(in)
	if err != nil {
		return err
	}
	q := index.Query{Text: query, Kind: kind, Limit: limit}
	if !in.on("all-scopes") {
		q.Scopes = a.reach()
	}
	hits, err := a.search(id.Name, q)
	if err != nil {
		return err
	}
	if in.on("json") {
		out := struct {
			Query    string      `json:"query"`
			Scopes   []string    `json:"scopes"`
			Memories []memoryRow `json:"memories"`
		}{Query: query, Scopes: q.Scopes, Memories: make([]memoryRow, 0, len(hits))}
		for _, hit := range hits {
			score := hit.Score
			out.Memories = append(out.Memories, memoryRow{
				ID: hit.ID, Kind: hit.Kind, Scope: hit.Scope, Source: hit.Source, Created: hit.Created,
				Anchor: hit.Anchor, Tags: hit.Tags, Body: hit.Body, Score: &score,
			})
		}
		return json.NewEncoder(a.env.Stdout).Encode(out)
	}
	searched := a.searched(in.on("all-scopes"))
	if len(hits) == 0 {
		a.printf("No memories match %q (%s).\n", query, searched)
		return nil
	}
	a.printf("%s %q (%s)\n", plural(len(hits), "memory matches", "memories match"), query, searched)
	for _, hit := range hits {
		a.printf("\n%s · %s · %s · %s · %s\n", hit.ID, hit.Kind, a.scopeLabel(hit.Scope), hit.Source, age(a.env.Now(), hit.Created))
		a.printf("%s\n", indent(hit.Body))
		if extra := anchorAndTags(hit.Anchor, hit.Tags); extra != "" {
			a.printf("  %s\n", extra)
		}
	}
	return nil
}

// memoryRow is a memory as --json prints it.
type memoryRow struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Scope   string    `json:"scope"`
	Source  string    `json:"source"`
	Created time.Time `json:"created"`
	Anchor  string    `json:"anchor,omitempty"`
	Tags    []string  `json:"tags,omitempty"`
	Body    string    `json:"body"`
	Score   *float64  `json:"score,omitempty"`
}

func (a *app) searched(allScopes bool) string {
	if allScopes {
		return "all scopes"
	}
	return "global + here"
}

func anchorAndTags(anchor string, tags []string) string {
	var parts []string
	if anchor != "" {
		parts = append(parts, "anchor: "+anchor)
	}
	if len(tags) > 0 {
		parts = append(parts, "tags: "+strings.Join(tags, ", "))
	}
	return strings.Join(parts, " · ")
}

// search brings the identity's index up to date with its memory files, then
// queries it. A refresh that fails costs freshness, not the search.
func (a *app) search(identity string, q index.Query) ([]index.Hit, error) {
	files, err := a.store.MemoryFiles(identity)
	if err != nil {
		return nil, err
	}
	ix, err := index.Open(filepath.Join(a.store.IdentityDir(identity), ".index"))
	if err != nil {
		return nil, err
	}
	defer ix.Close()

	byName := make(map[string]store.MemoryFile, len(files))
	listed := make([]index.File, len(files))
	for i, file := range files {
		byName[file.Name] = file
		listed[i] = index.File{Name: file.Name, Size: file.Size, ModTime: file.ModTime.UnixNano()}
	}
	skipped, err := ix.Sync(listed, func(name string) (index.Doc, error) {
		m, err := store.LoadMemory(byName[name])
		if err != nil {
			return index.Doc{}, err
		}
		return index.Doc{
			ID: m.ID, Kind: m.Kind, Scope: m.Scope, Source: m.Source,
			Anchor: m.Anchor, Tags: m.Tags, Created: m.Created, Body: m.Body,
		}, nil
	})
	if err != nil {
		fmt.Fprintf(a.env.Stderr, "engram: index not refreshed, results may be stale: %v\n", err)
	}
	a.warnSkipped(skipped)
	return ix.Search(q)
}

func (a *app) warnSkipped(skipped []error) {
	for _, skip := range skipped {
		fmt.Fprintf(a.env.Stderr, "engram: skipped unreadable memory: %v\n", skip)
	}
}

// memories lists what the identity holds, straight from its files.
func (a *app) memories(in input) error {
	if len(in.args) > 0 {
		return usagef("memories takes no arguments; to search, use `engram recall \"<query>\"`")
	}
	kind, source, tag := in.str("kind"), in.str("source"), in.str("tag")
	if kind != "" && !slices.Contains(memoryKinds, kind) {
		return usagef("unknown --kind %q: use %s", kind, strings.Join(memoryKinds, ", "))
	}
	if source != "" && !slices.Contains(memorySources, source) {
		return usagef("unknown --source %q: use %s", source, strings.Join(memorySources, ", "))
	}
	limit, err := in.num("limit", 1)
	if err != nil {
		return err
	}
	id, err := a.acting(in)
	if err != nil {
		return err
	}
	all, skipped, err := a.store.Memories(id.Name)
	if err != nil {
		return err
	}
	a.warnSkipped(skipped)

	var matching []store.Memory
	for _, m := range all {
		switch {
		case !in.on("all-scopes") && !a.inReach(m.Scope),
			kind != "" && m.Kind != kind,
			source != "" && m.Source != source,
			tag != "" && !slices.Contains(m.Tags, strings.ToLower(tag)):
			continue
		}
		matching = append(matching, m)
	}
	sort.SliceStable(matching, func(i, j int) bool { return matching[i].Created.After(matching[j].Created) })
	total := len(matching)
	if total > limit {
		matching = matching[:limit]
	}
	if in.on("json") {
		out := struct {
			Total    int         `json:"total"`
			Memories []memoryRow `json:"memories"`
		}{Total: total, Memories: make([]memoryRow, 0, len(matching))}
		for _, m := range matching {
			out.Memories = append(out.Memories, memoryRow{
				ID: m.ID, Kind: m.Kind, Scope: m.Scope, Source: m.Source, Created: m.Created,
				Anchor: m.Anchor, Tags: m.Tags, Body: m.Body,
			})
		}
		return json.NewEncoder(a.env.Stdout).Encode(out)
	}
	searched := a.searched(in.on("all-scopes"))
	if total == 0 {
		a.printf("No memories (%s).\n", searched)
		return nil
	}
	a.printf("%s (%s), newest first\n", plural(total, "memory", "memories"), searched)
	if total > len(matching) {
		a.printf("Showing %d of %d; raise --limit to see more.\n", len(matching), total)
	}
	a.printf("\n")
	for _, m := range matching {
		a.printf("%s — %s\n", a.memoryHead(m), clip(firstLine(m.Body), summaryWidth))
	}
	return nil
}

func (a *app) memoryHead(m store.Memory) string {
	return fmt.Sprintf("%s · %s · %s · %s · %s", m.ID, m.Kind, a.scopeLabel(m.Scope), m.Source, age(a.env.Now(), m.Created))
}

// show prints one memory or thread in full, by any id engram has printed.
func (a *app) show(in input) error {
	if len(in.args) != 1 {
		return usagef("show needs exactly one id")
	}
	want := in.args[0]
	id, err := a.acting(in)
	if err != nil {
		return err
	}
	all, skipped, err := a.store.Memories(id.Name)
	if err != nil {
		return err
	}
	a.warnSkipped(skipped)
	for _, m := range all {
		if m.ID != want {
			continue
		}
		a.printf("%s\n\n%s\n\n", a.memoryHead(m), m.Body)
		a.fields([][2]string{
			{"scope", m.Scope}, {"source", m.Source}, {"created", m.Created.Format(time.RFC3339)},
			{"session", m.Session}, {"anchor", m.Anchor}, {"tags", strings.Join(m.Tags, ", ")}, {"file", m.Path},
		})
		return nil
	}
	threads, err := a.store.Threads(id.Name)
	if err != nil {
		return err
	}
	for _, t := range threads {
		if t.ID != want {
			continue
		}
		state := "open"
		if t.Closed != nil {
			state = "closed"
		}
		a.printf("thread %s · %s · %s · opened %s\n\n%s\n", t.ID, state, a.scopeLabel(t.Scope), age(a.env.Now(), t.Opened), t.Title)
		if t.Body != "" {
			a.printf("\n%s\n", t.Body)
		}
		if t.Closed != nil {
			a.printf("\nClosed %s: %s\n", age(a.env.Now(), t.Closed.Closed), orPlaceholder(t.Closed.Resolution, "(no resolution given)"))
		}
		a.printf("\n")
		a.fields([][2]string{
			{"scope", t.Scope}, {"opened", t.Opened.Format(time.RFC3339)}, {"session", t.Session}, {"file", t.Path},
		})
		return nil
	}
	return fmt.Errorf("nothing has the id %q; ids are printed by recall, memories, boot and thread list", want)
}

// fields prints the name and value of each field that has a value.
func (a *app) fields(pairs [][2]string) {
	for _, pair := range pairs {
		if pair[1] != "" {
			a.printf("%-9s%s\n", pair[0]+":", pair[1])
		}
	}
}

func age(now, then time.Time) string {
	d := now.Sub(then)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func indent(text string) string {
	return "  " + strings.ReplaceAll(text, "\n", "\n  ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
