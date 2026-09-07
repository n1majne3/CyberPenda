package fgs

import (
	"context"
	"fmt"
	"strings"

	"pentest/internal/owner"
)

type Report struct {
	Revision int    `json:"revision"`
	Markdown string `json:"markdown"`
}

// Report renders one accepted snapshot. Goal completion does not assert a
// vulnerability classification or a Challenge Platform result.
func (s *Service) Report(ctx context.Context, c owner.Contract, title string) (Report, error) {
	graph, err := s.Read(ctx, c)
	if err != nil {
		return Report{}, err
	}
	steps := map[string][]Node{}
	facts := map[string][]Node{}
	corrections := map[string][]string{}
	for _, n := range graph.Nodes {
		if n.Type == "step" {
			steps[n.Goal] = append(steps[n.Goal], n)
		}
		if n.Type == "fact" {
			facts[n.Step] = append(facts[n.Step], n)
			if n.Corrects != "" {
				corrections[n.Corrects] = append(corrections[n.Corrects], n.Key)
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\nAccepted revision: %d\n\n", strings.ReplaceAll(title, "\n", " "), graph.Revision)
	for _, goal := range graph.Nodes {
		if goal.Type != "goal" {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\nGoal: %s · %s\n\nSuccess criteria: %s\n\n", goal.Title, goal.Key, goal.State, goal.SuccessCriteria)
		if goal.ParentGoal != "" {
			fmt.Fprintf(&b, "Parent Goal: %s\n\n", goal.ParentGoal)
		}
		if goal.Summary != "" {
			fmt.Fprintf(&b, "%s\n\n", goal.Summary)
		}
		if goal.Reason != "" {
			fmt.Fprintf(&b, "Reason: %s\n\n", goal.Reason)
		}
		if len(goal.Facts) > 0 {
			fmt.Fprintf(&b, "Supporting Facts: %s\n\n", strings.Join(goal.Facts, ", "))
		}
		for _, step := range steps[goal.Key] {
			fmt.Fprintf(&b, "### %s\n\nStep: %s · %s\n\n", step.Action, step.Key, step.State)
			if step.Reason != "" {
				fmt.Fprintf(&b, "Reason: %s\n\n", step.Reason)
			}
			for _, fact := range facts[step.Key] {
				fmt.Fprintf(&b, "#### %s\n\nFact: %s\n\n", fact.Summary, fact.Key)
				if fact.Body != "" {
					fmt.Fprintf(&b, "%s\n\n", fact.Body)
				}
				if fact.Corrects != "" {
					fmt.Fprintf(&b, "Corrects: %s\n\n", fact.Corrects)
				}
				if keys := corrections[fact.Key]; len(keys) > 0 {
					fmt.Fprintf(&b, "Corrected by: %s\n\n", strings.Join(keys, ", "))
				}
				if len(fact.DataRefs) > 0 {
					fmt.Fprintf(&b, "Data references: %s\n\n", strings.Join(fact.DataRefs, ", "))
				}
			}
		}
	}
	if len(graph.Nodes) == 0 {
		b.WriteString("No accepted Goals, Steps, or Facts.\n")
	}
	return Report{Revision: graph.Revision, Markdown: b.String()}, nil
}
