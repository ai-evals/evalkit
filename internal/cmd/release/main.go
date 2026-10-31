// Command release plans and publishes evalkit releases. It is internal: not
// installable with `go install`, run from a checkout with `go run`.
//
//	go run ./internal/cmd/release plan [--pr-title TITLE] [--build DIR]
//	go run ./internal/cmd/release publish
//
// plan is read-only and needs only git (and the Go toolchain with --build,
// which cross-builds the release archives into DIR without publishing).
// publish needs GITHUB_TOKEN, GITHUB_REPOSITORY and GITHUB_SHA and is meant
// for the CI `release` job. Both append their Markdown to
// $GITHUB_STEP_SUMMARY when it is set.
//
// Exit codes: 0 ok (including "nothing to release"), 1 the plan or publish
// failed, 2 invalid invocation.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ai-evals/evalkit/internal/release"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

const usage = "usage: release plan [--pr-title TITLE] [--build DIR] | release publish"

// run is the whole shell: parse args and env into release types, call one
// release function, print its Markdown. No policy lives here.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	emit := func(md string) {
		fmt.Fprint(stdout, md)
		if path := getenv("GITHUB_STEP_SUMMARY"); path != "" {
			if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				fmt.Fprint(f, md)
				f.Close()
			}
		}
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "release:", err)
		return 1
	}
	switch args[0] {
	case "plan":
		fs := flag.NewFlagSet("plan", flag.ContinueOnError)
		fs.SetOutput(stderr)
		title := fs.String("pr-title", "", "fail unless this pull-request title is a Conventional Commit header")
		buildDir := fs.String("build", "", "also cross-build the release archives into this directory")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		plan, err := release.PlanRelease(ctx, release.PlanRequest{Dir: ".", Head: "HEAD"})
		if err != nil {
			return fail(err)
		}
		emit(plan.Markdown())
		var errs []error
		if *title != "" {
			errs = append(errs, release.CheckTitle(*title))
		}
		if *buildDir != "" {
			b, err := release.Build(ctx, ".", plan, *buildDir)
			if err == nil {
				md := fmt.Sprintf("\n## Dry-run build (%s)\n\n", b.Version.Tag())
				for _, a := range b.Assets() {
					md += fmt.Sprintf("- `%s` %s\n", a.Name, a.Digest)
				}
				emit(md)
			}
			errs = append(errs, err)
		}
		if err := errors.Join(errs...); err != nil {
			return fail(err)
		}
		return 0
	case "publish":
		if len(args) != 1 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		repo, err := release.ParseRepo(getenv("GITHUB_REPOSITORY"))
		if err != nil {
			return fail(err)
		}
		token, head := getenv("GITHUB_TOKEN"), getenv("GITHUB_SHA")
		if token == "" || head == "" {
			return fail(errors.New("GITHUB_TOKEN and GITHUB_SHA are required"))
		}
		outDir := getenv("RUNNER_TEMP")
		if outDir == "" {
			outDir = os.TempDir()
		}
		api := getenv("GITHUB_API_URL")
		if api == "" {
			api = "https://api.github.com"
		}
		rep, err := release.Publish(ctx, release.PublishRequest{
			Dir: ".", Head: head, OutDir: outDir + "/evalkit-dist",
			GitHub: release.GitHub{API: api, Repo: repo, Token: token},
		})
		emit(rep.Markdown()) // also on error: the partial report says how far it got
		if err != nil {
			return fail(err)
		}
		return 0
	}
	fmt.Fprintln(stderr, usage)
	return 2
}
