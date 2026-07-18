package trading

import "time"

// DecisionReport is the canonical output of the deterministic trading brain.
// This is the Go reference implementation. All numbers that the LLM or user sees
// for trading decisions must originate here (or submodules).
// Direct faithful port of the Python schema + scoring structures.
type DecisionReport struct {
	Symbol          string                 `json:"symbol"`
	Market          string                 `json:"market"`
	Timestamp       time.Time              `json:"timestamp"`
	Direction       string                 `json:"direction"` // long / short / neutral
	Score           float64                `json:"score"`
	FinalScore      float64                `json:"final_score"`
	Regime          string                 `json:"regime"`
	Strategy        string                 `json:"strategy"`
	Confidence      float64                `json:"confidence"`
	Probability     float64                `json:"probability"`
	Risk            map[string]interface{} `json:"risk"`
	Plan            map[string]interface{} `json:"plan"`
	DisciplineGuard map[string]interface{} `json:"discipline_guard"`
	DataQuality     map[string]interface{} `json:"data_quality"`
	ContextFlags    []map[string]interface{} `json:"context_flags"`
	Meta            map[string]interface{} `json:"meta,omitempty"`
}

// ScoreComponent and ScoreBreakdown are used inside the report.
type ScoreComponent struct {
	Name   string  `json:"name"`
	Score  float64 `json:"score"`
	Weight float64 `json:"weight"`
	Reason string  `json:"reason"`
}

type ScoreBreakdown struct {
	Direction       string                 `json:"direction"`
	FinalScore      float64                `json:"final_score"`
	Components      []ScoreComponent       `json:"components"`
	DirectionScores map[string]interface{} `json:"direction_scores"`
	Regime          string                 `json:"regime"`
	Reasons         []string               `json:"reasons"`
}