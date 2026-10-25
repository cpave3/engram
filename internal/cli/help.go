package cli

import (
	"fmt"
	"strings"
)

// groups orders the commands in the overview.
var groups = []struct {
	title string
	names []string
}{
	{"Identities", []string{"identity new", "identity list", "identity show"}},
	{"Sessions", []string{"boot", "note", "handoff"}},
	{"Memories", []string{"remember", "recall", "memories", "show"}},
	{"Unfinished work", []string{"thread open", "thread list", "thread close"}},
	{"Yourself", []string{"self show", "self write"}},
}

// help answers `engram help [topic]`.
func (a *app) help(args []string) error {
	topic := strings.Join(args, " ")
	switch {
	case topic == "":
		a.printf("%s", overview())
	case topic == "concepts":
		a.printf("%s", concepts)
	case commandIndex[topic] != nil:
		a.printf("%s", commandIndex[topic].help())
	case len(subcommands(topic)) > 0:
		a.printf("engram %s has these commands:\n\n", topic)
		var cmds []*command
		width := 0
		for _, sub := range subcommands(topic) {
			cmd := commandIndex[topic+" "+sub]
			cmds = append(cmds, cmd)
			width = max(width, len(cmd.name+" "+cmd.args))
		}
		for _, cmd := range cmds {
			a.printf("  %-*s  %s\n", width, cmd.name+" "+cmd.args, cmd.summary)
		}
		a.printf("\nRun `engram help %s` for the flags, examples and output of one.\n", cmds[0].name)
	default:
		return usagef("no help for %q\nRun `engram help` for the list of commands.", topic)
	}
	return nil
}

func overview() string {
	var b strings.Builder
	b.WriteString(overviewIntro)
	b.WriteString("\nCommands:\n")
	for _, group := range groups {
		fmt.Fprintf(&b, "\n  %s\n", group.title)
		for _, name := range group.names {
			fmt.Fprintf(&b, "    %-15s %s\n", name, commandIndex[name].summary)
		}
	}
	b.WriteString(overviewRest)
	return b.String()
}

const overviewIntro = `engram — identities with memory for coding agents

An identity is an individual. A user issues it once; from then on it keeps
its own memories, notes and unfinished work, so each session starts from what
earlier sessions learned. Engram stores plain files and never calls a model.

A session, start to finish:

  engram boot                      who you are, what you know, where you left off
  engram recall "<query>"          search your memories before assuming
  engram note "<text>"             jot anything that may matter later
  engram remember "<claim>"        keep one durable claim
  engram thread open "<title>"     record work that will outlive the session
  engram handoff "<text>"          before you finish: what you did, what is open
`

const overviewRest = `
  engram help <command>     flags, examples and output of one command
  engram help concepts      identities, sessions, scopes, kinds, sources, files
  engram <command> --help   the same as engram help <command>

Acting as an identity:
  Every command except "identity" and "help" acts as one identity, named by
  ENGRAM_IDENTITY or by --as <name>. engram identity list shows the names.
  An agent's shell rarely keeps exported variables between calls, so if your
  environment does not already set ENGRAM_IDENTITY, put it on each command:
      ENGRAM_IDENTITY=ada engram boot

Writing text:
  Flags may come before or after the text.
  Text with quotes, apostrophes or several lines is easiest on standard input:
      engram remember --kind pitfall --file - <<'EOF'
      It's the "deferred" transaction that fails.
      EOF
  Text that starts with a dash goes after a bare --.
  To save many memories at once, see engram help remember (--batch).

Environment:
  ENGRAM_IDENTITY   the identity to act as
  ENGRAM_HOME       where the store is (default: $XDG_DATA_HOME/engram,
                    or ~/.local/share/engram)
  ENGRAM_SESSION    a name for this agent run, when the harness sets none
  ENGRAM_HARNESS, ENGRAM_MODEL   recorded with each session

Exit status:
  0  success
  1  the command failed; the reason is on standard error
  2  the command was called wrongly; standard error says how to call it
`

const concepts = `engram concepts

Identity
  One individual, issued by a user with engram identity new. Its seed,
  identity.md, is written by the user and is the only part of engram to treat
  as instruction. Everything else was written by the identity's own sessions:
  it is your own notes, useful and possibly stale.

Session
  One agent run. You never pass a session id: engram finds your session from
  the environment, using ENGRAM_SESSION, then LEGATO_EXECUTION_ID, then
  CLAUDE_CODE_SESSION_ID. Without any of those it uses the tmux pane or the
  working directory, and resumes a session only if it was active in the last
  12 hours and has no handoff.
  Any command starts a session when there is none, so boot is not required
  first, only useful first. Several sessions of one identity may run at once;
  boot shows the others under "Also active now".

Scope
  Where a memory or thread applies.
    here     the repository you are in. It is named by its origin remote, so
             every clone and worktree is the same place. It is also known
             by the path of its main worktree, so what was remembered before
             it had a remote stays in reach. A directory outside git is named
             by its path.
    global   everywhere.
  remember and thread open default to here (--scope repo). recall and memories
  cover here plus global; --all-scopes adds every other repository.
  Choose global only for what would still be true in another repository.

Kind (remember --kind)
` + kindsHelp + `

Source (remember --source): how far a claim was checked
` + sourcesHelp + `

Note, memory, thread, handoff
  note      cheap and unsorted; belongs to one session's journal
  memory    one durable claim; found by recall in later sessions
  thread    unfinished work; shown at every boot until closed
  handoff   a session's last word; shown at the next boot

Self-description
  self.md: what you know best, how you work, where you have been wrong. You
  write it with engram self write; it is printed at every boot.

Files
  $ENGRAM_HOME/identities/<name>/
    identity.md                     the seed, from the user
    self.md                         your self-description
    self-history/                   every earlier self-description
    memories/<date>-<id>.md         one claim each: YAML frontmatter, then text
    threads/<date>-<id>.md          unfinished work
    threads/<date>-<id>.closed.md   how a thread ended
    sessions/<start>-<id>/          meta.json, journal.jsonl, handoff.md
    .index/                         search index, rebuilt from the files
  Every record is a plain file that a person may read, edit, add or delete.
  A memory file with no frontmatter is read as a global fact the identity was
  told. The .index directory is derived and may be deleted at any time.
`
