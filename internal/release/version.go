// Package release derives, builds and publishes evalkit releases.
//
// The whole versioning policy lives in this file: a release version is a
// plain MAJOR.MINOR.PATCH derived from the newest release tag in history plus
// the Conventional Commits since it. Nothing else in the repository stores a
// version.
package release

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is a released evalkit version. evalkit never publishes
// pre-release or build-metadata versions, so they are unrepresentable.
// Comparable with ==.
type Version struct{ Major, Minor, Patch uint64 }

var tagPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// ParseTag parses a release tag "vMAJOR.MINOR.PATCH" (no leading zeros).
// Any other tag name is not a release tag and is ignored by planning.
func ParseTag(name string) (Version, bool) {
	m := tagPattern.FindStringSubmatch(name)
	if m == nil {
		return Version{}, false
	}
	var parts [3]uint64
	for i := range parts {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil {
			return Version{}, false
		}
		parts[i] = n
	}
	return Version{parts[0], parts[1], parts[2]}, true
}

// String is the bare form used by -ldflags main.version, the Homebrew
// formula's `version` and CHANGELOG headings: "0.5.1".
func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// Tag is the git tag, GitHub release name and archive-name form: "v0.5.1".
func (v Version) Tag() string { return "v" + v.String() }

// Less orders versions by semver precedence.
func (v Version) Less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

// Bump is the size of a release. Ordered: the bump of a set of changes is
// the maximum of their bumps.
type Bump uint8

const (
	NoBump Bump = iota
	PatchBump
	MinorBump
	MajorBump
)

func (b Bump) String() string {
	return [...]string{"none", "patch", "minor", "major"}[b]
}

// ErrNeedsModulePath is returned when a breaking change on 1.x would produce
// 2.0.0. Go requires the module path to end in /v2 before a v2 tag is usable,
// so the tool refuses rather than publishing a tag `go get` cannot resolve.
var ErrNeedsModulePath = errors.New("release: major version >= 2 requires a /vN module path in go.mod")

// Next returns the version after v for bump b.
//
// Rules (semantic-release's commit-analyzer defaults, plus two Go/semver
// constraints):
//
//	NoBump    -> v unchanged (callers do not release)
//	PatchBump -> x.y.(z+1)
//	MinorBump -> x.(y+1).0
//	MajorBump -> 0.y.z: 0.(y+1).0  (semver §4: 0.y.z promises nothing; 1.0.0 is
//	                               a deliberate declaration, never a footer side effect)
//	             1.y.z: ErrNeedsModulePath unless modulePath ends in "/v2"
func (v Version) Next(b Bump, modulePath string) (Version, error) {
	switch b {
	case NoBump:
		return v, nil
	case PatchBump:
		return Version{v.Major, v.Minor, v.Patch + 1}, nil
	case MinorBump:
		return Version{v.Major, v.Minor + 1, 0}, nil
	case MajorBump:
		if v.Major == 0 {
			return Version{0, v.Minor + 1, 0}, nil
		}
		if !strings.HasSuffix(modulePath, fmt.Sprintf("/v%d", v.Major+1)) {
			return Version{}, ErrNeedsModulePath
		}
		return Version{v.Major + 1, 0, 0}, nil
	}
	return Version{}, fmt.Errorf("release: unknown bump %d", b)
}

// Commit is one commit read from git history.
type Commit struct {
	SHA     string // full 40-hex SHA
	Parents int    // >1 means a merge commit; merges never carry a change themselves
	Message string // full message: subject, blank line, body and footers
}

func (c Commit) subject() string {
	s, _, _ := strings.Cut(c.Message, "\n")
	return strings.TrimSpace(s)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Change is a commit whose subject is a Conventional Commits header.
type Change struct {
	SHA          string
	Type         string // lower-cased: feat, fix, perf, revert, chore, docs, ci, ...
	Scope        string // "" when absent
	Description  string // text after ": "
	Breaking     bool   // "!" before ":" or a BREAKING CHANGE / BREAKING-CHANGE footer
	BreakingNote string // footer text, "" when Breaking came from "!" only
}

// Bump is the release size this change asks for:
// breaking -> MajorBump; feat -> MinorBump; fix, perf, revert -> PatchBump;
// every other type -> NoBump.
func (c Change) Bump() Bump {
	switch {
	case c.Breaking:
		return MajorBump
	case c.Type == "feat":
		return MinorBump
	case c.Type == "fix", c.Type == "perf", c.Type == "revert":
		return PatchBump
	}
	return NoBump
}

var revertSubject = regexp.MustCompile(`^Revert "(.+)"$`)

// classify splits history into Conventional Commits and the rest. Merge
// commits are dropped (their second-parent commits are in the range already).
// Ignored commits are reported in the plan, never silently counted.
func classify(commits []Commit) (changes []Change, ignored []Commit) {
	for _, c := range commits {
		if c.Parents > 1 {
			continue
		}
		subject := c.subject()
		var ch Change
		if m := revertSubject.FindStringSubmatch(subject); m != nil {
			// GitHub's revert button: `Revert "<original subject>"`.
			ch = Change{SHA: c.SHA, Type: "revert", Description: m[1]}
		} else {
			h, err := parseHeader(subject)
			if err != nil {
				ignored = append(ignored, c)
				continue
			}
			ch = Change{SHA: c.SHA, Type: h.Type, Scope: h.Scope, Description: h.Description, Breaking: h.Bang}
		}
		for _, line := range strings.Split(c.Message, "\n")[1:] {
			for _, key := range []string{"BREAKING CHANGE: ", "BREAKING-CHANGE: "} {
				if note, ok := strings.CutPrefix(line, key); ok {
					ch.Breaking, ch.BreakingNote = true, strings.TrimSpace(note)
				}
			}
		}
		changes = append(changes, ch)
	}
	return changes, ignored
}

// header is the parsed "type(scope)!: description" line.
type header struct {
	Type, Scope, Description string
	Bang                     bool
}

var headerPattern = regexp.MustCompile(`^([a-zA-Z]+)(?:\(([^()\s]+)\))?(!)?: (\S.*)$`)

// parseHeader is the single Conventional Commits grammar, shared by commit
// subjects and pull-request titles.
func parseHeader(s string) (header, error) {
	m := headerPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return header{}, fmt.Errorf("release: %q is not a Conventional Commit header (want \"type(scope): description\")", s)
	}
	return header{Type: strings.ToLower(m[1]), Scope: m[2], Bang: m[3] == "!", Description: strings.TrimSpace(m[4])}, nil
}

// CheckTitle reports whether a pull-request title would land as a
// Conventional Commit when squash-merged. Used by `release plan --pr-title`.
func CheckTitle(title string) error {
	_, err := parseHeader(title)
	return err
}
