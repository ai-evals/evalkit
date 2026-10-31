package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/template"
)

// Platform is one OS/arch evalkit ships for.
type Platform struct{ OS, Arch string }

// platforms is the single list of shipped targets. The build iterates it,
// checksumsOf requires a digest for each entry, and a test asserts the rendered
// formula contains a url for each entry, so the build, the release assets and
// the formula cannot disagree.
var platforms = []Platform{
	{"darwin", "arm64"},
	{"darwin", "amd64"},
	{"linux", "arm64"},
	{"linux", "amd64"},
}

// archiveName keeps today's asset names so existing formula URLs and any
// user scripts keep working: evalkit_v0.5.1_linux_amd64.tar.gz.
func archiveName(v Version, p Platform) string {
	return fmt.Sprintf("evalkit_%s_%s_%s.tar.gz", v.Tag(), p.OS, p.Arch)
}

const sumsName = "SHA256SUMS"

// Digest is a SHA-256. Rendering it can only produce 64 hex characters, so
// the formula template needs no escaping and no `ruby -c` check.
type Digest [32]byte

func (d Digest) String() string { return hex.EncodeToString(d[:]) }

// parseAssetDigest parses GitHub's release-asset `digest` field, "sha256:<hex>".
func parseAssetDigest(s string) (Digest, error) {
	var d Digest
	h, ok := strings.CutPrefix(s, "sha256:")
	if !ok {
		return d, fmt.Errorf("release: asset digest %q is not sha256", s)
	}
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != len(d) {
		return d, fmt.Errorf("release: malformed asset digest %q", s)
	}
	copy(d[:], b)
	return d, nil
}

// Artifact is one file to attach to a release.
type Artifact struct {
	Name   string // asset name
	Path   string // local file
	Digest Digest
}

// Bundle is the complete asset set of one release: one archive per platform
// plus SHA256SUMS over them. Complete by construction (newBundle is the
// only constructor).
type Bundle struct {
	Version  Version
	Archives map[Platform]Artifact
	Sums     Artifact
}

// Assets lists every artifact in upload order: archives, then SHA256SUMS.
func (b Bundle) Assets() []Artifact {
	out := make([]Artifact, 0, len(platforms)+1)
	for _, p := range platforms {
		out = append(out, b.Archives[p])
	}
	return append(out, b.Sums)
}

// newBundle writes SHA256SUMS over archives (one per platform, required) into
// outDir and returns the complete bundle.
func newBundle(v Version, archives map[Platform]Artifact, outDir string) (Bundle, error) {
	var lines []string
	for _, p := range platforms {
		a, ok := archives[p]
		if !ok {
			return Bundle{}, fmt.Errorf("release: no archive for %s/%s", p.OS, p.Arch)
		}
		lines = append(lines, a.Digest.String()+"  "+a.Name)
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i][66:] < lines[j][66:] })
	sums, err := writeArtifact(outDir, sumsName, []byte(strings.Join(lines, "\n")+"\n"))
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{Version: v, Archives: archives, Sums: sums}, nil
}

func writeArtifact(dir, name string, data []byte) (Artifact, error) {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return Artifact{}, err
	}
	return Artifact{Name: name, Path: path, Digest: sha256.Sum256(data)}, nil
}

