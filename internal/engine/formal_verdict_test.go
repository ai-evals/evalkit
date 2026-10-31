//go:build formalverify

// Bounded-exhaustive verification of the evalkit quality verdict.
// Run with: .claude/skills/formal-verify-verdict/run.sh
// The skill at .claude/skills/formal-verify-verdict/SKILL.md explains the
// properties, bounds, coverage record and how to read a failure.
//
// The oracles below are written from the README contract ("required scorers
// determine required coverage"; missing coverage is INDETERMINATE), never by
// calling the code under test.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// ---- bounded domain -------------------------------------------------------

// Custom scorer behaviours. Every outcome evaluateCase can see from a custom
// scorer is represented: valid scores below/at/above every threshold, a valid
// UNEVALUABLE, an invalid candidate, a permanent error, a transient error that
// recovers on the second attempt, and a transient error that never recovers.
var customBehaviours = []string{"scored0", "scored0.5", "scored1", "uneval", "invalid", "perm", "transOK", "transForever"}

// exact_match behaviours: target output equals / differs from expected.
var exactBehaviours = []string{"match", "mismatch"}

var thresholds = []float64{0.5, 1}                                       // with scores {0,0.5,1}: below, equal, above
var requiredSets = [][2]bool{{true, false}, {false, true}, {true, true}} // config requires >=1
var attemptBudgets = []int{1, 2}

// The target is /bin/sh echoing the request line, so exact_match "match"
// cases expect the request itself. Case IDs starting with "tfail" exit 1.
const targetScript = `IFS= read -r line; case "$line" in *'"case_id":"tfail'*) exit 3;; esac; printf '%s\n' "$line"`

type runShape struct {
	kinds    [2]string // "custom" | "exact_match"
	required [2]bool
	thr      [2]float64
	attempts int
}

type caseSpec struct {
	id    string
	beh   [2]string
	tfail bool
	dup   bool
}

func (s runShape) String() string {
	return fmt.Sprintf("kinds=%v required=%v thresholds=%v scorer_max_attempts=%d", s.kinds, s.required, s.thr, s.attempts)
}

func behavioursFor(kind string) []string {
	if kind == "custom" {
		return customBehaviours
	}
	return exactBehaviours
}

func allShapes() []runShape {
	var out []runShape
	for _, k0 := range []string{"custom", "exact_match"} {
		for _, k1 := range []string{"custom", "exact_match"} {
			for _, req := range requiredSets {
				for _, t0 := range thresholds {
					for _, t1 := range thresholds {
						for _, a := range attemptBudgets {
							out = append(out, runShape{[2]string{k0, k1}, req, [2]float64{t0, t1}, a})
						}
					}
				}
			}
		}
	}
	return out
}

// ---- oracle ---------------------------------------------------------------

type obs struct {
	status   string
	score    *float64
	attempts int
}

func f(v float64) *float64 { return &v }

func oracleScorer(kind, beh string, attempts int) obs {
	if kind == "exact_match" {
		if beh == "match" {
			return obs{"SUCCEEDED", f(1), 1}
		}
		return obs{"SUCCEEDED", f(0), 1}
	}
	switch beh {
	case "scored0":
		return obs{"SUCCEEDED", f(0), 1}
	case "scored0.5":
		return obs{"SUCCEEDED", f(0.5), 1}
	case "scored1":
		return obs{"SUCCEEDED", f(1), 1}
	case "uneval":
		return obs{"UNEVALUABLE", nil, 1}
	case "invalid", "perm":
		return obs{"ERRORED", nil, 1}
	case "transOK":
		if attempts >= 2 {
			return obs{"SUCCEEDED", f(1), 2}
		}
		return obs{"ERRORED", nil, 1}
	case "transForever":
		return obs{"ERRORED", nil, attempts}
	}
	panic("unknown behaviour " + beh)
}

