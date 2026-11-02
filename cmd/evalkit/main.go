package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ai-evals/evalkit/internal/engine"
)

var version = "dev" // overwritten by release -ldflags

// exitCodes is the CLI's one verdict-to-exit mapping. run exits through its
// own quality's recommendation and compare through the candidate's, so an
// artifact compared with itself exits like the run that made it.
var exitCodes = [...]int{
	engine.RecommendShip:        0,
	engine.RecommendIterate:     1,
	engine.RecommendInvestigate: 3,
}

// exitNoVerdict covers invocation errors, framework failures, cancellation
// and read refusals.
const exitNoVerdict = 2

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }

func run(args []string, stdout io.Writer) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintln(stdout, "evalkit", version)
		return 0
	}
	if len(args) > 0 && args[0] == "inspect" {
		return inspect(args[1:], stdout)
	}
	if len(args) > 0 && args[0] == "compare" {
		return compare(args[1:], stdout)
	}
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprintln(os.Stderr, "usage: evalkit run --config FILE --artifact-dir DIR [--timeout 10s] | inspect --artifact-dir DIR | compare --baseline DIR --candidate DIR | version")
		return exitNoVerdict
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config := fs.String("config", "", "V1 configuration JSON file")
	artifactDir := fs.String("artifact-dir", "", "required durable artifact directory")
	timeout := fs.Duration("timeout", 10*time.Second, "run-wide deadline")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *config == "" || *artifactDir == "" || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "invalid run invocation")
		return exitNoVerdict
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	raw, err := os.ReadFile(*config)
	if err != nil {
		// This is an invocation failure before engine Run creation.
		fmt.Fprintln(os.Stderr, "cannot read configuration")
		return exitNoVerdict
	}
	outcome := engine.RunJSON(ctx, raw, *artifactDir)
	_ = json.NewEncoder(stdout).Encode(outcome)
	quality, ok := outcome.CompletedQuality()
	if !ok {
		return exitNoVerdict
	}
	return exitCodes[quality.Recommendation()]
}

var invocationInvalid = &engine.ReadError{Code: "INVOCATION_INVALID", Scope: "INVOCATION"}

func inspect(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("artifact-dir", "", "existing artifact directory")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *dir == "" {
		return refuse(stdout, invocationInvalid)
	}
	result, err := engine.InspectArtifact(context.Background(), *dir)
	if err != nil {
		return refuse(stdout, err)
	}
	_ = json.NewEncoder(stdout).Encode(result)
	return 0
}

func compare(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	baseline := fs.String("baseline", "", "published artifact directory of the reference run")
	candidate := fs.String("candidate", "", "published artifact directory of the run being decided")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *baseline == "" || *candidate == "" {
		return refuse(stdout, invocationInvalid)
	}
	result, err := engine.CompareArtifacts(context.Background(), *baseline, *candidate)
	if err != nil {
		return refuse(stdout, err)
	}
	_ = json.NewEncoder(stdout).Encode(result)
	return exitCodes[result.Recommendation]
}

// refuse prints the V5 refusal envelope. The map keeps V5's sorted key order.
func refuse(stdout io.Writer, err error) int {
	var refused *engine.ReadError
	if !errors.As(err, &refused) {
		// With context.Background() the read commands return only *ReadError.
		fmt.Fprintln(os.Stderr, "evalkit:", err)
		return exitNoVerdict
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{"schema_version": 1, "error": refused})
	return exitNoVerdict
}