// buildBundle cross-compiles every platform from a verified checkout and
// writes reproducible archives into outDir. Same toolchain (go.mod toolchain
// line) + same commit => same bytes, so a re-run's digests match an earlier
// attempt's uploads and nothing re-uploads.
func buildBundle(ctx context.Context, co checkout, v Version, outDir string) (Bundle, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return Bundle{}, err
	}
	work, err := os.MkdirTemp("", "evalkit-release-")
	if err != nil {
		return Bundle{}, err
	}
	defer os.RemoveAll(work)
	archives := map[Platform]Artifact{}
	for _, p := range platforms {
		bin := filepath.Join(work, p.OS+"_"+p.Arch, "evalkit")
		cmd := exec.CommandContext(ctx, "go", "build", "-trimpath",
			"-ldflags", "-s -w -X main.version="+v.String(), "-o", bin, "./cmd/evalkit")
		cmd.Dir = co.dir
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+p.OS, "GOARCH="+p.Arch)
		if out, err := cmd.CombinedOutput(); err != nil {
			return Bundle{}, fmt.Errorf("release: build %s/%s: %w\n%s", p.OS, p.Arch, err, out)
		}
		if p.OS == runtime.GOOS && p.Arch == runtime.GOARCH {
			// The same assertion the formula's `test do` makes after install.
			out, err := exec.CommandContext(ctx, bin, "version").Output()
			if want := "evalkit " + v.String() + "\n"; err != nil || string(out) != want {
				return Bundle{}, fmt.Errorf("release: %s version printed %q (err %v), want %q", bin, out, err, want)
			}
		}
		data, err := tarGz(bin, co)
		if err != nil {
			return Bundle{}, err
		}
		a, err := writeArtifact(outDir, archiveName(v, p), data)
		if err != nil {
			return Bundle{}, err
		}
		archives[p] = a
	}
	return newBundle(v, archives, outDir)
}

// tarGz packs bin as the single entry "evalkit" with fixed metadata: mode
// 0755, uid/gid 0, no owner names, mtime = commit time, and a gzip header
// with no name and zero mtime.
func tarGz(bin string, co checkout) ([]byte, error) {
	data, err := os.ReadFile(bin)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(zw)
	hdr := &tar.Header{Name: "evalkit", Mode: 0o755, Size: int64(len(data)), ModTime: co.commitTime, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	if _, err := tw.Write(data); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Checksums is the per-platform digest set of a published release. Complete
// by construction.
type Checksums struct {
	Version Version
	ByOS    map[Platform]Digest
}

// checksumsOf reads digests from the release's asset metadata (GitHub
// computes `digest` server-side on upload). No asset download and no second
// parser for SHA256SUMS: the published release is the only source.
// Error if any platform's archive is missing or has no digest.
func checksumsOf(r Release) (Checksums, error) {
	byName := map[string]Digest{}
	for _, a := range r.Assets {
		byName[a.Name] = a.Digest
	}
	c := Checksums{Version: r.Version, ByOS: map[Platform]Digest{}}
	for _, p := range platforms {
		d, ok := byName[archiveName(r.Version, p)]
		if !ok || d == (Digest{}) {
			return Checksums{}, fmt.Errorf("release: %s has no digest for %s", r.Version.Tag(), archiveName(r.Version, p))
		}
		c.ByOS[p] = d
	}
	return c, nil
}

//go:embed formula.rb.tmpl
var formulaTemplate string

var formulaTmpl = template.Must(template.New("formula").Option("missingkey=error").Parse(formulaTemplate))

// formulaData is what formula.rb.tmpl sees.
type formulaData struct {
	Repo    Repo
	Version Version
	sums    Checksums
}

func (d formulaData) URL(os, arch string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", d.Repo, d.Version.Tag(), archiveName(d.Version, Platform{os, arch}))
}

func (d formulaData) SHA(os, arch string) (string, error) {
	dg, ok := d.sums.ByOS[Platform{os, arch}]
	if !ok {
		return "", fmt.Errorf("release: no checksum for %s/%s", os, arch)
	}
	return dg.String(), nil
}

// renderFormula is Formula/evalkit.rb for one published release. Pure; the
// replacement for scripts/render_formula.py. Golden-tested against
// testdata/evalkit-0.5.0.rb built from v0.5.0's real digests.
func renderFormula(repo Repo, sums Checksums) ([]byte, error) {
	var buf bytes.Buffer
	if err := formulaTmpl.Execute(&buf, formulaData{Repo: repo, Version: sums.Version, sums: sums}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