// oracleCase returns the expected case state and per-scorer observations.
func oracleCase(s runShape, c caseSpec) (string, [2]obs) {
	var o [2]obs
	if c.dup {
		return "INVALID", [2]obs{{status: "SKIPPED"}, {status: "SKIPPED"}}
	}
	if c.tfail {
		return "ERRORED", [2]obs{{status: "ERRORED"}, {status: "ERRORED"}}
	}
	state := "SCORED"
	for j := 0; j < 2; j++ {
		o[j] = oracleScorer(s.kinds[j], c.beh[j], s.attempts)
		if s.required[j] && o[j].status != "SUCCEEDED" {
			state = "ERRORED"
		}
	}
	return state, o
}

// oracleVerdict: PASS iff every case has a score from every required scorer
// and each meets its threshold; FAIL iff coverage is complete but some
// required score is below threshold; INDETERMINATE when coverage is missing.
func oracleVerdict(s runShape, cases []caseSpec) string {
	covered, below := true, false
	for _, c := range cases {
		state, o := oracleCase(s, c)
		if state != "SCORED" {
			covered = false
			continue
		}
		for j := 0; j < 2; j++ {
			if !s.required[j] {
				continue
			}
			if o[j].score == nil {
				covered = false
			} else if *o[j].score < s.thr[j] {
				below = true
			}
		}
	}
	switch {
	case !covered:
		return "INDETERMINATE"
	case below:
		return "FAIL"
	default:
		return "PASS"
	}
}

// ---- driving the real engine ---------------------------------------------

type callCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *callCounter) inc(k string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n[k]++
	return c.n[k]
}
func (c *callCounter) get(k string) int { c.mu.Lock(); defer c.mu.Unlock(); return c.n[k] }

var errPermanent = errors.New("permanent scorer failure")

func scorerID(j int) string { return fmt.Sprintf("s%d", j) }

