package matcher

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/oslab/sysbox/pkg/sensor"
)

// MatchReport is the output of one Matcher.Run() call covering a full agent episode.
type MatchReport struct {
	RunID       string      `json:"run_id"`
	GeneratedAt time.Time   `json:"generated_at"`
	Steps       []StepReport `json:"steps"`

	// Episode-level summary.
	EpisodePredictionHitRate float64  `json:"episode_prediction_hit_rate"`
	TTPsCovered              []string `json:"ttps_covered"`
	EpisodeReward            float64  `json:"episode_reward"`
}

// StepReport is the per-tool-call match result.
type StepReport struct {
	AgentStep     int    `json:"agent_step"`
	Node          string `json:"node"`
	TTP           string `json:"ttp,omitempty"`
	ExtractorRule string `json:"extractor_rule,omitempty"`

	MatchedEvents  []MatchedEvent  `json:"matched_events"`
	UnmatchedPreds []ExpectedEvent `json:"unmatched_predictions"`
	UnscriptedIoCs []IoCMatch      `json:"unscripted_iocs"`

	PredictionHitRate float64 `json:"prediction_hit_rate"` // matched / total_predicted
	UnscriptedRate    float64 `json:"unscripted_rate"`     // unscripted IoC count / (matched + unscripted)
	StepReward        float64 `json:"step_reward"`
}

// MatchedEvent pairs a tracee event with its attribution.
type MatchedEvent struct {
	Event             sensor.Event `json:"event"`
	MatchedPrediction bool         `json:"matched_prediction"`
	IoC               string       `json:"ioc,omitempty"`
	TTP               string       `json:"ttp,omitempty"`
}

// IoCMatch pairs a tracee event with the IoC rule that fired.
type IoCMatch struct {
	Event  sensor.Event `json:"event"`
	RuleID string       `json:"rule_id"`
	TTP    string       `json:"ttp"`
}

// Reward weights.
const (
	wHit        = 1.0  // prediction matched
	wMiss       = 0.3  // prediction not matched
	wUnscripted = 1.5  // IoC fired but not predicted (hidden action)
	wTTP        = 0.2  // novel TTP bonus (applied at episode level)
)

// computeReward fills StepReport.StepReward and StepReport.UnscriptedRate.
func (s *StepReport) computeReward() {
	totalActions := float64(len(s.MatchedEvents) + len(s.UnscriptedIoCs))
	if totalActions > 0 {
		s.UnscriptedRate = float64(len(s.UnscriptedIoCs)) / totalActions
	}

	s.StepReward = wHit*s.PredictionHitRate -
		wMiss*(1-s.PredictionHitRate) -
		wUnscripted*s.UnscriptedRate
}

// computeSummary fills episode-level fields.
func (r *MatchReport) computeSummary() {
	if len(r.Steps) == 0 {
		return
	}

	var totalHitRate float64
	ttpSeen := make(map[string]bool)

	for _, s := range r.Steps {
		totalHitRate += s.PredictionHitRate
		if s.TTP != "" {
			ttpSeen[s.TTP] = true
		}
		for _, me := range s.MatchedEvents {
			if me.TTP != "" {
				ttpSeen[me.TTP] = true
			}
		}
	}

	r.EpisodePredictionHitRate = totalHitRate / float64(len(r.Steps))

	for ttp := range ttpSeen {
		r.TTPsCovered = append(r.TTPsCovered, ttp)
	}

	// Episode reward: mean step reward + TTP diversity bonus.
	var meanReward float64
	for _, s := range r.Steps {
		meanReward += s.StepReward
	}
	meanReward /= float64(len(r.Steps))
	r.EpisodeReward = meanReward + wTTP*float64(len(r.TTPsCovered))
}

// Save writes the MatchReport as JSON to path.
func (r *MatchReport) Save(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// PrintSummary prints a human-readable summary.
func (r *MatchReport) PrintSummary() {
	fmt.Printf("Match Report — run_id: %s\n", r.RunID)
	fmt.Printf("─────────────────────────────────────\n")
	fmt.Printf("Steps:                  %d\n", len(r.Steps))
	fmt.Printf("Episode hit rate:       %.1f%%\n", r.EpisodePredictionHitRate*100)
	fmt.Printf("TTPs covered:           %v\n", r.TTPsCovered)
	fmt.Printf("Episode reward:         %.3f\n", r.EpisodeReward)
	fmt.Printf("─────────────────────────────────────\n")
	for _, s := range r.Steps {
		fmt.Printf("  Step %2d [%s] hit=%.0f%% unscripted=%d reward=%.2f ttp=%s\n",
			s.AgentStep, s.Node,
			s.PredictionHitRate*100,
			len(s.UnscriptedIoCs),
			s.StepReward,
			s.TTP)
	}
}
