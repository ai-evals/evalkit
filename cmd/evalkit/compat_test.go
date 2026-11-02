package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The corpus and golden.json were written by the v0.6.0 binary
// (testdata/v0.6.0/generate.sh), so they are what v0.6.0 printed, not what
// this tree prints.
func TestV7AC06V060ArtifactsReadByteIdentically(t *testing.T) {
	root := filepath.Join("testdata", "v0.6.0")
	raw, err := os.ReadFile(filepath.Join(root, "golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden []struct {
		Args   []string `json:"args"`
		Stdout string   `json:"stdout"`
		Exit   int      `json:"exit"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil || len(golden) == 0 {
		t.Fatalf("golden.json: %v", err)
	}
	for _, g := range golden {
		args := append([]string(nil), g.Args...)
		for i, a := range args {
			if strings.HasPrefix(a, "artifacts/") {
				args[i] = filepath.Join(root, a)
			}
		}
		var stdout bytes.Buffer
		if exit := run(args, &stdout); exit != g.Exit || stdout.String() != g.Stdout {
			t.Errorf("%v\n got exit %d: %s\nwant exit %d: %s", g.Args, exit, stdout.String(), g.Exit, g.Stdout)
		}
	}
}
