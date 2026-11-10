package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cpave3/engram/internal/cli"
)

// clock is shared between harnesses that stand for concurrent sessions.
type clock struct{ now time.Time }

func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

// harness drives engram the way an agent does: through its command line,
// against a throwaway store and working directory.
type harness struct {
	t     *testing.T
	home  string
	cwd   string
	env   map[string]string
	clock *clock
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return &harness{
		t:     t,
		home:  t.TempDir(),
		cwd:   t.TempDir(),
		env:   map[string]string{},
		clock: &clock{now: time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)},
	}
}

// with returns a harness over the same store and clock with extra environment,
// standing for another process.
func (h *harness) with(kv ...string) *harness {
	h.t.Helper()
	next := *h
	next.env = maps.Clone(h.env)
	for i := 0; i+1 < len(kv); i += 2 {
		next.env[kv[i]] = kv[i+1]
	}
	return &next
}

func (h *harness) in(cwd string) *harness {
	next := *h
	next.cwd = cwd
	return &next
}

func (h *harness) exec(stdin string, args ...string) (code int, stdout, stderr string) {
	h.t.Helper()
	env := map[string]string{"ENGRAM_HOME": h.home}
	maps.Copy(env, h.env)
	var out, errOut bytes.Buffer
	code = cli.Run(args, cli.Env{
		Stdin:  strings.NewReader(stdin),
		Stdout: &out,
		Stderr: &errOut,
		Getenv: func(key string) string { return env[key] },
		Cwd:    h.cwd,
		Now:    func() time.Time { return h.clock.now },
	})
	return code, out.String(), errOut.String()
}

// run executes a command that must succeed and returns its stdout.
func (h *harness) run(args ...string) string {
	h.t.Helper()
	code, out, errOut := h.exec("", args...)
	if code != 0 {
		h.t.Fatalf("engram %s: exit %d\nstderr: %s", strings.Join(args, " "), code, errOut)
	}
	return out
}

func mustContain(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("output does not contain %q:\n%s", want, got)
		}
	}
}

func mustNotContain(t *testing.T, got string, unwanted ...string) {
	t.Helper()
	for _, s := range unwanted {
		if strings.Contains(got, s) {
			t.Fatalf("output unexpectedly contains %q:\n%s", s, got)
		}
	}
}

func TestIssuedIdentityCanBeShownAndListed(t *testing.T) {
	h := newHarness(t)
	h.run("identity", "new", "ada", "Specialist in SQLite concurrency for Go services.")

	mustContain(t, h.run("identity", "show", "ada"), "Specialist in SQLite concurrency for Go services.")
	mustContain(t, h.run("identity", "list"), "ada")
}

// issued returns a harness acting as a freshly issued identity.
func issued(t *testing.T, name string) *harness {
	t.Helper()
	h := newHarness(t)
	h.run("identity", "new", name, "Specialist in SQLite concurrency for Go services.")
	return h.with("ENGRAM_IDENTITY", name)
}

var rememberedID = regexp.MustCompile(`remembered ([0-9a-z]{8})`)

// remember stores a claim and returns its id.
func (h *harness) remember(args ...string) string {
	h.t.Helper()
	out := h.run(append([]string{"remember"}, args...)...)
	match := rememberedID.FindStringSubmatch(out)
	if match == nil {
		h.t.Fatalf("remember did not report an id:\n%s", out)
	}
	return match[1]
}

func TestRememberedClaimIsRecalledByMeaningfulWords(t *testing.T) {
	h := issued(t, "ada")
	id := h.remember("Deferred transactions that upgrade to a write fail with SQLITE_BUSY at once.",
		"--kind", "pitfall", "--source", "observed", "--tags", "sqlite,locking")
	h.remember("The user prefers Taskfile targets over Makefiles.", "--kind", "preference", "--source", "told")

	h.clock.advance(3 * 24 * time.Hour)
	out := h.run("recall", "why did my transaction upgrade fail")

	mustContain(t, out, id, "Deferred transactions that upgrade", "pitfall", "observed", "3d ago", "sqlite, locking")
	mustNotContain(t, out, "Taskfile")
}

// repo makes a git repository with the given origin remote and returns its path.
func repo(t *testing.T, origin string) string {
	t.Helper()
	dir := t.TempDir()
	gitInit(t, dir, origin)
	return dir
}