func buildRun(t *testing.T, s runShape, cases []caseSpec, concurrency int) ([]byte, map[string]RegisteredScorer, *callCounter) {
	t.Helper()
	byID := map[string]caseSpec{}
	var cfgCases []map[string]any
	for _, c := range cases {
		byID[c.id] = c
		input := map[string]any{"n": 1}
		expected := any(map[string]any{"other": true})
		for j := 0; j < 2; j++ {
			if s.kinds[j] == "exact_match" && c.beh[j] == "match" {
				expected = map[string]any{"case_id": c.id, "input": input}
			}
		}
		cfgCases = append(cfgCases, map[string]any{"id": c.id, "input": input, "expected": expected})
	}
	counter := &callCounter{n: map[string]int{}}
	custom := map[string]RegisteredScorer{}
	var scorers []map[string]any
	for j := 0; j < 2; j++ {
		sc := map[string]any{"id": scorerID(j), "kind": s.kinds[j], "threshold": s.thr[j], "required": s.required[j]}
		if s.kinds[j] == "custom" {
			j := j
			sc["implementation"] = "formal/" + scorerID(j)
			custom[scorerID(j)] = RegisteredScorer{Implementation: "formal/" + scorerID(j), Evaluate: func(_ context.Context, in ScorerInput) (AssessmentCandidate, error) {
				n := counter.inc(in.CaseID + "/" + in.ScorerID)
				switch byID[in.CaseID].beh[j] {
				case "scored0":
					return AssessmentCandidate{Kind: "SCORED", Score: f(0)}, nil
				case "scored0.5":
					return AssessmentCandidate{Kind: "SCORED", Score: f(0.5)}, nil
				case "scored1":
					return AssessmentCandidate{Kind: "SCORED", Score: f(1)}, nil
				case "uneval":
					return AssessmentCandidate{Kind: "UNEVALUABLE", Reason: "EVIDENCE_MISSING"}, nil
				case "invalid":
					return AssessmentCandidate{Kind: "SCORED", Score: f(1.5)}, nil
				case "perm":
					return AssessmentCandidate{}, errPermanent
				case "transOK":
					if n == 1 {
						return AssessmentCandidate{}, &RetryableScorerError{Err: errors.New("blip")}
					}
					return AssessmentCandidate{Kind: "SCORED", Score: f(1)}, nil
				case "transForever":
					return AssessmentCandidate{}, &RetryableScorerError{Err: errors.New("blip")}
				}
				return AssessmentCandidate{}, fmt.Errorf("behaviour %q not valid for custom scorer", byID[in.CaseID].beh[j])
			}}
		}
		scorers = append(scorers, sc)
	}
	raw, err := json.Marshal(map[string]any{
		"version":   4,
		"cases":     cfgCases,
		"target":    map[string]any{"command": "/bin/sh", "args": []string{"-c", targetScript}},
		"scorers":   scorers,
		"execution": map[string]any{"concurrency": concurrency, "scorer_max_attempts": s.attempts},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw, custom, counter
}

func scoreStr(p *float64) string {
	if p == nil {
		return "none"
	}
	return fmt.Sprint(*p)
}

// checkRun drives one real run (RunJSONWithScorers -> runCases -> publish),
// then reads the artifact back with VerifyArtifact, and compares both to the
// oracle. It returns a list of human-readable violations.
func checkRun(t *testing.T, s runShape, cases []caseSpec, concurrency int) []string {
	raw, custom, counter := buildRun(t, s, cases, concurrency)
	dir := filepath.Join(t.TempDir(), "artifact")
	o := RunJSONWithScorers(context.Background(), raw, dir, custom)
	desc := describeCases(cases)
	if len(cases) > 4 {
		desc = fmt.Sprintf("all %d behaviour pairs + tfail-0 + duplicate pair", len(cases)-3)
	}
	where := fmt.Sprintf("[%s concurrency=%d cases=%s]", s, concurrency, desc)
	var bad []string
	if o.State != "COMPLETED" || o.Quality == nil {
		return []string{fmt.Sprintf("%s run did not complete: state=%s diagnostics=%v", where, o.State, o.Diagnostics)}
	}
	want := oracleVerdict(s, cases)
	if *o.Quality != want {
		bad = append(bad, fmt.Sprintf("%s VERDICT: engine said %s, contract says %s", where, *o.Quality, want))
	}
	if len(o.Cases) != len(cases) {
		return append(bad, fmt.Sprintf("%s case count %d != %d", where, len(o.Cases), len(cases)))
	}
	for i, c := range cases {
		got := o.Cases[i]
		wantState, wantObs := oracleCase(s, c)
		if got.ID != c.id {
			bad = append(bad, fmt.Sprintf("%s case %d: ID %q in slot of %q (dataset order lost)", where, i, got.ID, c.id))
			continue
		}
		if got.State != wantState {
			bad = append(bad, fmt.Sprintf("%s case %s: state %s, contract says %s (scorers %v)", where, c.id, got.State, wantState, c.beh))
		}
		for j := 0; j < 2; j++ {
			g := got.Scorers[j]
			w := wantObs[j]
			if g.Status != w.status || scoreStr(g.Score) != scoreStr(w.score) || g.Attempts != w.attempts {
				bad = append(bad, fmt.Sprintf("%s case %s scorer %s (%s %s): got %s/%s/attempts=%d, want %s/%s/attempts=%d",
					where, c.id, scorerID(j), s.kinds[j], c.beh[j], g.Status, scoreStr(g.Score), g.Attempts, w.status, scoreStr(w.score), w.attempts))
			}
			if s.kinds[j] == "custom" {
				calls := counter.get(c.id + "/" + scorerID(j))
				if calls != w.attempts || calls > s.attempts {
					bad = append(bad, fmt.Sprintf("%s case %s scorer %s: scorer executed %d times, recorded %d, budget %d", where, c.id, scorerID(j), calls, g.Attempts, s.attempts))
				}
			}
		}
	}
	v, err := VerifyArtifact(dir)
	if err != nil {
		bad = append(bad, fmt.Sprintf("%s published artifact rejected by VerifyArtifact: %v", where, err))
	} else if v.Quality == nil || *v.Quality != *o.Quality {
		bad = append(bad, fmt.Sprintf("%s VerifyArtifact verdict %v != reported %s", where, v.Quality, *o.Quality))
	}
	return bad
}

func describeCases(cs []caseSpec) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		switch {
		case c.dup:
			parts = append(parts, c.id+"(duplicate id)")
		case c.tfail:
			parts = append(parts, c.id+"(target fails)")
		default:
			parts = append(parts, fmt.Sprintf("%s(%s,%s)", c.id, c.beh[0], c.beh[1]))
		}
	}
	return strings.Join(parts, " ")
}

