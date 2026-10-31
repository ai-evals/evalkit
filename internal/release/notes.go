package release

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// noteSections is the conventional-changelog layout semantic-release
// produces, in order. Only releasable types are listed; chore/docs/ci/test/
// refactor/style/build commits stay out of user-facing notes.
var noteSections = []struct {
	title string
	keep  func(Change) bool
}{
	{"⚠ BREAKING CHANGES", func(c Change) bool { return c.Breaking }},
	{"Features", func(c Change) bool { return c.Type == "feat" }},
	{"Bug Fixes", func(c Change) bool { return c.Type == "fix" }},
	{"Performance Improvements", func(c Change) bool { return c.Type == "perf" }},
	{"Reverts", func(c Change) bool { return c.Type == "revert" }},
}

// renderNotes is the release-notes body for a set of changes. Pure.
//
//	### Bug Fixes
//
//	- **inspect:** reject scorer contracts evalkit would never run (ea7f1f1)
//
// Short SHAs autolink in GitHub release bodies.
func renderNotes(changes []Change) string {
	var b strings.Builder
	for _, s := range noteSections {
		var lines []string
		for _, c := range changes {
			if !s.keep(c) {
				continue
			}
			text := c.Description
			if s.title == noteSections[0].title && c.BreakingNote != "" {
				text = c.BreakingNote
			}
			if c.Scope != "" {
				text = "**" + c.Scope + ":** " + text
			}
			lines = append(lines, fmt.Sprintf("- %s (%s)", text, short(c.SHA)))
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, "### %s\n\n%s\n\n", s.title, strings.Join(lines, "\n"))
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

var changelogHeading = regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\]`)

// changelogFile is CHANGELOG.md as it should be on main, given every
// published release. Pure and convergent: a section is inserted for each
// published release newer than the newest "## [X.Y.Z]" heading already in
// current (hand-written sections count, and are never rewritten), above the
// first heading, newest first. The section body is the release body on
// GitHub, not a re-render, so a late run after a failed sync writes exactly
// what users saw on the release.
func changelogFile(current []byte, published []Release) (next []byte, changed bool) {
	newest := Version{}
	haveNewest := false
	for _, m := range changelogHeading.FindAllSubmatch(current, -1) {
		if v, ok := ParseTag("v" + string(m[1])); ok && (!haveNewest || newest.Less(v)) {
			newest, haveNewest = v, true
		}
	}
	var fresh []Release
	for _, r := range published {
		if !r.Draft && (!haveNewest || newest.Less(r.Version)) {
			fresh = append(fresh, r)
		}
	}
	if len(fresh) == 0 {
		return current, false
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[j].Version.Less(fresh[i].Version) })
	var sections bytes.Buffer
	for _, r := range fresh {
		fmt.Fprintf(&sections, "## [%s] - %s\n\n", r.Version, r.PublishedAt.UTC().Format("2006-01-02"))
		if body := strings.TrimSpace(r.Body); body != "" {
			sections.WriteString(body + "\n\n")
		}
	}
	at := len(current)
	if loc := changelogHeading.FindIndex(current); loc != nil {
		at = loc[0]
	} else if at > 0 && current[at-1] != '\n' {
		sections = *bytes.NewBuffer(append([]byte("\n\n"), sections.Bytes()...))
	}
	next = append(append(append([]byte{}, current[:at]...), sections.Bytes()...), current[at:]...)
	return next, true
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
