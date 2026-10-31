package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Repo is "owner/name".
type Repo struct{ Owner, Name string }

// ParseRepo parses $GITHUB_REPOSITORY.
func ParseRepo(s string) (Repo, error) {
	owner, name, ok := strings.Cut(s, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return Repo{}, fmt.Errorf("release: repository %q is not owner/name", s)
	}
	return Repo{owner, name}, nil
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

// GitHub is the connection to the forge, built by the cmd from the Actions
// environment. Token comes from $GITHUB_TOKEN only: never a flag, never
// logged, never written to disk.
type GitHub struct {
	API   string // $GITHUB_API_URL, default https://api.github.com
	Repo  Repo
	Token string
}

// Release is the domain view of a GitHub release: only the fields the
// policy reads. The REST JSON shape stays private to restForge.
type Release struct {
	ID          int64
	Version     Version // from tag_name; non-semver releases are dropped at parse time
	Draft       bool
	Commit      string // target_commitish; a full SHA for drafts this tool creates (only drafts are compared)
	Body        string
	PublishedAt time.Time // zero for drafts
	Assets      []Asset
}

// Asset is one uploaded release file.
type Asset struct {
	ID     int64
	Name   string
	Digest Digest // GitHub's server-side SHA-256 of the upload
}

// Branch is a consistent read of one branch tip and the files the tool owns.
type Branch struct {
	Name      string
	Commit    string // tip; the compare-and-swap parent for CommitFiles
	Tree      string
	Formula   []byte // Formula/evalkit.rb at Commit (nil if absent)
	Changelog []byte // CHANGELOG.md at Commit
}

const (
	formulaPath   = "Formula/evalkit.rb"
	changelogPath = "CHANGELOG.md"
)

// errBranchMoved is returned by CommitFiles when the branch tip is no longer
// parent.Commit. Callers re-read and recompute; never force.
var errBranchMoved = errors.New("release: branch moved")

// Comparison is how a branch tip relates to a commit: the commits on the
// branch that the commit lacks, and whether the commit is on the branch at all.
type Comparison struct {
	OnBranch bool     // commit is the tip or an ancestor of it
	Ahead    []Commit // commits in commit..tip, oldest first
}

// forge is everything publish needs from GitHub, in domain terms. One
// production implementation (restForge) and one in-memory fake in tests
// that can fail after any call, which is how crash-at-every-step recovery is
// tested.
type forge interface {
	// Releases lists every release, drafts included (needs contents:write).
	Releases(ctx context.Context) ([]Release, error)
	// CreateDraft creates an unpublished release for t; no tag is created yet.
	CreateDraft(ctx context.Context, t Target) (Release, error)
	// UpdateDraft rewrites a draft's body (notes are re-rendered on resume).
	UpdateDraft(ctx context.Context, id int64, t Target) error
	DeleteRelease(ctx context.Context, id int64) error
	UploadAsset(ctx context.Context, releaseID int64, a Artifact) (Asset, error)
	DeleteAsset(ctx context.Context, id int64) error
	// Publish flips draft -> published with make_latest. GitHub creates the
	// tag at Release.Commit in the same operation, so tag and complete
	// release become visible together.
	Publish(ctx context.Context, id int64) (Release, error)
	// ReadBranch reads the tip of name and the two owned files at that tip.
	ReadBranch(ctx context.Context, name string) (Branch, error)
	// Compare reports how branch relates to commit (stale-run guard).
	Compare(ctx context.Context, commit, branch string) (Comparison, error)
	// CommitFiles creates one commit on parent.Name whose parent is
	// parent.Commit, replacing exactly files, then fast-forwards the branch
	// (force=false). files must be non-empty; the created tree must differ
	// from parent.Tree.
	CommitFiles(ctx context.Context, parent Branch, files map[string][]byte, message string) (string, error)
}

// restForge implements forge over the REST API with net/http only.
type restForge struct {
	gh   GitHub
	http *http.Client
}

func newRESTForge(gh GitHub) forge {
	if gh.API == "" {
		gh.API = "https://api.github.com"
	}
	gh.API = strings.TrimSuffix(gh.API, "/")
	return &restForge{gh: gh, http: &http.Client{Timeout: 5 * time.Minute}}
}

// apiError keeps the status so callers can branch on it; it never includes
// request headers, so the token cannot leak into logs.
type apiError struct {
	Method, Path string
	Status       int
	Message      string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("github: %s %s: %d %s", e.Method, e.Path, e.Status, e.Message)
}

func (f *restForge) repoPath(format string, args ...any) string {
	return "/repos/" + f.gh.Repo.String() + fmt.Sprintf(format, args...)
}

// do sends one request to an absolute URL or API path. in (if non-nil) is
// JSON-encoded unless it is an io.Reader; out (if non-nil) is JSON-decoded,
// or receives raw bytes when it is *[]byte.
func (f *restForge) do(ctx context.Context, method, target string, in, out any, header http.Header) error {
	u := target
	if strings.HasPrefix(target, "/") {
		u = f.gh.API + target
	}
	var body io.Reader
	switch v := in.(type) {
	case nil:
	case io.Reader:
		body = v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+f.gh.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	if r, ok := in.(*os.File); ok {
		if st, err := r.Stat(); err == nil {
			req.ContentLength = st.Size()
		}
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var msg struct{ Message string }
		_ = json.Unmarshal(data, &msg)
		path := u
		if p, err := url.Parse(u); err == nil {
			path = p.Path
		}
		return &apiError{Method: method, Path: path, Status: resp.StatusCode, Message: msg.Message}
	}
	switch v := out.(type) {
	case nil:
		return nil
	case *[]byte:
		*v = data
		return nil
	default:
		return json.Unmarshal(data, out)
	}
}

func isStatus(err error, status int) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == status
}

type restAsset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type restRelease struct {
	ID          int64       `json:"id"`
	TagName     string      `json:"tag_name"`
	Target      string      `json:"target_commitish"`
	Draft       bool        `json:"draft"`
	Body        string      `json:"body"`
	PublishedAt *time.Time  `json:"published_at"`
	UploadURL   string      `json:"upload_url"`
	Assets      []restAsset `json:"assets"`
}

func (r restRelease) domain() (Release, bool, error) {
	v, ok := ParseTag(r.TagName)
	if !ok {
		return Release{}, false, nil
	}
	rel := Release{ID: r.ID, Version: v, Draft: r.Draft, Commit: r.Target, Body: r.Body}
	if r.PublishedAt != nil {
		rel.PublishedAt = *r.PublishedAt
	}
	for _, a := range r.Assets {
		asset := Asset{ID: a.ID, Name: a.Name}
		if a.Digest != "" {
			d, err := parseAssetDigest(a.Digest)
			if err != nil {
				return Release{}, false, err
			}
			asset.Digest = d
		}
		rel.Assets = append(rel.Assets, asset)
	}
	return rel, true, nil
}

func (f *restForge) Releases(ctx context.Context) ([]Release, error) {
	var all []Release
	for page := 1; ; page++ {
		var batch []restRelease
		if err := f.do(ctx, http.MethodGet, f.repoPath("/releases?per_page=100&page=%d", page), nil, &batch, nil); err != nil {
			return nil, err
		}
		for _, r := range batch {
			rel, ok, err := r.domain()
			if err != nil {
				return nil, err
			}
			if ok {
				all = append(all, rel)
			}
		}
		if len(batch) < 100 {
			return all, nil
		}
	}
}

func (f *restForge) CreateDraft(ctx context.Context, t Target) (Release, error) {
	in := map[string]any{
		"tag_name": t.Version.Tag(), "target_commitish": t.Commit,
		"name": "evalkit " + t.Version.Tag(), "body": t.Notes, "draft": true,
	}
	var out restRelease
	if err := f.do(ctx, http.MethodPost, f.repoPath("/releases"), in, &out, nil); err != nil {
		return Release{}, err
	}
	rel, _, err := out.domain()
	return rel, err
}

func (f *restForge) UpdateDraft(ctx context.Context, id int64, t Target) error {
	in := map[string]any{"body": t.Notes, "target_commitish": t.Commit, "name": "evalkit " + t.Version.Tag()}
	return f.do(ctx, http.MethodPatch, f.repoPath("/releases/%d", id), in, nil, nil)
}

func (f *restForge) DeleteRelease(ctx context.Context, id int64) error {
	err := f.do(ctx, http.MethodDelete, f.repoPath("/releases/%d", id), nil, nil, nil)
	if isStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

func (f *restForge) uploadsBase() string {
	if f.gh.API == "https://api.github.com" {
		return "https://uploads.github.com"
	}
	// GitHub Enterprise Server: https://HOST/api/v3 -> https://HOST/api/uploads
	return strings.TrimSuffix(f.gh.API, "/v3") + "/uploads"
}

func (f *restForge) UploadAsset(ctx context.Context, releaseID int64, a Artifact) (Asset, error) {
	file, err := os.Open(a.Path)
	if err != nil {
		return Asset{}, err
	}
	defer file.Close()
	target := fmt.Sprintf("%s/repos/%s/releases/%d/assets?name=%s", f.uploadsBase(), f.gh.Repo, releaseID, url.QueryEscape(a.Name))
	var out restAsset
	h := http.Header{"Content-Type": {"application/octet-stream"}}
	if err := f.do(ctx, http.MethodPost, target, file, &out, h); err != nil {
		return Asset{}, err
	}
	asset := Asset{ID: out.ID, Name: out.Name}
	if out.Digest != "" {
		d, err := parseAssetDigest(out.Digest)
		if err != nil {
			return Asset{}, err
		}
		asset.Digest = d
	}
	return asset, nil
}

func (f *restForge) DeleteAsset(ctx context.Context, id int64) error {
	err := f.do(ctx, http.MethodDelete, f.repoPath("/releases/assets/%d", id), nil, nil, nil)
	if isStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

func (f *restForge) Publish(ctx context.Context, id int64) (Release, error) {
	var out restRelease
	in := map[string]any{"draft": false, "make_latest": "true"}
	if err := f.do(ctx, http.MethodPatch, f.repoPath("/releases/%d", id), in, &out, nil); err != nil {
		return Release{}, err
	}
	rel, _, err := out.domain()
	return rel, err
}

func (f *restForge) ReadBranch(ctx context.Context, name string) (Branch, error) {
	var ref struct {
		Object struct{ SHA string } `json:"object"`
	}
	if err := f.do(ctx, http.MethodGet, f.repoPath("/git/ref/heads/%s", name), nil, &ref, nil); err != nil {
		return Branch{}, err
	}
	var commit struct {
		Tree struct{ SHA string } `json:"tree"`
	}
	if err := f.do(ctx, http.MethodGet, f.repoPath("/git/commits/%s", ref.Object.SHA), nil, &commit, nil); err != nil {
		return Branch{}, err
	}
	br := Branch{Name: name, Commit: ref.Object.SHA, Tree: commit.Tree.SHA}
	raw := http.Header{"Accept": {"application/vnd.github.raw+json"}}
	for path, dst := range map[string]*[]byte{formulaPath: &br.Formula, changelogPath: &br.Changelog} {
		err := f.do(ctx, http.MethodGet, f.repoPath("/contents/%s?ref=%s", path, br.Commit), nil, dst, raw)
		if err != nil && !isStatus(err, http.StatusNotFound) {
			return Branch{}, err
		}
	}
	return br, nil
}

func (f *restForge) Compare(ctx context.Context, commit, branch string) (Comparison, error) {
	var out struct {
		Status  string `json:"status"`
		Commits []struct {
			SHA     string `json:"sha"`
			Parents []any  `json:"parents"`
			Commit  struct {
				Message string `json:"message"`
			} `json:"commit"`
		} `json:"commits"`
	}
	if err := f.do(ctx, http.MethodGet, f.repoPath("/compare/%s...%s", commit, branch), nil, &out, nil); err != nil {
		return Comparison{}, err
	}
	c := Comparison{OnBranch: out.Status == "identical" || out.Status == "ahead"}
	for _, x := range out.Commits {
		c.Ahead = append(c.Ahead, Commit{SHA: x.SHA, Parents: len(x.Parents), Message: x.Commit.Message})
	}
	return c, nil
}

func (f *restForge) CommitFiles(ctx context.Context, parent Branch, files map[string][]byte, message string) (string, error) {
	if len(files) == 0 {
		return "", errors.New("release: refusing to create a commit with no changed files")
	}
	var entries []map[string]string
	for _, path := range sortedKeys(files) {
		entries = append(entries, map[string]string{"path": path, "mode": "100644", "type": "blob", "content": string(files[path])})
	}
	var tree struct{ SHA string }
	if err := f.do(ctx, http.MethodPost, f.repoPath("/git/trees"), map[string]any{"base_tree": parent.Tree, "tree": entries}, &tree, nil); err != nil {
		return "", err
	}
	if tree.SHA == parent.Tree {
		return "", errors.New("release: refusing to create an empty commit (tree unchanged)")
	}
	var commit struct{ SHA string }
	if err := f.do(ctx, http.MethodPost, f.repoPath("/git/commits"), map[string]any{"message": message, "tree": tree.SHA, "parents": []string{parent.Commit}}, &commit, nil); err != nil {
		return "", err
	}
	err := f.do(ctx, http.MethodPatch, f.repoPath("/git/refs/heads/%s", parent.Name), map[string]any{"sha": commit.SHA, "force": false}, nil, nil)
	if isStatus(err, http.StatusUnprocessableEntity) {
		return "", errBranchMoved
	}
	return commit.SHA, err
}