func pairCases(s runShape) []caseSpec {
	var out []caseSpec
	for _, b0 := range behavioursFor(s.kinds[0]) {
		for _, b1 := range behavioursFor(s.kinds[1]) {
			// Two exact_match scorers see the same actual/expected pair, so
			// they always agree; a split pair is not a reachable input.
			if s.kinds[0] == "exact_match" && s.kinds[1] == "exact_match" && b0 != b1 {
				continue
			}
			out = append(out, caseSpec{id: fmt.Sprintf("k%d", len(out)), beh: [2]string{b0, b1}})
		}
	}
	return out
}

func report(t *testing.T, bad []string) {
	t.Helper()
	sort.Strings(bad)
	if len(bad) == 0 {
		return
	}
	limit := 15
	if v := os.Getenv("FORMAL_MAX_REPORT"); v != "" {
		fmt.Sscan(v, &limit)
	}
	for i, b := range bad {
		if i == limit {
			t.Errorf("... and %d more violations", len(bad)-limit)
			break
		}
		t.Error(b)
	}
}

func parallelRuns(t *testing.T, jobs []func() []string) []string {
	var mu sync.Mutex
	var bad []string
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(job func() []string) {
			defer wg.Done()
			defer func() { <-sem }()
			b := job()
			mu.Lock()
			bad = append(bad, b...)
			mu.Unlock()
		}(job)
	}
	wg.Wait()
	return bad
}

// ---- P1: per-case verdict through the real pipeline ------------------------

// Every single-case run over every scorer-behaviour pair, scorer kind pair,
// required set, threshold pair and attempt budget in the bounded domain.
func TestFormalP1SingleCaseVerdict(t *testing.T) {
	var jobs []func() []string
	for _, s := range allShapes() {
		for _, c := range pairCases(s) {
			s, c := s, c
			jobs = append(jobs, func() []string { return checkRun(t, s, []caseSpec{c}, 1) })
		}
	}
	report(t, parallelRuns(t, jobs))
	t.Logf("checked %d single-case runs", len(jobs))
}

// ---- P2: case mapping and order under the bounded worker pool --------------

// One run per shape containing every behaviour pair plus a failing target and
// a duplicate-ID pair, at concurrency 1 and 16.
func TestFormalP2MultiCaseMapping(t *testing.T) {
	var jobs []func() []string
	for _, s := range allShapes() {
		cases := pairCases(s)
		cases = append(cases, caseSpec{id: "tfail-0", tfail: true}, caseSpec{id: "dup", dup: true, beh: [2]string{"x", "x"}}, caseSpec{id: "dup", dup: true, beh: [2]string{"x", "x"}})
		for _, conc := range []int{1, 16} {
			s, cases, conc := s, cases, conc
			jobs = append(jobs, func() []string { return checkRun(t, s, cases, conc) })
		}
	}
	report(t, parallelRuns(t, jobs))
	t.Logf("checked %d multi-case runs", len(jobs))
}

// ---- P3: aggregation over cases (pure, exhaustive) -------------------------

type absObs struct {
	status string
	score  *float64
}

