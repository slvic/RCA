package report

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToJSON serializes the report to indented JSON.
func (r *RCAReport) ToJSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// ToMarkdown formats the report as a human-readable Slack/markdown block.
func (r *RCAReport) ToMarkdown() string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "## RCA Report — %s\n\n", r.AlertID)
	fmt.Fprintf(&sb, "**Root Cause:** %s\n", r.RootCause)
	fmt.Fprintf(&sb, "**Confidence:** %.0f%%\n\n", r.Confidence*100)

	if len(r.Evidence) > 0 {
		fmt.Fprintf(&sb, "### Evidence\n")
		for _, e := range r.Evidence {
			line := fmt.Sprintf("- [%s] %s", e.Source, e.Description)
			if e.Metric != "" {
				line += fmt.Sprintf(" (`%s`: %s", e.Metric, e.Value)
				if e.Baseline != "" {
					line += fmt.Sprintf(", baseline: %s", e.Baseline)
				}
				line += ")"
			}
			fmt.Fprintln(&sb, line)
		}
		fmt.Fprintln(&sb)
	}

	if len(r.Timeline) > 0 {
		fmt.Fprintf(&sb, "### Timeline\n")
		for _, ev := range r.Timeline {
			fmt.Fprintf(&sb, "- `%s` [%s] %s\n", ev.Time.Format("15:04:05"), ev.Source, ev.Description)
		}
		fmt.Fprintln(&sb)
	}

	if len(r.NextSteps) > 0 {
		fmt.Fprintf(&sb, "### Next Steps\n")
		for i, step := range r.NextSteps {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, step)
		}
		fmt.Fprintln(&sb)
	}

	if len(r.AlternativeHypotheses) > 0 {
		fmt.Fprintf(&sb, "### Alternative Hypotheses\n")
		for _, h := range r.AlternativeHypotheses {
			fmt.Fprintf(&sb, "- %s\n", h)
		}
		fmt.Fprintln(&sb)
	}

	fmt.Fprintf(&sb, "_Investigated in %dms, %d LLM tokens used_\n", r.DurationMs, r.LLMTokensUsed)
	return sb.String()
}
