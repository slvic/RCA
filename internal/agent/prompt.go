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

// ── ReAct mode prompts ────────────────────────────────────────────────────────

const reactToolsDescription = `Available tools:
- detect_anomalies: golden signals z-score analysis (error rate, latency, throughput, saturation)
- get_error_summary: top error patterns from logs, clustered by template
- correlate_events: deployments, restarts, HPA events, config changes near alert time
- get_slow_traces: slowest/errored traces with critical path breakdown
- compare_profiles: CPU/memory profile diff between baseline and incident window`

const reactSystemPrompt = `You are an expert SRE performing root cause analysis step by step.
At each step you can either call a tool to gather more evidence, or conclude your investigation.

TOOLS:
` + reactToolsDescription + `

OUTPUT FORMAT — respond with ONLY valid JSON, one of:

To call a tool:
{"type": "tool_call", "tool": "<tool_name>", "reason": "<one sentence: what you expect to find and why>"}

To conclude:
{"type": "final_answer", "root_cause": "...", "confidence": 0.0-1.0, "evidence": [...], "next_steps": [...], "timeline": [...]}

RULES:
- Call each tool at most once. If you've already seen its output, don't call it again.
- Confidence > 0.8 requires at least 2 corroborating signals from different sources.
- If evidence points in multiple directions, pick the most likely and list alternatives in next_steps.
- Never guess. If evidence is insufficient, say so in root_cause and set confidence < 0.4.
- No markdown, no explanation outside JSON.`

const reactMaxHistoryChars = 6000

// trimHistory keeps the tail of history when it grows too large — recent observations matter most.
func trimHistory(history string) string {
	if len(history) <= reactMaxHistoryChars {
		return history
	}
	return "...[earlier steps trimmed]\n" + history[len(history)-reactMaxHistoryChars:]
}

func buildReactMessage(alert report.Alert, tr report.TimeRange, history string, iter int) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "## Alert\n")
	fmt.Fprintf(&sb, "Name: %s\nService: %s\nStarted: %s\n",
		alert.Name,
		alert.Labels["service"],
		alert.StartsAt.Format(time.RFC3339),
	)
	if desc := alert.Annotations["description"]; desc != "" {
		fmt.Fprintf(&sb, "Description: %s\n", desc)
	}
	fmt.Fprintln(&sb)

	fmt.Fprintf(&sb, "## Time window\n%s → %s\n\n",
		tr.From.Format(time.RFC3339),
		tr.To.Format(time.RFC3339),
	)

	if history != "" {
		fmt.Fprintf(&sb, "## Investigation so far\n%s\n", history)
	}

	fmt.Fprintf(&sb, "## Your turn (iteration %d)\n", iter+1)
	fmt.Fprintf(&sb, "What is your next action?\n")

	return sb.String()
}