var absObsDomain = []absObs{{"SUCCEEDED", f(0)}, {"SUCCEEDED", f(0.5)}, {"SUCCEEDED", f(1)}, {"SUCCEEDED", nil}, {"ERRORED", nil}, {"UNEVALUABLE", nil}, {"SKIPPED", nil}}
var absStates = []string{"SCORED", "ERRORED", "INVALID"}

func absOracle(results []V2CaseResult, scorers []V2Scorer) string {
	covered, below := true, false
	for _, c := range results {
		if c.State != "SCORED" {
			covered = false
		}
		for j, sc := range scorers {
			if !sc.Required {
				continue
			}
			o := c.Scorers[j]
			if o.Status != "SUCCEEDED" || o.Score == nil {
				covered = false
			} else if *o.Score < sc.Threshold {
				below = true
			}
		}
	}
	if !covered {
		return "INDETERMINATE"
	}
	if below {
		return "FAIL"
	}
	return "PASS"
}

func enumerate(nCases, nScorers int, visit func([]V2CaseResult)) {
	per := len(absStates)
	for i := 0; i < nScorers; i++ {
		per *= len(absObsDomain)
	}
	total := 1
	for i := 0; i < nCases; i++ {
		total *= per
	}
	results := make([]V2CaseResult, nCases)
	for i := range results {
		results[i].Scorers = make([]ScorerObservation, nScorers)
	}
	for code := 0; code < total; code++ {
		x := code
		for i := 0; i < nCases; i++ {
			results[i].ID = fmt.Sprintf("c%d", i)
			results[i].State = absStates[x%len(absStates)]
			x /= len(absStates)
			for j := 0; j < nScorers; j++ {
				o := absObsDomain[x%len(absObsDomain)]
				x /= len(absObsDomain)
				results[i].Scorers[j] = ScorerObservation{ID: fmt.Sprint(j), Status: o.status, Score: o.score}
			}
		}
		visit(results)
	}
}

func scorerContracts(n int) [][]V2Scorer {
	thr := []float64{0, 0.5, 1}
	var out [][]V2Scorer
	var rec func(prefix []V2Scorer)
	rec = func(prefix []V2Scorer) {
		if len(prefix) == n {
			for _, s := range prefix {
				if s.Required {
					out = append(out, append([]V2Scorer(nil), prefix...))
					return
				}
			}
			return
		}
		for _, r := range []bool{true, false} {
			for _, t := range thr {
				rec(append(prefix, V2Scorer{ID: fmt.Sprint(len(prefix)), Required: r, Threshold: t}))
			}
		}
	}
	rec(nil)
	return out
}

// qualityOfCases is the single verdict function used by the producer
// (runCases) and by the inspector (verifyMeasurementAccounting).
func TestFormalP3AggregationExhaustive(t *testing.T) {
	var bad []string
	checked := 0
	for _, b := range []struct{ cases, scorers int }{{1, 1}, {2, 1}, {3, 1}, {1, 2}, {2, 2}} {
		for _, contract := range scorerContracts(b.scorers) {
			enumerate(b.cases, b.scorers, func(results []V2CaseResult) {
				checked++
				if got, want := qualityOfCases(results, contract), absOracle(results, contract); got != want && len(bad) < 50 {
					var cs []string
					for _, r := range results {
						var ss []string
						for _, o := range r.Scorers {
							ss = append(ss, o.Status+"/"+scoreStr(o.Score))
						}
						cs = append(cs, r.State+"["+strings.Join(ss, ",")+"]")
					}
					bad = append(bad, fmt.Sprintf("VERDICT: qualityOfCases said %s, contract says %s for scorers %+v cases %s", got, want, contract, strings.Join(cs, " ")))
				}
			})
		}
	}
	report(t, bad)
	t.Logf("checked %d (cases, contract) combinations", checked)
}

// ---- P4: inspector never accepts a verdict the contract forbids ------------

