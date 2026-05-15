package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/slvic/rca-agent/internal/report"
)

const systemPrompt = `You are an expert SRE performing root cause analysis.
You will receive structured findings from observability tools about a production incident.
Your job is to synthesize these into a clear root cause hypothesis.

Rules:
- Be specific. "Database connection pool exhausted" not "database issue"
- Only claim what the evidence supports. Do not guess.
- Confidence should reflect evidence strength: 0.9+ requires multiple corroborating signals
- If evidence is insufficient, say so and recommend what to investigate next
- Timeline matters: cause must precede effect

Output ONLY valid JSON matching this schema:
{
  "root_cause": "string — specific technical description",
  "confidence": 0.0-1.0,
  "evidence": [
    {"description": "string", "source": "string", "metric": "string", "value": "string", "baseline": "string"}
  ],
  "timeline": [
    {"time": "ISO8601", "description": "string", "source": "string"}
  ],
  "next_steps": ["string"],
  "alternative_hypotheses": ["string"]
}`

const maxFindingsChars = 8000

func buildUserMessage(alert report.Alert, tr report.TimeRange, findings []report.Finding) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "## Alert\n")
	fmt.Fprintf(&sb, "Name: %s\n", alert.Name)
	fmt.Fprintf(&sb, "Service: %s\n", alert.Labels["service"])
	fmt.Fprintf(&sb, "Started: %s\n", alert.StartsAt.Format(time.RFC3339))
	if desc := alert.Annotations["description"]; desc != "" {
		fmt.Fprintf(&sb, "Description: %s\n", desc)
	}
	fmt.Fprintln(&sb)

	fmt.Fprintf(&sb, "## Investigation window\n")
	fmt.Fprintf(&sb, "%s → %s\n\n", tr.From.Format(time.RFC3339), tr.To.Format(time.RFC3339))

	// Budget context: cap total findings to maxFindingsChars.
	perFinding := maxFindingsChars
	if len(findings) > 0 {
		perFinding = maxFindingsChars / len(findings)
	}

	fmt.Fprintf(&sb, "## Findings\n")
	for _, f := range findings {
		summary := f.Summary
		if len(summary) > perFinding {
			summary = summary[:perFinding] + "\n...(truncated)"
		}
		fmt.Fprintf(&sb, "### %s [%s] severity=%s\n", f.Signal, f.Source, f.Severity)
		fmt.Fprintf(&sb, "%s\n\n", summary)
	}

	return sb.String()
}
