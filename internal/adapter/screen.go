package adapter

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/term"
)

const screenRegionBottom = "viewport.bottom"

// ScreenHint is one copied, normalized edge from a private adapter rule.
type ScreenHint struct {
	// Kind is evidence for Detector, not an Agent state.
	Kind detect.Kind
	// Rule is the stable adapter-owned rule identity.
	Rule string
	// Edge reports whether the rule appeared or cleared.
	Edge agent.ScreenEdge
	// Region is a stable identifier and never contains screen text.
	Region string
	// Evidence is static adapter-authored audit text.
	Evidence string
	// Confidence is bounded to the inclusive range from zero to one.
	Confidence float64
	// Confirmation must equal Detector's fixed duration for this edge.
	Confirmation time.Duration
}

type screenRuleProvider func() []screenRuleDefinition

type screenRuleDefinition struct {
	name       string
	region     screenRegion
	evidence   string
	confidence float64
	pattern    string
	present    screenRuleEdge
	cleared    *screenRuleEdge
}

type screenRegion struct {
	name       string
	bottomRows int
}

type screenRuleEdge struct {
	kind         detect.Kind
	confirmation time.Duration
}

type compiledScreenRule struct {
	definition screenRuleDefinition
	matcher    *regexp.Regexp
	matched    bool
}

// ScreenClassifier owns the match state for one session.
type ScreenClassifier struct {
	rules []compiledScreenRule
}

func newScreenClassifier(
	vendor string,
	definitions []screenRuleDefinition,
) (*ScreenClassifier, error) {
	if vendor == "" {
		return nil, errors.New("adapter: screen classifier vendor is required")
	}

	classifier := &ScreenClassifier{
		rules: make([]compiledScreenRule, 0, len(definitions)),
	}
	names := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if _, duplicate := names[definition.name]; duplicate {
			return nil, fmt.Errorf(
				"adapter: duplicate screen rule %q",
				definition.name,
			)
		}
		names[definition.name] = struct{}{}

		matcher, err := validateScreenRule(vendor, definition)
		if err != nil {
			return nil, err
		}
		classifier.rules = append(classifier.rules, compiledScreenRule{
			definition: definition,
			matcher:    matcher,
		})
	}
	return classifier, nil
}

// Observe returns only rule edges whose configured presence changed.
func (c *ScreenClassifier) Observe(snapshot term.Snapshot) []ScreenHint {
	if c == nil || len(c.rules) == 0 {
		return nil
	}

	regions := make(map[screenRegion]string)
	hints := make([]ScreenHint, 0, len(c.rules))
	for index := range c.rules {
		rule := &c.rules[index]
		text, ok := regions[rule.definition.region]
		if !ok {
			text = screenRegionText(snapshot, rule.definition.region)
			regions[rule.definition.region] = text
		}

		matched := rule.matcher.MatchString(text)
		if matched == rule.matched {
			continue
		}
		rule.matched = matched

		edge := rule.definition.present
		edgeName := agent.ScreenEdgePresent
		if !matched {
			if rule.definition.cleared == nil {
				continue
			}
			edge = *rule.definition.cleared
			edgeName = agent.ScreenEdgeCleared
		}
		hints = append(hints, newScreenHint(rule.definition, edge, edgeName))
	}
	return hints
}

// Rebaseline replaces prior match state without reporting an edge.
func (c *ScreenClassifier) Rebaseline(snapshot term.Snapshot) {
	if c == nil {
		return
	}
	regions := make(map[screenRegion]string)
	for index := range c.rules {
		rule := &c.rules[index]
		text, ok := regions[rule.definition.region]
		if !ok {
			text = screenRegionText(snapshot, rule.definition.region)
			regions[rule.definition.region] = text
		}
		rule.matched = rule.matcher.MatchString(text)
	}
}

func validateScreenRule(
	vendor string,
	definition screenRuleDefinition,
) (*regexp.Regexp, error) {
	if !strings.HasPrefix(definition.name, vendor+".") {
		return nil, fmt.Errorf(
			"adapter: screen rule %q does not belong to vendor %q",
			definition.name,
			vendor,
		)
	}
	if definition.region.name == "" ||
		definition.region.bottomRows <= 0 ||
		definition.region.bottomRows > term.MaxViewRows {
		return nil, fmt.Errorf(
			"adapter: screen rule %q has an invalid region",
			definition.name,
		)
	}
	if math.IsNaN(definition.confidence) ||
		math.IsInf(definition.confidence, 0) ||
		definition.confidence < 0 ||
		definition.confidence > 1 {
		return nil, fmt.Errorf(
			"adapter: screen rule %q has invalid confidence %v",
			definition.name,
			definition.confidence,
		)
	}
	if definition.pattern == "" {
		return nil, fmt.Errorf(
			"adapter: screen rule %q has an empty matcher",
			definition.name,
		)
	}
	if err := validateScreenRuleEdge(
		vendor,
		definition,
		definition.present,
		agent.ScreenEdgePresent,
	); err != nil {
		return nil, err
	}
	if definition.cleared != nil {
		if err := validateScreenRuleEdge(
			vendor,
			definition,
			*definition.cleared,
			agent.ScreenEdgeCleared,
		); err != nil {
			return nil, err
		}
	}

	matcher, err := regexp.Compile(definition.pattern)
	if err != nil {
		return nil, fmt.Errorf(
			"adapter: compile screen rule %q: %w",
			definition.name,
			err,
		)
	}
	return matcher, nil
}

func validateScreenRuleEdge(
	vendor string,
	definition screenRuleDefinition,
	edge screenRuleEdge,
	edgeName agent.ScreenEdge,
) error {
	attribution, err := agent.NewScreenAttribution(
		definition.name,
		edgeName,
		definition.region.name,
		1,
		1,
		definition.evidence,
	)
	if err != nil {
		return fmt.Errorf(
			"adapter: validate screen rule %q: %w",
			definition.name,
			err,
		)
	}
	if _, err := detect.NewScreenSignal(detect.Signal{
		Kind:       edge.kind,
		Vendor:     vendor,
		Confidence: definition.confidence,
		ReceivedAt: time.Unix(1, 0).UTC(),
		Screen:     &attribution,
	}); err != nil {
		return fmt.Errorf(
			"adapter: validate screen rule %q: %w",
			definition.name,
			err,
		)
	}

	confirmation, ok := detect.ScreenRuleConfirmation(
		definition.name,
		edge.kind,
		edgeName,
	)
	if !ok || edge.confirmation != confirmation {
		return fmt.Errorf(
			"adapter: screen rule %q confirmation %s does not match detector policy",
			definition.name,
			edge.confirmation,
		)
	}
	return nil
}

func newScreenHint(
	definition screenRuleDefinition,
	edge screenRuleEdge,
	edgeName agent.ScreenEdge,
) ScreenHint {
	return ScreenHint{
		Kind:         edge.kind,
		Rule:         strings.Clone(definition.name),
		Edge:         edgeName,
		Region:       strings.Clone(definition.region.name),
		Evidence:     strings.Clone(definition.evidence),
		Confidence:   definition.confidence,
		Confirmation: edge.confirmation,
	}
}

func screenRegionText(snapshot term.Snapshot, region screenRegion) string {
	start := max(0, snapshot.Rows()-region.bottomRows)
	rows := make([]string, 0, snapshot.Rows()-start)
	for index := start; index < snapshot.Rows(); index++ {
		row, ok := snapshot.Row(index)
		if ok {
			rows = append(rows, row)
		}
	}
	return strings.Join(rows, "\n")
}
