// Command evalkit-example-scorer is a version 5 command scorer. It scores the
// target's answer by word overlap with the expected answer, as an F1 score.
package main

import (
	"encoding/json"
	"os"
	"strings"
	"unicode"
)

func main() {
	var request struct {
		Expected struct {
			Answer *string `json:"answer"`
		} `json:"expected"`
		Output struct {
			Answer *string `json:"answer"`
		} `json:"output"`
	}
	if json.NewDecoder(os.Stdin).Decode(&request) != nil {
		os.Exit(1)
	}
	if request.Expected.Answer == nil || request.Output.Answer == nil {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"unevaluable": "EVIDENCE_MISSING"})
		return
	}
	score := overlap(words(*request.Output.Answer), words(*request.Expected.Answer))
	_ = json.NewEncoder(os.Stdout).Encode(map[string]float64{"score": score})
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// overlap is the F1 of the shared words, counting each repeat once per match.
func overlap(got, want []string) float64 {
	if len(got) == 0 || len(want) == 0 {
		if len(got) == len(want) {
			return 1
		}
		return 0
	}
	remaining := map[string]int{}
	for _, w := range want {
		remaining[w]++
	}
	shared := 0
	for _, w := range got {
		if remaining[w] > 0 {
			remaining[w]--
			shared++
		}
	}
	if shared == 0 {
		return 0
	}
	precision := float64(shared) / float64(len(got))
	recall := float64(shared) / float64(len(want))
	return 2 * precision * recall / (precision + recall)
}
