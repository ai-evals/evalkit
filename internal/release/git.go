package release

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// gitRepo is the local git adapter: os/exec of the git binary in dir.
// Read-only. Tags are never created locally; GitHub creates the tag when the
// draft release is published (see publish.go), so the job needs no push
// credentials (checkout uses persist-credentials: false).
type gitRepo struct{ dir string }

func (g gitRepo) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// resolve returns the full commit SHA of rev (`git rev-parse --verify rev^{commit}`).
func (g gitRepo) resolve(ctx context.Context, rev string) (string, error) {
	out, err := g.git(ctx, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
	return strings.TrimSpace(out), err
}

func (g gitRepo) tagNames(ctx context.Context, args ...string) (map[string]bool, error) {
	out, err := g.git(ctx, append([]string{"tag", "--list", "v*"}, args...)...)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, n := range strings.Fields(out) {
		names[n] = true
	}
	return names, nil
}

// releaseTags lists tags that ParseTag accepts, peeled to commits, with Rel
// computed against head from two calls: `git tag --merged head` and
// `git tag --contains head`.
func (g gitRepo) releaseTags(ctx context.Context, head string) ([]Tag, error) {
	out, err := g.git(ctx, "for-each-ref", "--format=%(refname:short) %(objectname) %(*objectname)", "refs/tags/v*")
	if err != nil {
		return nil, err
	}
	merged, err := g.tagNames(ctx, "--merged", head)
	if err != nil {
		return nil, err
	}
	contains, err := g.tagNames(ctx, "--contains", head)
	if err != nil {
		return nil, err
	}
	var tags []Tag
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, ok := ParseTag(f[0])
		if !ok {
			continue
		}
		commit := f[1]
		if len(f) == 3 { // annotated tag: %(*objectname) is the peeled commit
			commit = f[2]
		}
		t := Tag{Version: v, Commit: commit, Rel: Unrelated}
		switch {
		case commit == head:
			t.Rel = AtHead
		case merged[f[0]]:
			t.Rel = Ancestor
		case contains[f[0]]:
			t.Rel = Descendant
		}
		tags = append(tags, t)
	}
	return tags, nil
}

// log returns base..head (all parents, so merged branch commits are included),
// oldest first. base == nil means all of head's history.
func (g gitRepo) log(ctx context.Context, base *Tag, head string) ([]Commit, error) {
	rng := head
	if base != nil {
		rng = base.Commit + ".." + head
	}
	out, err := g.git(ctx, "log", "--reverse", "--format=%H%x00%P%x00%B%x1e", rng)
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, "\x00", 3)
		if len(f) != 3 {
			return nil, fmt.Errorf("release: unexpected git log record %q", rec)
		}
		commits = append(commits, Commit{SHA: f[0], Parents: len(strings.Fields(f[1])), Message: strings.TrimSpace(f[2])})
	}
	return commits, nil
}

// modulePath reads the module line of go.mod at head, used by Version.Next
// to refuse a v2 tag on a v1 module path.
func (g gitRepo) modulePath(ctx context.Context, head string) (string, error) {
	out, err := g.git(ctx, "show", head+":go.mod")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(p), `"`), nil
		}
	}
	return "", fmt.Errorf("release: go.mod at %s has no module line", short(head))
}

// checkout is proof that dir holds exactly commit with a clean work tree.
// Only verifyCheckout constructs it outside tests, and buildBundle requires
// it, so an archive can never be built from a tree other than the commit.
type checkout struct {
	dir        string
	commit     string
	commitTime time.Time // archive entry mtime, so archives are byte-reproducible
}

// verifyCheckout checks `git rev-parse HEAD` == commit and
// `git status --porcelain` is empty.
func (g gitRepo) verifyCheckout(ctx context.Context, commit string) (checkout, error) {
	head, err := g.resolve(ctx, "HEAD")
	if err != nil {
		return checkout{}, err
	}
	if head != commit {
		return checkout{}, fmt.Errorf("release: checkout is at %s, want %s", short(head), short(commit))
	}
	status, err := g.git(ctx, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return checkout{}, err
	}
	if strings.TrimSpace(status) != "" {
		return checkout{}, fmt.Errorf("release: work tree is not clean; archives are built only from a committed tree:\n%s", status)
	}
	ts, err := g.git(ctx, "show", "-s", "--format=%ct", commit)
	if err != nil {
		return checkout{}, err
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(ts), 10, 64)
	if err != nil {
		return checkout{}, err
	}
	return checkout{dir: g.dir, commit: commit, commitTime: time.Unix(sec, 0).UTC()}, nil
}