// gitInit turns dir into a git repository with the given origin remote.
func gitInit(t *testing.T, dir, origin string) {
	t.Helper()
	for _, args := range [][]string{{"init", "--quiet"}, {"remote", "add", "origin", origin}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestRepoScopedMemoryStaysInItsRepository(t *testing.T) {
	h := issued(t, "ada")
	legato := h.in(repo(t, "git@github.com:cpave3/legato.git"))
	engram := h.in(repo(t, "https://github.com/cpave3/engram.git"))

	legato.remember("Legato migrations take an immediate transaction before reading schema state.")
	legato.remember("A transaction that only reads should never be opened as a writer.", "--scope", "global")

	elsewhere := engram.run("recall", "transaction")
	mustContain(t, elsewhere, "only reads")
	mustNotContain(t, elsewhere, "Legato migrations")

	mustContain(t, legato.run("recall", "transaction"), "only reads", "Legato migrations")
	mustContain(t, engram.run("recall", "transaction", "--all-scopes"), "Legato migrations", "repo:github.com/cpave3/legato")
}

func TestSshAndHttpsClonesOfOneRepositoryShareAScope(t *testing.T) {
	h := issued(t, "ada")
	h.in(repo(t, "git@github.com:cpave3/Legato.git")).remember("Hooks report agent activity over the legato socket.")

	out := h.in(repo(t, "https://github.com/cpave3/legato")).run("recall", "hooks activity")
	mustContain(t, out, "Hooks report agent activity")
}

func TestCommandsSayHowToChooseAnIdentity(t *testing.T) {
	h := newHarness(t)
	h.run("identity", "new", "ada")

	code, _, stderr := h.exec("", "recall", "anything")
	if code != 2 {
		t.Fatalf("exit %d, want 2 (usage)", code)
	}
	mustContain(t, stderr, "--as", "ENGRAM_IDENTITY", "ada")

	code, _, stderr = h.exec("", "recall", "anything", "--as", "grace")
	if code != 3 {
		t.Fatalf("exit %d, want 3 (unknown identity)", code)
	}
	mustContain(t, stderr, `unknown identity "grace"`, "ada")
}

func TestIndexIsDisposableAndFollowsHandEdits(t *testing.T) {
	h := issued(t, "ada")
	h.remember("Busy timeouts do not cover lock upgrades.", "--scope", "global")
	mustContain(t, h.run("recall", "busy timeouts"), "lock upgrades")

	dir := filepath.Join(h.home, "identities", "ada")
	if err := os.RemoveAll(filepath.Join(dir, ".index")); err != nil {
		t.Fatal(err)
	}
	mustContain(t, h.run("recall", "busy timeouts"), "lock upgrades")

	// A memory is a markdown file a person can read, correct, add or delete.
	files, _ := filepath.Glob(filepath.Join(dir, "memories", "2026-10-07-*.md"))
	if len(files) != 1 {
		t.Fatalf("want one dated memory file, found %v", files)
	}
	original, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, string(original), "kind: fact", "scope: global", "source: observed", "session: 20261007-", "Busy timeouts do not cover lock upgrades.")

	edited := strings.Replace(string(original), "lock upgrades", "deferred write promotion", 1)
	if err := os.WriteFile(files[0], []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	handwritten := filepath.Join(dir, "memories", "from-cameron.md")
	if err := os.WriteFile(handwritten, []byte("Prefer WAL mode for every local database.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	mustContain(t, h.run("recall", "busy timeouts"), "deferred write promotion")
	mustContain(t, h.run("recall", "WAL mode"), "Prefer WAL mode", "from-cameron", "told")

	if err := os.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	mustContain(t, h.run("recall", "busy timeouts"), "No memories match")
}

var sessionID = regexp.MustCompile(`Session (\d{8}-\d{6}-[0-9a-z]{4})`)

// boot starts or resumes the harness's session and returns its id and packet.
func (h *harness) boot(args ...string) (id, packet string) {
	h.t.Helper()
	packet = h.run(append([]string{"boot"}, args...)...)
	match := sessionID.FindStringSubmatch(packet)
	if match == nil {
		h.t.Fatalf("boot did not name its session:\n%s", packet)
	}
	return match[1], packet
}

func TestBootTellsTheAgentWhoItIsAndHowToUseEngram(t *testing.T) {
	h := issued(t, "ada").with("LEGATO_EXECUTION_ID", "ex-one")

	_, packet := h.boot()

	mustContain(t, packet, "You are ada", "Specialist in SQLite concurrency for Go services.",
		"engram recall", "engram note", "engram remember", "engram handoff")
}

func TestBootingAgainInOneSessionResumesItWithItsNotes(t *testing.T) {
	h := issued(t, "ada").with("LEGATO_EXECUTION_ID", "ex-one")
	first, _ := h.boot()
	h.run("note", "Only the recall path opens SQLite.")

	// An agent whose context was compacted boots again to get its bearings.
	h.clock.advance(20 * time.Minute)
	second, packet := h.boot()

	if second != first {
		t.Fatalf("second boot started session %s, want it to resume %s", second, first)
	}
	mustContain(t, packet, "resumed", "Only the recall path opens SQLite.", "20m ago")
}

func TestNextSessionStartsFromThePreviousHandoff(t *testing.T) {
	h := issued(t, "ada")
	first := h.with("LEGATO_EXECUTION_ID", "ex-one")
	firstID, _ := first.boot()
	first.run("note", "Tried sqlite-vec; it needs CGO.")
	first.run("handoff", "Indexed memories with FTS5.\nOpen: decide how vectors are stored.")

	h.clock.advance(18 * time.Hour)
	_, packet := h.with("LEGATO_EXECUTION_ID", "ex-two").boot()

	mustContain(t, packet, "## Last session", firstID, "handoff 18h ago",
		"Indexed memories with FTS5.", "Open: decide how vectors are stored.")
	mustNotContain(t, packet, "Also active now", "resumed")
}

func TestSessionThatEndedWithoutAHandoffLeavesItsNotes(t *testing.T) {
	h := issued(t, "ada")
	first := h.with("LEGATO_EXECUTION_ID", "ex-one")
	first.run("note", "Reconcile takes no write lock when nothing changed.")
	first.run("note", "Next: stress it with parallel processes.")

	h.clock.advance(5 * time.Hour)
	_, packet := h.with("LEGATO_EXECUTION_ID", "ex-two").boot()

	mustContain(t, packet, "## Last session", "no handoff",
		"Reconcile takes no write lock when nothing changed.", "Next: stress it with parallel processes.")
}

func TestConcurrentSessionsOfOneIdentitySeeEachOtherAndBothWrite(t *testing.T) {
	h := issued(t, "ada")
	one := h.with("LEGATO_EXECUTION_ID", "ex-one", "LEGATO_TASK_ID", "task-one")
	two := h.with("LEGATO_EXECUTION_ID", "ex-two")

	oneID, _ := one.boot()
	one.run("note", "Rewriting the index reconcile loop.")
	h.clock.advance(10 * time.Minute)
	twoID, packet := two.boot()

	if oneID == twoID {
		t.Fatalf("two agents share session %s", oneID)
	}
	mustContain(t, packet, "## Also active now", oneID, "task-one", "10m ago", "Rewriting the index reconcile loop.")

	one.remember("Session one learned how reconcile diffs files.", "--scope", "global")
	two.remember("Session two learned how reconcile handles deletions.", "--scope", "global")
	mustContain(t, two.run("recall", "reconcile"), "Session one learned", "Session two learned")

	_, again := one.boot()
	mustContain(t, again, "## Also active now", twoID)
}

var openedThread = regexp.MustCompile(`opened thread ([0-9a-z]{8})`)

func TestOpenThreadsCarryAcrossSessionsUntilClosed(t *testing.T) {
	h := issued(t, "ada")
	first := h.with("LEGATO_EXECUTION_ID", "ex-one")
	out := first.run("thread", "open", "Decide how vectors are stored", "--body", "A blob per memory, or a separate file?")
	match := openedThread.FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("thread open did not report an id:\n%s", out)
	}
	id := match[1]

	h.clock.advance(3 * 24 * time.Hour)
	second := h.with("LEGATO_EXECUTION_ID", "ex-two")
	_, packet := second.boot()
	mustContain(t, packet, "## Open threads", id, "Decide how vectors are stored", "3d ago")
	mustContain(t, second.run("thread", "list"), id, "Decide how vectors are stored", "A blob per memory, or a separate file?")

	second.run("thread", "close", id, "Blobs in the index, which is disposable anyway.")

	mustContain(t, second.run("thread", "list"), "No open threads")
	_, packet = h.with("LEGATO_EXECUTION_ID", "ex-three").boot()
	mustNotContain(t, packet, "Open threads", "Decide how vectors are stored")

	code, _, stderr := second.exec("", "thread", "close", id, "Again.")
	if code != 1 {
		t.Fatalf("closing a closed thread: exit %d, want 1", code)
	}
	mustContain(t, stderr, "already closed")
}

func TestBootMapsWhatTheIdentityKnowsAndWhere(t *testing.T) {
	h := issued(t, "ada")
	legato := h.in(repo(t, "git@github.com:cpave3/legato.git")).with("LEGATO_EXECUTION_ID", "ex-one")
	legato.remember("Migrations take an immediate transaction.", "--tags", "sqlite,migrations")
	legato.remember("Hooks must drain stdin before gating.", "--kind", "pitfall", "--tags", "hooks")
	legato.remember("Deferred upgrades fail with SQLITE_BUSY.", "--kind", "pitfall", "--scope", "global", "--tags", "sqlite")

	h.clock.advance(30 * time.Hour)
	engram := h.in(repo(t, "git@github.com:cpave3/engram.git")).with("LEGATO_EXECUTION_ID", "ex-two")
	_, packet := engram.boot()

	mustContain(t, packet, "## What you know", "3 memories", "here 0", "global 1", "repo:github.com/cpave3/legato 2",
		"pitfall 2", "fact 1", "sqlite 2", "--all-scopes")
	// Only what a plain recall would reach from here is quoted.
	mustContain(t, packet, "Deferred upgrades fail with SQLITE_BUSY.", "30h ago")
	mustNotContain(t, packet, "Hooks must drain stdin")
}

func TestBootIncludesTheSelfDescriptionTheIdentityKeeps(t *testing.T) {
	h := issued(t, "ada")
	self := filepath.Join(h.home, "identities", "ada", "self.md")
	if err := os.WriteFile(self, []byte("I check lock behaviour with real processes, not mocks.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, packet := h.boot()

	mustContain(t, packet, "## Self-description", "I check lock behaviour with real processes, not mocks.")
}

func TestBootFitsItsBudgetByCuttingTheLeastUrgentSectionsFirst(t *testing.T) {
	h := issued(t, "ada")
	first := h.with("LEGATO_EXECUTION_ID", "ex-one")
	for i := range 30 {
		h.clock.advance(time.Second)
		first.remember(fmt.Sprintf("Fact %d about write-ahead logging and checkpoints.", i), "--scope", "global", "--tags", fmt.Sprintf("topic-%d", i))
	}
	first.run("thread", "open", "Decide how vectors are stored")
	first.run("handoff", strings.Repeat("A long handoff line about the reconcile loop.\n", 60))
	h.clock.advance(3 * time.Hour)
	second := h.with("LEGATO_EXECUTION_ID", "ex-two")

	_, roomy := second.boot("--budget", "100000")
	_, tight := second.boot("--budget", "600")

	if len(tight) > 600*4 {
		t.Fatalf("packet is %d characters, over a 600-token budget:\n%s", len(tight), tight)
	}
	if len(roomy) <= len(tight) {
		t.Fatalf("a roomy budget (%d chars) should hold more than a tight one (%d chars)", len(roomy), len(tight))
	}
	mustContain(t, roomy, "Fact 29 about write-ahead logging", strings.Repeat("A long handoff line about the reconcile loop.\n", 60))
	// Who you are, how to use engram and unfinished work outlast history and the map.
	mustContain(t, tight, "Specialist in SQLite concurrency", "engram recall", "Decide how vectors are stored", "cut to fit --budget 600", "## Keeping yourself current")
	mustNotContain(t, tight, "Fact 29 about write-ahead logging")
}

func TestOutsideAHarnessASessionIsResumedOnlyWhileFresh(t *testing.T) {
	// A bare tmux pane says where an agent runs, not which run it is.
	h := issued(t, "ada").with("TMUX", "/tmp/tmux-1000/default,9546,7", "TMUX_PANE", "%7")
	first, _ := h.boot()
	h.run("note", "Working in a plain tmux pane.")

	h.clock.advance(time.Hour)
	if again, _ := h.boot(); again != first {
		t.Fatalf("an hour later boot started %s, want it to resume %s", again, first)
	}

	h.clock.advance(13 * time.Hour)
	next, packet := h.boot()
	if next == first {
		t.Fatalf("a boot the next day resumed %s, want a new session", first)
	}
	mustContain(t, packet, "## Last session", first, "Working in a plain tmux pane.")

	h.run("handoff", "Finished in the pane.")
	if after, _ := h.boot(); after == next {
		t.Fatalf("boot after a handoff resumed %s, want a new session", next)
	}
}

func TestBootNewStartsAFreshSessionForTheSameAgent(t *testing.T) {
	h := issued(t, "ada").with("LEGATO_EXECUTION_ID", "ex-one")
	first, _ := h.boot()
	h.clock.advance(time.Minute)

	second, packet := h.boot("--new")

	if second == first {
		t.Fatalf("boot --new resumed %s", first)
	}
	mustContain(t, packet, "(new)")
}

func TestLongTextIsReadFromStandardInput(t *testing.T) {
	h := issued(t, "ada")
	code, _, stderr := h.with("LEGATO_EXECUTION_ID", "ex-one").exec(
		"Did: built recall.\n\nOpen: --all-scopes ranking.\n", "handoff", "--file", "-")
	if code != 0 {
		t.Fatalf("handoff --file -: exit %d: %s", code, stderr)
	}

	h.clock.advance(3 * time.Hour)
	_, packet := h.with("LEGATO_EXECUTION_ID", "ex-two").boot()

	mustContain(t, packet, "Did: built recall.", "Open: --all-scopes ranking.")
}

func TestTextStartingWithADashFollowsADoubleDash(t *testing.T) {
	h := issued(t, "ada")
	h.remember("--scope", "global", "--", "-race finds the unsynchronised clock in tests.")

	mustContain(t, h.run("recall", "unsynchronised clock"), "-race finds the unsynchronised clock in tests.")
}

func TestParallelSessionsWriteAndSearchWithoutLosingAnything(t *testing.T) {
	h := issued(t, "ada")
	const agents, each = 8, 6

	var wg sync.WaitGroup
	failures := make(chan string, agents*each*2)
	for agent := range agents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session := h.with("LEGATO_EXECUTION_ID", fmt.Sprintf("ex-%d", agent))
			for i := range each {
				claim := fmt.Sprintf("Agent %d finding %d about checkpoint starvation.", agent, i)
				if code, _, stderr := session.exec("", "remember", claim, "--scope", "global"); code != 0 {
					failures <- "remember: " + stderr
				}
				if code, _, stderr := session.exec("", "recall", "checkpoint starvation"); code != 0 || stderr != "" {
					failures <- "recall: " + stderr
				}
			}
		}()
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}

	var found struct {
		Memories []struct {
			Body string `json:"body"`
		} `json:"memories"`
	}
	out := h.run("recall", "checkpoint starvation", "--limit", "1000", "--json")
	if err := json.Unmarshal([]byte(out), &found); err != nil {
		t.Fatalf("recall --json: %v\n%s", err, out)
	}
	if len(found.Memories) != agents*each {
		t.Fatalf("recalled %d memories, want %d", len(found.Memories), agents*each)
	}
}

// Every command an agent can run, as typed.
var commandNames = []string{
	"identity new", "identity list", "identity show",
	"boot", "note", "handoff", "session end",
	"remember", "recall", "memories", "show", "edit", "remove",
	"thread open", "thread list", "thread close",
	"self show", "self write",
	"where", "version",
}

func TestHelpOverviewShowsASessionAndListsEveryCommand(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{}, {"help"}, {"--help"}, {"-h"}} {
		code, out, stderr := h.exec("", args...)
		if code != 0 {
			t.Fatalf("engram %v: exit %d: %s", args, code, stderr)
		}
		mustContain(t, out, "engram boot", "engram recall", "engram handoff", "ENGRAM_IDENTITY",
			"engram help <command>", "engram help concepts", "<<'EOF'", "Exit status")
		mustContain(t, out, commandNames...)
	}
}

func TestEveryCommandHasItsOwnHelp(t *testing.T) {
	h := newHarness(t)
	for _, name := range commandNames {
		words := strings.Fields(name)
		viaHelp := h.run(append([]string{"help"}, words...)...)
		mustContain(t, viaHelp, "engram "+name, "Usage:", "Prints:")

		code, viaFlag, stderr := h.exec("", append(words, "--help")...)
		if code != 0 {
			t.Fatalf("engram %s --help: exit %d: %s", name, code, stderr)
		}
		if viaFlag != viaHelp {
			t.Fatalf("engram %s --help differs from engram help %s", name, name)
		}
	}
	mustContain(t, h.run("help", "thread"), "thread open", "thread list", "thread close", "engram help thread open")
}

func TestRememberHelpExplainsEveryChoiceAnAgentMustMake(t *testing.T) {
	h := newHarness(t)
	out := h.run("help", "remember")

	mustContain(t, out, "--kind", "fact", "pitfall", "preference", "procedure")
	mustContain(t, out, "--source", "observed", "told", "inferred", "prior")
	mustContain(t, out, "--scope", "repo", "global")
	mustContain(t, out, "--anchor", "--tags", "--file", "--batch", "--as", "ENGRAM_IDENTITY")
	mustContain(t, out, "Examples:", "<<'EOF'", `{"claim"`, "remembered <id>")
}

func TestConceptsExplainTheVocabulary(t *testing.T) {
	h := newHarness(t)
	out := h.run("help", "concepts")

	mustContain(t, out, "Identity", "Session", "Scope", "Kind", "Source", "Files",
		"identity.md", "LEGATO_EXECUTION_ID", "--all-scopes", "pitfall", "inferred", "prior", "handoff", ".index")
}

func TestMistakesPointAtTheHelpThatFixesThem(t *testing.T) {
	h := issued(t, "ada")
	for _, tc := range []struct {
		args  []string
		wants []string
	}{
		{[]string{"remembr", "typo"}, []string{`unknown command "remembr"`, "engram help"}},
		{[]string{"remember", "A claim.", "--kind", "hunch"}, []string{"--kind", "hunch", "engram help remember"}},
		{[]string{"recall", "words", "--frobnicate"}, []string{"frobnicate", "engram help recall"}},
		{[]string{"recall", "words", "--limit", "many"}, []string{"--limit", "engram help recall"}},
		{[]string{"thread"}, []string{"open", "list", "close", "engram help thread"}},
		{[]string{"thread", "close"}, []string{"thread id", "engram help thread close"}},
		{[]string{"help", "nonsense"}, []string{`no help for "nonsense"`, "engram help"}},
	} {
		code, _, stderr := h.exec("", tc.args...)
		if code != 2 {
			t.Fatalf("engram %v: exit %d, want 2 (usage)\n%s", tc.args, code, stderr)
		}
		mustContain(t, stderr, tc.wants...)
	}
}

func TestIndexDirectoryIsKeptOutOfVersionControl(t *testing.T) {
	h := issued(t, "ada")
	h.remember("A store kept in git should not carry its derived index.", "--scope", "global")
	h.run("recall", "derived index")

	ignore, err := os.ReadFile(filepath.Join(h.home, "identities", "ada", ".index", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(ignore)) != "*" {
		t.Fatalf(".index/.gitignore = %q, want it to ignore everything", ignore)
	}
}

func TestSourceSaysHowFarAClaimWasChecked(t *testing.T) {
	h := issued(t, "ada")
	// Background knowledge is neither seen in a session, told, nor derived there.
	h.remember("bbolt allows one writing process at a time.", "--source", "prior", "--scope", "global")
	mustContain(t, h.run("recall", "bbolt writing process"), "· prior ·")

	code, _, stderr := h.exec("", "remember", "Unlabelled.", "--source", "guess")
	if code != 2 {
		t.Fatalf("unknown source: exit %d, want 2", code)
	}
	mustContain(t, stderr, "observed, told, inferred, prior")
}

func TestMemoriesStayInReachWhenTheirDirectoryBecomesARepository(t *testing.T) {
	h := issued(t, "ada")
	project := h.in(t.TempDir())
	before := project.remember("Saved before this project had a git remote.")

	gitInit(t, project.cwd, "git@github.com:cpave3/engram.git")

	mustContain(t, project.run("recall", "saved before remote"), before, "· here ·")
	_, packet := project.boot()
	mustContain(t, packet, "Where: here 1, global 0.")
	mustContain(t, project.run("remember", "Saved after it had one."), "repo:github.com/cpave3/engram")
	mustContain(t, project.run("recall", "saved"), "Saved before", "Saved after")

	elsewhere := h.in(repo(t, "git@github.com:cpave3/legato.git"))
	mustContain(t, elsewhere.run("recall", "saved before remote"), "No memories match")
}

func TestMemoriesCanBeListedAndShownWithoutAQuery(t *testing.T) {
	h := issued(t, "ada")
	legato := h.in(repo(t, "git@github.com:cpave3/legato.git"))
	legato.remember("Hooks must drain stdin before gating.", "--kind", "pitfall", "--tags", "hooks")
	h.clock.advance(time.Hour)
	here := h.in(repo(t, "git@github.com:cpave3/engram.git"))
	first := here.remember("Only recall opens SQLite.\nEvery other command uses plain files.", "--tags", "sqlite,design", "--anchor", "internal/cli/memory.go")
	h.clock.advance(time.Hour)
	second := here.remember("Deferred upgrades fail with SQLITE_BUSY.", "--kind", "pitfall", "--source", "prior", "--scope", "global", "--tags", "sqlite")

	list := here.run("memories")
	mustContain(t, list, "2 memories", first, second, "Only recall opens SQLite.", "· prior ·")
	mustNotContain(t, list, "Hooks must drain", "Every other command")
	if strings.Index(list, second) > strings.Index(list, first) {
		t.Fatalf("memories are not newest first:\n%s", list)
	}

	mustContain(t, here.run("memories", "--all-scopes"), "3 memories", "Hooks must drain", "repo:github.com/cpave3/legato")
	pitfalls := here.run("memories", "--kind", "pitfall", "--all-scopes")
	mustContain(t, pitfalls, "2 memories", "Hooks must drain", "Deferred upgrades")
	mustNotContain(t, pitfalls, "Only recall opens")
	mustContain(t, here.run("memories", "--tag", "design"), "1 memory", first)
	mustContain(t, here.run("memories", "--source", "prior"), "1 memory", second)
	mustContain(t, here.run("memories", "--limit", "1"), second, "Showing 1 of 2")

	shown := here.run("show", first)
	mustContain(t, shown, "Only recall opens SQLite.", "Every other command uses plain files.",
		"repo:github.com/cpave3/engram", "internal/cli/memory.go", "sqlite, design",
		filepath.Join("identities", "ada", "memories", "2026-10-07-"+first+".md"))

	code, _, stderr := here.exec("", "show", "nosuchid")
	if code != 1 {
		t.Fatalf("show of an unknown id: exit %d, want 1", code)
	}
	mustContain(t, stderr, "nosuchid")
}

func TestShowPrintsAThreadWithHowItEnded(t *testing.T) {
	h := issued(t, "ada")
	out := h.run("thread", "open", "Decide how vectors are stored")
	id := openedThread.FindStringSubmatch(out)[1]
	code, out, stderr := h.exec("A blob per memory,\nor a separate file?\n", "thread", "open", "Pick a tokenizer", "--file", "-")
	if code != 0 {
		t.Fatalf("thread open --file -: exit %d: %s", code, stderr)
	}
	withBody := openedThread.FindStringSubmatch(out)[1]

	mustContain(t, h.run("show", withBody), "Pick a tokenizer", "A blob per memory,", "or a separate file?", "open")
	h.run("thread", "close", id, "Blobs in the index.")
	mustContain(t, h.run("show", id), "Decide how vectors are stored", "closed", "Blobs in the index.")
}

func TestSelfDescriptionIsWrittenThroughEngramAndEarlierVersionsAreKept(t *testing.T) {
	h := issued(t, "ada").with("LEGATO_EXECUTION_ID", "ex-one")
	mustContain(t, h.run("self", "show"), "No self-description yet", "engram self write")

	h.run("self", "write", "I check lock behaviour with real processes.")
	h.clock.advance(time.Hour)
	code, out, stderr := h.exec("I check lock behaviour with real processes.\nI was wrong about swarms.\n", "self", "write", "--file", "-")
	if code != 0 {
		t.Fatalf("self write --file -: exit %d: %s", code, stderr)
	}
	mustContain(t, out, "1 earlier version kept")

	mustContain(t, h.run("self", "show"), "I was wrong about swarms.")
	_, packet := h.boot()
	mustContain(t, packet, "## Self-description", "I was wrong about swarms.")

	// A person may edit self.md directly; the next write keeps their version too.
	dir := filepath.Join(h.home, "identities", "ada")
	if err := os.WriteFile(filepath.Join(dir, "self.md"), []byte("Edited by hand.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(time.Hour)
	mustContain(t, h.run("self", "write", "Third version."), "3 earlier versions kept")
	var kept []string
	history, _ := filepath.Glob(filepath.Join(dir, "self-history", "*.md"))
	for _, path := range history {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		kept = append(kept, string(data))
	}
	mustContain(t, strings.Join(kept, "\n"), "I check lock behaviour with real processes.", "I was wrong about swarms.", "Edited by hand.")
}

func TestManyMemoriesAreRememberedFromOneBatch(t *testing.T) {
	h := issued(t, "ada")
	batch := `{"claim": "It's the \"deferred\" transaction that fails.", "kind": "pitfall", "tags": ["sqlite", "locking"]}

{"claim": "The user wants task check to pass before a commit.", "kind": "preference", "source": "told", "tags": "user, workflow"}
{"claim": "Scoped to this repository.", "scope": "repo", "anchor": "db.go:40"}
`
	code, out, stderr := h.exec(batch, "remember", "--batch", "-", "--scope", "global", "--source", "prior")
	if code != 0 {
		t.Fatalf("remember --batch: exit %d: %s", code, stderr)
	}
	if got := len(rememberedID.FindAllString(out, -1)); got != 3 {
		t.Fatalf("batch reported %d memories, want 3:\n%s", got, out)
	}

	all := h.run("memories", "--limit", "10")
	mustContain(t, all, "3 memories", `It's the "deferred" transaction that fails.`, "· pitfall · global · prior ·",
		"· preference · global · told ·", "· fact · here · prior ·")
	mustContain(t, h.run("recall", "task check commit"), "tags: user, workflow")
	mustContain(t, h.run("recall", "scoped repository"), "anchor: db.go:40")

	// One bad line and nothing is saved.
	bad := `{"claim": "A fourth claim."}
{"claim": "A fifth claim.", "kind": "hunch"}
`
	code, _, stderr = h.exec(bad, "remember", "--batch", "-")
	if code != 2 {
		t.Fatalf("bad batch: exit %d, want 2", code)
	}
	mustContain(t, stderr, "line 2", "hunch")
	mustContain(t, h.run("memories"), "3 memories")

	code, _, stderr = h.exec(`{"text": "Wrong field name."}`, "remember", "--batch", "-")
	if code != 2 {
		t.Fatalf("unknown field: exit %d, want 2", code)
	}
	mustContain(t, stderr, "line 1", "text")
}

func TestBootIdentityCarriesToLaterCommands(t *testing.T) {
	for _, ambient := range [][]string{
		{"ENGRAM_SESSION", "run-one"},
		{"LEGATO_EXECUTION_ID", "ex-one"},
		{"CLAUDE_CODE_SESSION_ID", "claude-one"},
		{"TMUX", "socket,123,0", "TMUX_PANE", "%1"},
		nil,
	} {
		t.Run(fmt.Sprint(ambient), func(t *testing.T) {
			h := newHarness(t).with(ambient...)
			h.run("identity", "new", "fettle")
			packet := h.run("boot", "--as", "fettle")
			mustContain(t, packet, `engram recall "<query>" --as fettle`, "started 2026-10-07 09:00:00 UTC")
			mustNotContain(t, packet, "&lt;", "&gt;")
			opened := h.run("thread", "open", "Fix session identity")
			id := strings.TrimSpace(strings.TrimPrefix(opened, "opened thread "))
			mustContain(t, h.run("thread", "close", id, "Fixed"), "closed thread "+id)
			mustNotContain(t, h.run("thread", "list"), "Fix session identity")
			h.run("note", "Identity survived")
			mustContain(t, h.run("boot"), "resumed", "Identity survived")
		})
	}
}

func TestInferredIdentityIsScopedFreshAndUnambiguous(t *testing.T) {
	h := newHarness(t).with("ENGRAM_SESSION", "one")
	for _, name := range []string{"ada", "grace"} {
		h.run("identity", "new", name)
	}
	h.run("boot", "--as", "ada")
	code, _, stderr := h.with("ENGRAM_SESSION", "two").exec("", "note", "wrong run")
	if code != 2 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	h.run("boot", "--as", "grace")
	code, _, stderr = h.exec("", "note", "ambiguous")
	if code != 2 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	mustContain(t, stderr, "multiple identities", "--as")
	h.with("ENGRAM_IDENTITY", "ada").run("note", "Environment wins")
	h.with("ENGRAM_IDENTITY", "ada").run("note", "Flag wins", "--as", "grace")
	ada := h.run("boot", "--as", "ada")
	grace := h.run("boot", "--as", "grace")
	mustContain(t, ada, "Environment wins")
	mustNotContain(t, ada, "Flag wins")
	mustContain(t, grace, "Flag wins")
	mustNotContain(t, grace, "Environment wins")

	weak := newHarness(t)
	weak.run("identity", "new", "ada")
	weak.run("boot", "--as", "ada")
	weak.run("handoff", "Done")
	code, _, stderr = weak.exec("", "note", "after handoff")
	if code != 2 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	weak.run("boot", "--as", "ada")
	weak.clock.advance(13 * time.Hour)
	code, _, stderr = weak.exec("", "note", "too late")
	if code != 2 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestIdentityFlagBeforeCommand(t *testing.T) {
	for _, args := range [][]string{
		{"--as", "ada", "thread", "open", "Global flag"},
		{"--as=ada", "thread", "open", "Global flag"},
		{"thread", "--as", "ada", "open", "Global flag"},
		{"thread", "open", "--as", "ada", "Global flag"},
		{"thread", "open", "Global flag", "--as", "ada"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := newHarness(t)
			h.run("identity", "new", "ada")
			h.run(args...)
			mustContain(t, h.run("thread", "list", "--as", "ada"), "Global flag")
		})
	}
	h := newHarness(t)
	code, _, stderr := h.exec("", "--as")
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	mustContain(t, stderr, "--as", "name")
}

func TestWeakIdentityInferenceExcludesFinishedAndStaleMatches(t *testing.T) {
	for _, finished := range []bool{false, true} {
		t.Run(fmt.Sprint("handoff=", finished), func(t *testing.T) {
			h := newHarness(t)
			for _, name := range []string{"ada", "grace"} {
				h.run("identity", "new", name)
				h.run("boot", "--as", name)
			}
			code, _, stderr := h.exec("", "note", "ambiguous")
			if code != 2 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			mustContain(t, stderr, "multiple identities")
			if finished {
				h.run("handoff", "Done", "--as", "grace")
			} else {
				h.clock.advance(13 * time.Hour)
				h.run("note", "Keep ada active", "--as", "ada")
			}
			h.run("note", "Unique match")
			mustContain(t, h.run("boot", "--as", "ada"), "Unique match")
			mustNotContain(t, h.run("boot", "--as", "grace"), "Unique match")
		})
	}
}

func TestBootLabelsUTCWhenLocalDateDiffers(t *testing.T) {
	h := issued(t, "ada")
	h.clock.now = time.Date(2026, 10, 26, 9, 54, 14, 0, time.FixedZone("AEDT", 11*60*60))
	id, packet := h.boot()
	if !strings.HasPrefix(id, "20261025-225414-") {
		t.Fatalf("session id %s does not encode UTC", id)
	}
	mustContain(t, packet, "started 2026-10-25 22:54:14 UTC", "Session IDs use UTC")
	h.clock.advance(2 * time.Minute)
	mustContain(t, h.run("boot"), "resumed, started 2m ago", "started 2026-10-25 22:54:14 UTC")
}

func TestEditMemoryPreservesMetadataAndRefreshesRecall(t *testing.T) {
	h := issued(t, "ada")
	id := h.remember("Old zeppelin advice.", "--kind", "pitfall", "--scope", "global", "--source", "told", "--tags", "travel,design", "--anchor", "guide.go:42")
	mustContain(t, h.run("recall", "zeppelin"), id)
	files, err := filepath.Glob(filepath.Join(h.home, "identities", "ada", "memories", "*"+id+".md"))
	if err != nil || len(files) != 1 {
		t.Fatalf("memory file: %v %v", files, err)
	}
	before, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	h.clock.advance(time.Hour)
	mustContain(t, h.run("edit", id, "New submarine advice."), "edited "+id)
	shown := h.run("show", id)
	mustContain(t, shown, "New submarine advice.", "pitfall", "global", "told", "travel, design", "guide.go:42", "2026-10-07T09:00:00Z")
	mustNotContain(t, shown, "Old zeppelin advice.")
	after, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.SplitN(string(before), "\n---\n", 2)[0] != strings.SplitN(string(after), "\n---\n", 2)[0] {
		t.Fatalf("edit changed metadata:\nbefore: %s\nafter: %s", before, after)
	}
	mustContain(t, h.run("recall", "submarine"), id, "New submarine advice.")
	mustContain(t, h.run("recall", "zeppelin"), "No memories match")
	code, _, stderr := h.exec("Replacement from stdin.\nTwo lines.\n", "edit", id, "--file", "-")
	if code != 0 {
		t.Fatalf("edit stdin exit %d: %s", code, stderr)
	}
	mustContain(t, h.run("show", id), "Replacement from stdin.\nTwo lines.")
}

func TestRemoveMemoryRequiresConfirmationAndRefreshesRecall(t *testing.T) {
	h := issued(t, "ada")
	id := h.remember("Obsolete zeppelin advice.")
	kept := h.remember("Keep submarine advice.")
	mustContain(t, h.run("recall", "zeppelin"), id)
	code, _, stderr := h.exec("", "remove", id)
	if code != 2 {
		t.Fatalf("remove without confirmation exit %d: %s", code, stderr)
	}
	mustContain(t, stderr, "--yes")
	mustContain(t, h.run("show", id), "Obsolete zeppelin advice.")
	mustContain(t, h.run("remove", id, "--yes"), "removed "+id)
	code, _, stderr = h.exec("", "show", id)
	if code != 1 {
		t.Fatalf("show removed exit %d: %s", code, stderr)
	}
	mustContain(t, h.run("recall", "zeppelin"), "No memories match")
	mustNotContain(t, h.run("memories"), id)
	_, packet := h.boot()
	mustNotContain(t, packet, "Obsolete zeppelin advice.")
	mustContain(t, h.run("show", kept), "Keep submarine advice.")
	code, _, stderr = h.exec("", "remove", id, "--yes")
	if code != 1 {
		t.Fatalf("second remove exit %d: %s", code, stderr)
	}
	mustContain(t, stderr, id, "not found")
}

func TestMemoryMutationsRejectInvalidInputAndOtherIdentities(t *testing.T) {
	h := issued(t, "ada")
	id := h.remember("Original memory.")
	h.run("identity", "new", "grace")
	thread := openedThread.FindStringSubmatch(h.run("thread", "open", "Keep thread"))[1]
	for _, args := range [][]string{
		{"edit", id}, {"edit", id, "   "}, {"edit", id, "text", "--file", "-"},
		{"remove"}, {"remove", id, "extra", "--yes"},
	} {
		code, _, stderr := h.exec("", args...)
		if code != 2 {
			t.Fatalf("%v exit %d: %s", args, code, stderr)
		}
	}
	for _, args := range [][]string{
		{"edit", id, "Wrong identity", "--as", "grace"},
		{"remove", id, "--yes", "--as", "grace"},
		{"edit", thread, "Not a memory"}, {"remove", thread, "--yes"},
	} {
		code, _, stderr := h.exec("", args...)
		if code != 1 {
			t.Fatalf("%v exit %d: %s", args, code, stderr)
		}
		mustContain(t, stderr, "memory", "not found")
	}
	mustContain(t, h.run("show", id), "Original memory.")
	mustContain(t, h.run("show", thread), "Keep thread", "open")
}

func TestEditHandWrittenMemoryWithFileInput(t *testing.T) {
	h := issued(t, "ada")
	path := filepath.Join(h.home, "identities", "ada", "memories", "manual.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("Original handwritten claim.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := h.run("show", "manual")
	file := filepath.Join(t.TempDir(), "claim.txt")
	if err := os.WriteFile(file, []byte("Corrected handwritten claim.\nWith detail.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.run("edit", "manual", "--file", file)
	after := h.run("show", "manual")
	mustContain(t, after, "Corrected handwritten claim.\nWith detail.", "fact", "global", "told", path)
	// The loaded default creation time becomes explicit, not the edit time.
	created := regexp.MustCompile(`created:\s+(\S+)`)
	if created.FindStringSubmatch(before)[1] != created.FindStringSubmatch(after)[1] {
		t.Fatalf("edit changed creation time:\n%s\n%s", before, after)
	}
	h.run("remove", "manual", "--yes")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
}
