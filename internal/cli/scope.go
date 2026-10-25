package cli

import (
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cpave3/engram/internal/store"
)

// place is where a command runs.
type place struct {
	primary string   // the scope new records get
	scopes  []string // every scope that counts as here, primary first
}

func (a *app) place() place {
	if a.where == nil {
		where := placeOf(a.env.Cwd)
		a.where = &where
	}
	return *a.where
}

// here is the scope given to what is remembered in this place.
func (a *app) here() string { return a.place().primary }

func (a *app) isHere(scope string) bool { return slices.Contains(a.place().scopes, scope) }

// reach is what a search covers unless asked for more: global, and here.
func (a *app) reach() []string {
	return append([]string{store.ScopeGlobal}, a.place().scopes...)
}

func (a *app) inReach(scope string) bool {
	return scope == store.ScopeGlobal || a.isHere(scope)
}

// scopeLabel shortens a scope for display relative to where the command runs.
func (a *app) scopeLabel(scope string) string {
	switch {
	case scope == store.ScopeGlobal:
		return "global"
	case a.isHere(scope):
		return "here"
	}
	return scope
}

// scopeFlag turns a --scope value into the scope to record.
func (a *app) scopeFlag(value string) (string, error) {
	switch value {
	case "repo":
		return a.here(), nil
	case "global":
		return store.ScopeGlobal, nil
	}
	return "", usagef("unknown --scope %q: use repo or global", value)
}

// placeOf identifies a working directory. A repository is named by its origin
// remote, so every clone and worktree is one place. It also stays known by
// the path of its main worktree, which is its only name before it has a
// remote: memories saved then remain in reach afterwards. Outside a
// repository the place is the directory itself.
func placeOf(cwd string) place {
	var scopes []string
	if key := remoteKey(git(cwd, "remote", "get-url", "origin")); key != "" {
		scopes = append(scopes, "repo:"+key)
	}
	if common := git(cwd, "rev-parse", "--path-format=absolute", "--git-common-dir"); common != "" {
		scopes = append(scopes, "dir:"+filepath.Dir(common))
	} else {
		if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
			cwd = resolved
		}
		scopes = append(scopes, "dir:"+filepath.Clean(cwd))
	}
	return place{primary: scopes[0], scopes: scopes}
}

func git(cwd string, args ...string) string {
	out, err := exec.Command("git", append([]string{"-C", cwd}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// remoteKey reduces a remote URL to host/path, so the SSH and HTTPS forms of
// one repository agree. Remotes that are local paths have no key.
func remoteKey(remote string) string {
	var host, path string
	switch {
	case remote == "":
		return ""
	case strings.Contains(remote, "://"):
		u, err := url.Parse(remote)
		if err != nil || u.Scheme == "file" {
			return ""
		}
		host, path = u.Hostname(), u.Path
	case strings.Contains(remote, ":") && !strings.HasPrefix(remote, "/"):
		host, path, _ = strings.Cut(remote, ":")
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
	default:
		return ""
	}
	path = strings.Trim(strings.TrimSuffix(strings.Trim(path, "/"), ".git"), "/")
	if host == "" || path == "" {
		return ""
	}
	return strings.ToLower(host + "/" + path)
}
