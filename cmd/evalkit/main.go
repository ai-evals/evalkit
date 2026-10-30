package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ai-evals/evalkit/internal/engine"
)

var version = "dev" // overwritten by release -ldflags

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }

func run(args []string, stdout io.Writer) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintln(stdout, "evalkit", version)
		return 0
	}
	if len(args) > 0 && args[0] == "inspect" {
		return inspect(args[1:], stdout)
	}
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprintln(os.Stderr, "usage: evalkit run --config FILE --artifact-dir DIR [--timeout 10s] | inspect --artifact-dir DIR | version")
		return 2
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config := fs.String("config", "", "V1 configuration JSON file")
	artifactDir := fs.String("artifact-dir", "", "required durable artifact directory")
	timeout := fs.Duration("timeout", 10*time.Second, "run-wide deadline")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *config == "" || *artifactDir == "" || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "invalid run invocation")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	raw, err := os.ReadFile(*config)
	if err != nil {
		// This is an invocation failure before engine Run creation.
		fmt.Fprintln(os.Stderr, "cannot read configuration")
		return 2
	}
	outcome := engine.RunJSON(ctx, raw, *artifactDir)
	_ = json.NewEncoder(stdout).Encode(outcome)
	switch outcome.State {
	case "COMPLETED":
		switch *outcome.Quality {
		case "PASS":
			return 0
		case "FAIL":
			return 1
		default:
			return 3
		}
	default:
		return 2
	}
}

func inspect(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("artifact-dir", "", "existing artifact directory")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *dir == "" {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"schema_version": 1, "error": map[string]string{"code": "INVOCATION_INVALID", "scope": "INVOCATION"}})
		return 2
	}
	result, err := engine.InspectArtifact(context.Background(), *dir)
	if err != nil {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"schema_version": 1, "error": map[string]string{"code": "ARTIFACT_VERIFICATION_FAILED", "scope": "INSPECTION"}})
		return 2
	}
	_ = json.NewEncoder(stdout).Encode(result)
	return 0
}
