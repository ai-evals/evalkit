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
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprintln(os.Stderr, "usage: evalkit run --config FILE --artifact-dir DIR [--timeout 10s]")
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
