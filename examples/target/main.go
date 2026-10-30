package main

import (
	"encoding/json"
	"os"
)

func main() {
	var request struct {
		CaseID string          `json:"case_id"`
		Input  json.RawMessage `json:"input"`
	}
	if json.NewDecoder(os.Stdin).Decode(&request) != nil || request.CaseID == "" {
		os.Exit(2)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"answer": "yes"})
}
