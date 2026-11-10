package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests cover what a program that launches agents, such as Legato,
// relies on: it reads engram's answers without parsing prose.

func decode[T any](t *testing.T, out string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	return v
}

type jsonError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func TestBootTeachesHowToKeepTheIdentityCurrentAndNeverCutsIt(t *testing.T) {
	h := issued(t, "ada").with("LEGATO_EXECUTION_ID", "ex-one")
	for _, budget := range []string{"3000", "1"} {
		_, packet := h.boot("--budget", budget)
		_, practice, ok := strings.Cut(packet, "## Keeping yourself current\n")
		if !ok {
			t.Fatalf("--budget %s: packet has no practice section:\n%s", budget, packet)
		}
		practice, _, _ = strings.Cut(practice, "\n## ")
		mustContain(t, practice,
			"engram note", "engram remember", "--source", "observed", "inferred", "--scope",
			"engram thread open", "engram thread close", "engram self write",
			"Boot again", "compacted", "natural stopping points", "engram handoff", "engram session end")
		mustNotContain(t, practice, "cut to fit")
	}
}

func TestWhereNeedsNoIdentityAndStartsNothing(t *testing.T) {
	h := newHarness(t)
	store := filepath.Join(h.home, "store")
	dir := repo(t, "git@github.com:cpave3/engram.git")
	h = h.in(dir).with("ENGRAM_HOME", store)

	report := decode[struct {
		Binary  string `json:"binary"`
		Version string `json:"version"`
		Store   struct {
			Root   string `json:"root"`
			Source string `json:"source"`
			Exists bool   `json:"exists"`
		} `json:"store"`
		Place *struct {
			Dir     string   `json:"dir"`
			Primary string   `json:"primary"`
			Scopes  []string `json:"scopes"`
		} `json:"place"`
	}](t, h.run("where", "--json"))
	if !filepath.IsAbs(report.Binary) || report.Version == "" {
		t.Fatalf("binary %q, version %q: want an absolute path and a version", report.Binary, report.Version)
	}
	if report.Store.Root != store || report.Store.Source != "ENGRAM_HOME" || report.Store.Exists {
		t.Fatalf("store = %+v, want %s from ENGRAM_HOME, not existing", report.Store, store)
	}
	if report.Place != nil {
		t.Fatalf("place without --dir: %+v", report.Place)
	}

	out := h.run("where", "--dir", ".", "--json")
	place := decode[struct {
		Place struct {
			Dir     string   `json:"dir"`
			Primary string   `json:"primary"`
			Scopes  []string `json:"scopes"`
		} `json:"place"`
	}](t, out).Place
	if place.Dir != dir || place.Primary != "repo:github.com/cpave3/engram" ||
		len(place.Scopes) != 2 || place.Scopes[0] != place.Primary || !strings.HasPrefix(place.Scopes[1], "dir:") {
		t.Fatalf("place = %+v", place)
	}
	mustContain(t, h.run("where", "--dir", dir), "store", "repo:github.com/cpave3/engram", "missing")

	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("where created the store: %v", err)
	}
	if code, _, _ := h.exec("", "where", "--dir", filepath.Join(dir, "nope")); code != 1 {
		t.Fatalf("where --dir on a missing directory: exit %d, want 1", code)
	}
}

func TestWhereSaysWhichSettingChoseTheStore(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		env        []string
		root, from string
	}{
		{[]string{"ENGRAM_HOME", "/e", "XDG_DATA_HOME", "/x", "HOME", "/h"}, "/e", "ENGRAM_HOME"},
		{[]string{"ENGRAM_HOME", "", "XDG_DATA_HOME", "/x", "HOME", "/h"}, "/x/engram", "XDG_DATA_HOME"},
		{[]string{"ENGRAM_HOME", "", "HOME", "/h"}, "/h/.local/share/engram", "HOME"},
	} {
		got := decode[struct {
			Store struct{ Root, Source string } `json:"store"`
		}](t, h.with(tc.env...).run("where", "--json")).Store
		if got.Root != tc.root || got.Source != tc.from {
			t.Fatalf("%v: store %+v, want %s from %s", tc.env, got, tc.root, tc.from)
		}
	}
}

