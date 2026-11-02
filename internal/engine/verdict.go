package engine

import "fmt"

// Quality is a committed run verdict. Artifacts store it as a string, which
// is parsed once, at the read boundary.
type Quality uint8

const (
	QualityPass Quality = iota
	QualityFail
	QualityIndeterminate
	qualityCount
)

// qualityNames are the strings qualityOfCases (v2.go) and the v1 producer
// (engine.go) write. Those files are frozen, so this list cannot be derived
// from them. TestV6VerdictVocabulary fails if they diverge.
var qualityNames = [qualityCount]string{
	QualityPass:          "PASS",
	QualityFail:          "FAIL",
	QualityIndeterminate: "INDETERMINATE",
}

func parseQuality(s string) (Quality, bool) {
	for q, name := range qualityNames {
		if name == s {
			return Quality(q), true
		}
	}
	return 0, false
}

func (q Quality) String() string               { return enumName(q, qualityNames[:]) }
func (q Quality) MarshalText() ([]byte, error) { return enumText(q, qualityNames[:]) }

// Recommendation is compare's answer, and through the CLI's one exit table
// also the exit of run.
type Recommendation uint8

const (
	RecommendShip Recommendation = iota
	RecommendIterate
	RecommendInvestigate
	recommendationCount
)

var recommendationNames = [recommendationCount]string{
	RecommendShip:        "SHIP",
	RecommendIterate:     "ITERATE",
	RecommendInvestigate: "INVESTIGATE",
}

func (r Recommendation) String() string               { return enumName(r, recommendationNames[:]) }
func (r Recommendation) MarshalText() ([]byte, error) { return enumText(r, recommendationNames[:]) }

// recommendationOf: the candidate's quality alone decides. A required
// REGRESSED cell makes the candidate FAIL or INDETERMINATE, so it never
// ships. Optional regressions are listed but never change this row.
var recommendationOf = [qualityCount]Recommendation{
	QualityPass:          RecommendShip,
	QualityFail:          RecommendIterate,
	QualityIndeterminate: RecommendInvestigate,
}

func (q Quality) Recommendation() Recommendation { return recommendationOf[q] }

// CompletedQuality returns the committed verdict of a COMPLETED outcome. ok
// is false for every other state, which has no verdict.
func (o Outcome) CompletedQuality() (q Quality, ok bool) {
	if o.State != "COMPLETED" || o.Quality == nil {
		return 0, false
	}
	return parseQuality(*o.Quality)
}

// An out-of-range enum value fails to marshal instead of printing a
// plausible-looking name.
func enumName[E ~uint8](e E, names []string) string {
	if int(e) < len(names) {
		return names[e]
	}
	return fmt.Sprintf("invalid(%d)", uint8(e))
}

func enumText[E ~uint8](e E, names []string) ([]byte, error) {
	if int(e) < len(names) {
		return []byte(names[e]), nil
	}
	return nil, fmt.Errorf("evalkit: enum value %d out of range", uint8(e))
}