// Takes real published artifacts, rewrites the retained measurement contract
// and the stored verdict, re-hashes them consistently (what a buggy writer or
// a rehash tamper produces), and asks VerifyArtifact. An accepted artifact
// must name at least one required scorer, keep thresholds in [0,1] (the
// limits config validation enforces), and carry the contract's verdict.
func TestFormalP4InspectorVerdict(t *testing.T) {
	base := runShape{kinds: [2]string{"custom", "custom"}, required: [2]bool{true, true}, thr: [2]float64{0.5, 0.5}, attempts: 1}
	var bases []caseSpec
	for _, pair := range [][2]string{{"scored0", "scored1"}, {"scored0.5", "scored0.5"}, {"scored1", "uneval"}, {"scored0", "scored0"}} {
		bases = append(bases, caseSpec{id: "b", beh: pair})
	}
	thrDomain := []float64{-0.5, 0, 0.5, 1, 1.5}
	classes := map[string]string{}
	checked := 0
	for _, bc := range bases {
		raw, custom, _ := buildRun(t, base, []caseSpec{bc}, 1)
		dir := filepath.Join(t.TempDir(), "artifact")
		if o := RunJSONWithScorers(context.Background(), raw, dir, custom); o.State != "COMPLETED" {
			t.Fatalf("base run failed: %+v", o)
		}
		runRaw, _ := os.ReadFile(filepath.Join(dir, "run.json"))
		reportRaw, _ := os.ReadFile(filepath.Join(dir, "report.json"))
		for _, r0 := range []bool{true, false} {
			for _, r1 := range []bool{true, false} {
				for _, t0 := range thrDomain {
					for _, t1 := range thrDomain {
						for _, q := range []string{"PASS", "FAIL", "INDETERMINATE"} {
							checked++
							var run Outcome
							if err := json.Unmarshal(runRaw, &run); err != nil {
								t.Fatal(err)
							}
							run.Measurement.Scorers[0].Required, run.Measurement.Scorers[0].Threshold = r0, t0
							run.Measurement.Scorers[1].Required, run.Measurement.Scorers[1].Threshold = r1, t1
							run.Quality = &q
							var rep map[string]any
							_ = json.Unmarshal(reportRaw, &rep)
							rep["quality"] = q
							mdir := filepath.Join(t.TempDir(), "mutated")
							writeArtifact(t, mdir, run, rep)
							v, err := VerifyArtifact(mdir)
							if err != nil {
								continue
							}
							contractOK := (r0 || r1) && inUnit(t0) && inUnit(t1)
							want := absOracle(run.Cases, run.Measurement.Scorers)
							if !contractOK || *v.Quality != want {
								why := "verdict contradicts contract"
								if !(r0 || r1) {
									why = "no required scorer"
								} else if !inUnit(t0) || !inUnit(t1) {
									why = "threshold outside [0,1]"
								}
								key := why + " / " + q
								if _, seen := classes[key]; !seen {
									classes[key] = fmt.Sprintf("INSPECTOR accepted %s artifact (%s): case scorers %s,%s required=[%v %v] thresholds=[%v %v]",
										q, why, bc.beh[0], bc.beh[1], r0, r1, t0, t1)
								}
							}
						}
					}
				}
			}
		}
	}
	var bad []string
	for _, v := range classes {
		bad = append(bad, v)
	}
	report(t, bad)
	t.Logf("checked %d rewritten artifacts", checked)
}

func inUnit(x float64) bool { return x >= 0 && x <= 1 }

func writeArtifact(t *testing.T, dir string, run Outcome, rep map[string]any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	runJSON, _ := json.MarshalIndent(run, "", "  ")
	repJSON, _ := json.MarshalIndent(rep, "", "  ")
	hRun, hRep := sha256.Sum256(runJSON), sha256.Sum256(repJSON)
	marker, _ := json.Marshal(map[string]any{"version": 1, "run_sha256": hex.EncodeToString(hRun[:]), "report_sha256": hex.EncodeToString(hRep[:])})
	for name, b := range map[string][]byte{"run.json": runJSON, "report.json": repJSON, "commit.json": marker} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