func TestFailuresAProgramMustTellApartHaveTheirOwnExitAndCode(t *testing.T) {
	h := issued(t, "ada").with("LEGATO_EXECUTION_ID", "ex-one")
	missing := h.with("ENGRAM_HOME", filepath.Join(h.home, "nowhere"))
	for _, tc := range []struct {
		name string
		h    *harness
		args []string
		exit int
		code string
	}{
		{"unknown identity", h, []string{"boot", "--as", "grace"}, 3, "unknown_identity"},
		{"missing store", missing, []string{"boot", "--as", "ada"}, 4, "store_missing"},
		{"missing store, no name", missing.with("ENGRAM_IDENTITY", ""), []string{"boot"}, 4, "store_missing"},
		{"existing identity", h, []string{"identity", "new", "ada", "Someone else."}, 5, "identity_exists"},
		{"no identity", h.with("ENGRAM_IDENTITY", ""), []string{"boot"}, 2, "no_identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, stderr := tc.h.exec("", tc.args...)
			if code != tc.exit || out != "" || stderr == "" {
				t.Fatalf("exit %d, stdout %q, stderr %q: want exit %d and only a message", code, out, stderr, tc.exit)
			}
			code, out, _ = tc.h.exec("", append(tc.args, "--json")...)
			got := decode[jsonError](t, out)
			if code != tc.exit || got.Error.Code != tc.code || got.Error.Message == "" {
				t.Fatalf("with --json: exit %d, %+v: want exit %d, code %s", code, got, tc.exit, tc.code)
			}
		})
	}
	mustContain(t, h.run("identity", "show", "ada"), "Specialist in SQLite concurrency")
}

