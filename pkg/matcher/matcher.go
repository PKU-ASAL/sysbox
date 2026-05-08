package matcher

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/sensor"
)

// Matcher joins Predictions against a tracee event stream.
type Matcher struct {
	IoC *IoCEngine
}

// NewMatcher creates a Matcher with the given IoC engine.
func NewMatcher(ioc *IoCEngine) *Matcher {
	return &Matcher{IoC: ioc}
}

// RunFile reads predictions and events from files, runs the full match,
// and returns a MatchReport. This is the primary entry point for
// `sysbox match run`.
func (m *Matcher) RunFile(predictionsPath, eventsPath, runID string) (*MatchReport, error) {
	preds, err := ReadPredictions(predictionsPath)
	if err != nil {
		return nil, fmt.Errorf("read predictions: %w", err)
	}

	events, err := readEvents(eventsPath)
	if err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}

	return m.Run(preds, events, runID), nil
}

// Run executes the Matcher against an in-memory slice of events.
func (m *Matcher) Run(preds []Prediction, events []sensor.Event, runID string) *MatchReport {
	report := &MatchReport{
		RunID:       runID,
		GeneratedAt: time.Now(),
	}

	// Match each prediction.
	for _, pred := range preds {
		step := m.matchPrediction(pred, events)
		report.Steps = append(report.Steps, step)
	}

	// Find unscripted IoC hits (events not covered by any prediction).
	predictedEventIDs := make(map[string]bool)
	for _, step := range report.Steps {
		for _, me := range step.MatchedEvents {
			predictedEventIDs[eventKey(me.Event)] = true
		}
	}

	// Global IoC scan for unscripted events.
	var unscriptedByStep = make(map[int][]IoCMatch)
	for _, ev := range events {
		if predictedEventIDs[eventKey(ev)] {
			continue
		}
		if ruleID, ttp, ok := m.IoC.Scan(ev); ok {
			// Attribute to closest prediction step by time.
			step := closestStep(report.Steps, ev.Timestamp)
			unscriptedByStep[step] = append(unscriptedByStep[step], IoCMatch{
				Event:  ev,
				RuleID: ruleID,
				TTP:    ttp,
			})
		}
	}
	for i := range report.Steps {
		report.Steps[i].UnscriptedIoCs = unscriptedByStep[report.Steps[i].AgentStep]
		report.Steps[i].computeReward()
	}

	report.computeSummary()
	return report
}

// matchPrediction matches one Prediction against the event stream.
func (m *Matcher) matchPrediction(pred Prediction, events []sensor.Event) StepReport {
	step := StepReport{
		AgentStep:     pred.AgentStep,
		Node:          pred.Node,
		TTP:           pred.TTP,
		ExtractorRule: pred.ExtractorRule,
	}

	windowEnd := pred.SubmittedAt.Add(time.Duration(pred.TimeWindow) * time.Second)

	// Filter events to this prediction's time window and node.
	var window []sensor.Event
	for _, ev := range events {
		t := time.Unix(0, ev.Timestamp)
		if ev.NodeID == pred.Node && !t.Before(pred.SubmittedAt) && !t.After(windowEnd) {
			window = append(window, ev)
		}
	}

	// For each expected event, find the first matching event in the window.
	usedIdx := make(map[int]bool)
	for _, expected := range pred.ExpectedEvents {
		found := false
		for i, ev := range window {
			if usedIdx[i] {
				continue
			}
			if matchExpected(expected, ev) {
				iocID, iocTTP, _ := m.IoC.Scan(ev)
				ttp := pred.TTP
				if iocTTP != "" {
					ttp = iocTTP
				}
				step.MatchedEvents = append(step.MatchedEvents, MatchedEvent{
					Event:             ev,
					MatchedPrediction: true,
					IoC:               iocID,
					TTP:               ttp,
				})
				usedIdx[i] = true
				found = true
				break
			}
		}
		if !found {
			step.UnmatchedPreds = append(step.UnmatchedPreds, expected)
		}
	}

	total := len(pred.ExpectedEvents)
	if total > 0 {
		step.PredictionHitRate = float64(len(step.MatchedEvents)) / float64(total)
	} else {
		step.PredictionHitRate = 1.0
	}

	return step
}

// matchExpected checks whether event ev satisfies the ExpectedEvent pattern.
func matchExpected(expected ExpectedEvent, ev sensor.Event) bool {
	if expected.Name != "" && expected.Name != ev.Name {
		return false
	}
	for key, expVal := range expected.Args {
		key = strings.TrimPrefix(key, "args.")
		actualVal, ok := ev.Args[key]

		// Special case: pathname_prefix
		if key == "pathname_prefix" {
			s, _ := expVal.(string)
			actual, _ := actualVal.(string)
			if !strings.HasPrefix(actual, s) {
				return false
			}
			continue
		}

		if !ok {
			return false
		}

		// CIDR match for remote_addr
		if key == "remote_addr" {
			if !cidrMatch(expVal, actualVal) {
				return false
			}
			continue
		}

		// Port list or exact
		if key == "remote_port" {
			if !portMatch(expVal, actualVal) {
				return false
			}
			continue
		}

		if !matchValue(expVal, actualVal) {
			return false
		}
	}
	return true
}

func cidrMatch(cidrVal, addrVal any) bool {
	cidr, ok := cidrVal.(string)
	if !ok {
		return false
	}
	addr, ok := addrVal.(string)
	if !ok {
		return false
	}
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return cidr == addr
	}
	ip := net.ParseIP(addr)
	return ip != nil && ipNet.Contains(ip)
}

func portMatch(expected, actual any) bool {
	actualPort := -1
	switch v := actual.(type) {
	case float64:
		actualPort = int(v)
	case int:
		actualPort = v
	}
	if actualPort < 0 {
		return false
	}
	switch e := expected.(type) {
	case float64:
		return actualPort == int(e)
	case int:
		return actualPort == e
	case []any:
		for _, item := range e {
			switch p := item.(type) {
			case float64:
				if actualPort == int(p) {
					return true
				}
			case int:
				if actualPort == p {
					return true
				}
			}
		}
	}
	return false
}

func eventKey(ev sensor.Event) string {
	return fmt.Sprintf("%s:%d:%d", ev.Name, ev.Timestamp, ev.PID)
}

func closestStep(steps []StepReport, ts int64) int {
	if len(steps) == 0 {
		return 0
	}
	best := steps[0].AgentStep
	return best
}

func readEvents(path string) ([]sensor.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var events []sensor.Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev sensor.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	return events, scanner.Err()
}