func TestIdentityNewWithJSONSaysWhereItWasIssued(t *testing.T) {
	h := newHarness(t).with("ENGRAM_HOME", "")
	h = h.with("XDG_DATA_HOME", h.home)
	code, out, stderr := h.exec("The Reeve runs Legato.\n", "identity", "new", "reeve", "--file", "-", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	got := decode[struct{ Identity, Dir string }](t, out)
	if got.Identity != "reeve" || got.Dir != filepath.Join(h.home, "engram", "identities", "reeve") {
		t.Fatalf("got %+v", got)
	}
}

func TestBootWithJSONGivesThePacketAndItsSession(t *testing.T) {
	h := issued(t, "ada").with("LEGATO_EXECUTION_ID", "ex-one")
	type boot struct {
		Packet    string    `json:"packet"`
		SessionID string    `json:"session_id"`
		State     string    `json:"state"`
		Identity  string    `json:"identity"`
		Started   time.Time `json:"started"`
	}
	first := decode[boot](t, h.run("boot", "--json"))
	if first.State != "new" || first.Identity != "ada" || first.SessionID == "" || !first.Started.Equal(h.clock.now) {
		t.Fatalf("first boot: %+v", first)
	}
	mustContain(t, first.Packet, "# You are ada", "Session "+first.SessionID+" (new)", "## Keeping yourself current")

	h.clock.advance(time.Minute)
	again := decode[boot](t, h.run("boot", "--json"))
	if again.State != "resumed" || again.SessionID != first.SessionID {
		t.Fatalf("second boot: %+v, want session %s resumed", again, first.SessionID)
	}
}

func TestEndedSessionLeavesAlsoActiveButResumesByItsKey(t *testing.T) {
	h := issued(t, "ada")
	one := h.with("LEGATO_EXECUTION_ID", "ex-one")
	two := h.with("LEGATO_EXECUTION_ID", "ex-two")
	oneID, _ := one.boot()
	one.run("note", "Paused before the migration.")
	_, packet := two.boot()
	mustContain(t, packet, "## Also active now", oneID)

	mustContain(t, one.run("session", "end"), "ended session "+oneID)
	h.clock.advance(time.Minute)
	_, packet = two.boot()
	mustNotContain(t, packet, "Also active now")
	// Ended without a handoff, it leaves its notes to the next session.
	mustContain(t, h.with("LEGATO_EXECUTION_ID", "ex-three").run("boot"), "## Last session", oneID, "Paused before the migration.")

	// A restarted run carries its key forward and gets the same session back.
	h.clock.advance(time.Hour)
	resumedID, packet := one.boot()
	if resumedID != oneID {
		t.Fatalf("boot after end started %s, want %s resumed", resumedID, oneID)
	}
	mustContain(t, packet, "resumed", "Paused before the migration.")
	one.run("note", "Back at it.")
	_, packet = two.boot()
	mustContain(t, packet, "## Also active now", oneID, "Back at it.")
}

func TestSessionEndNeverStartsASessionAndIsIdempotent(t *testing.T) {
	h := issued(t, "ada").with("ENGRAM_SESSION", "run-one")
	code, out, _ := h.exec("", "session", "end", "--as", "ada", "--json")
	if got := decode[jsonError](t, out); code != 1 || got.Error.Code != "no_session" {
		t.Fatalf("ending with no session: exit %d, %+v", code, got)
	}
	if entries, _ := os.ReadDir(filepath.Join(h.home, "identities", "ada", "sessions")); len(entries) > 0 {
		t.Fatalf("session end started a session")
	}

	id, _ := h.boot("--as", "ada")
	h.run("session", "end", "--as", "ada")
	mustContain(t, h.run("session", "end", "--as", "ada"), "already ended")
	got := decode[struct {
		SessionID    string `json:"session_id"`
		AlreadyEnded bool   `json:"already_ended"`
	}](t, h.run("session", "end", "--as", "ada", "--json"))
	if got.SessionID != id || !got.AlreadyEnded {
		t.Fatalf("got %+v", got)
	}
}

func TestSessionFoundByPlaceIsNotResumedAfterItEnds(t *testing.T) {
	h := issued(t, "ada").with("TMUX", "socket,123,0", "TMUX_PANE", "%3")
	first, _ := h.boot("--as", "ada")
	h.run("session", "end", "--as", "ada")
	if next, _ := h.boot("--as", "ada"); next == first {
		t.Fatalf("a pane's ended session %s was resumed", first)
	}
}

func TestBareRepositoriesAndSubmodulesAreNamedByTheirOwnDirectory(t *testing.T) {
	h := newHarness(t)
	root := t.TempDir()
	gitRun := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "protocol.file.allow=always",
			"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun(root, "init", "--quiet", "--bare", "bare.git")
	gitRun(root, "init", "--quiet", "lib")
	gitRun(filepath.Join(root, "lib"), "commit", "--quiet", "--allow-empty", "-m", "lib")
	gitRun(root, "init", "--quiet", "app")
	gitRun(filepath.Join(root, "app"), "submodule", "--quiet", "add", filepath.Join(root, "lib"), "lib")

	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]string{
		"bare.git": "dir:" + filepath.Join(resolved, "bare.git"),
		"app":      "dir:" + filepath.Join(resolved, "app"),
		"app/lib":  "dir:" + filepath.Join(resolved, "app", "lib"),
	} {
		got := decode[struct {
			Place struct{ Primary string } `json:"place"`
		}](t, h.run("where", "--dir", filepath.Join(root, dir), "--json")).Place.Primary
		if got != want {
			t.Fatalf("%s: primary %s, want %s", dir, got, want)
		}
	}
}

func TestVersionIsPrinted(t *testing.T) {
	h := newHarness(t)
	mustContain(t, h.run("version"), "engram ")
	if v := decode[struct{ Version string }](t, h.run("version", "--json")).Version; v == "" {
		t.Fatal("version --json has no version")
	}
}
