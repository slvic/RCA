package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/slvic/rca-agent/internal/report"
)

// reactLLMResponse is the JSON shape the LLM returns on every ReAct iteration.
type reactLLMResponse struct {
	Type string `json:"type"` // "tool_call" | "final_answer"

	// tool_call fields
	Tool   string `json:"tool,omitempty"`
	Reason string `json:"reason,omitempty"`

	// final_answer fields
	RootCause  string            `json:"root_cause,omitempty"`
	Confidence float64           `json:"confidence,omitempty"`
	Evidence   []report.Evidence `json:"evidence,omitempty"`
	NextSteps  []string          `json:"next_steps,omitempty"`
	Timeline   []llmEvent        `json:"timeline,omitempty"` // reuse llmEvent from agent.go
}

func (a *Agent) investigateReAct(ctx context.Context, alert report.Alert) (*report.RCAReport, error) {
	start := time.Now()

	tr := report.TimeRange{
		From: alert.StartsAt.Add(-5 * time.Minute),
		To:   time.Now().Add(2 * time.Minute),
	}

	a.logger.Info("investigation started",
		"alert", alert.Name,
		"mode", "react",
		"service", alert.Labels["service"],
		"window_from", tr.From,
		"window_to", tr.To,
	)

	maxIter := a.cfg.Investigation.ReactMaxIter
	if maxIter == 0 {
		maxIter = 8
	}

	var steps []report.ReactStep
	var history strings.Builder
	var totalTokens int
	calledTools := map[string]bool{}

	for i := 0; i < maxIter; i++ {
		userMsg := buildReactMessage(alert, tr, trimHistory(history.String()), i)

		raw, tokens, err := a.llm.Complete(ctx, reactSystemPrompt, userMsg)
		totalTokens += tokens
		if err != nil {
			return nil, fmt.Errorf("react iter %d: llm: %w", i, err)
		}

		resp, err := parseReactResponse(raw)
		if err != nil {
			a.logger.Warn("react: failed to parse llm response", "iter", i, "raw", raw, "err", err)
			// Don't stop — give the LLM another chance next iteration.
			fmt.Fprintf(&history, "\n### Step %d\nError: could not parse LLM response, retrying.\n", i+1)
			continue
		}

		if resp.Type == "final_answer" {
			steps = append(steps, report.ReactStep{
				Iteration: i,
				Thought:   "Reached conclusion",
				Action:    "final_answer",
				Timestamp: time.Now(),
			})
			return a.buildFinalReport(alert, start, resp, steps, totalTokens), nil
		}

		// Prevent re-calling the same tool.
		if calledTools[resp.Tool] {
			a.logger.Warn("react: LLM requested already-called tool, skipping", "tool", resp.Tool, "iter", i)
			fmt.Fprintf(&history, "\n### Step %d\nNote: tool %q already called, skipping.\n", i+1, resp.Tool)
			continue
		}
		calledTools[resp.Tool] = true

		finding, toolErr := a.callTool(ctx, resp.Tool, alert, tr)
		observation := ""
		if toolErr != nil {
			observation = fmt.Sprintf("ERROR: %s", toolErr.Error())
			a.logger.Warn("react: tool call failed", "tool", resp.Tool, "iter", i, "err", toolErr)
		} else {
			observation = finding.Summary
		}

		step := report.ReactStep{
			Iteration:   i,
			Thought:     resp.Reason,
			Action:      resp.Tool,
			ActionInput: resp.Tool,
			Observation: observation,
			Timestamp:   time.Now(),
		}
		steps = append(steps, step)

		fmt.Fprintf(&history, "\n### Step %d\n", i+1)
		fmt.Fprintf(&history, "Thought: %s\n", resp.Reason)
		fmt.Fprintf(&history, "Action: %s\n", resp.Tool)
		fmt.Fprintf(&history, "Observation:\n%s\n", observation)
	}

	// Exhausted iterations — force a final answer from the LLM.
	return a.reactForceFinish(ctx, alert, tr, steps, start, totalTokens)
}

// callTool maps a tool name to the concrete MCP call.
func (a *Agent) callTool(ctx context.Context, tool string, alert report.Alert, tr report.TimeRange) (report.Finding, error) {
	service := alert.Labels["service"]
	namespace := alert.Labels["namespace"]
	if namespace == "" {
		namespace = "production"
	}

	switch tool {
	case "detect_anomalies":
		return a.mcp.DetectAnomalies(ctx, service, namespace, tr)
	case "get_error_summary":
		return a.mcp.GetErrorSummary(ctx, service, tr, 10)
	case "correlate_events":
		return a.mcp.CorrelateEvents(ctx, service, namespace, tr)
	case "get_slow_traces":
		return a.mcp.GetSlowTraces(ctx, service, tr, 500)
	case "compare_profiles":
		baseline := report.TimeRange{
			From: tr.From.Add(-7 * 24 * time.Hour),
			To:   tr.To.Add(-7 * 24 * time.Hour),
		}
		return a.mcp.CompareProfiles(ctx, service, baseline, tr)
	default:
		return report.Finding{}, fmt.Errorf("unknown tool: %s", tool)
	}
}

// reactForceFinish sends one final LLM call demanding a final_answer.
func (a *Agent) reactForceFinish(
	ctx context.Context,
	alert report.Alert,
	tr report.TimeRange,
	steps []report.ReactStep,
	start time.Time,
	totalTokens int,
) (*report.RCAReport, error) {
	var history strings.Builder
	for _, s := range steps {
		fmt.Fprintf(&history, "### Step %d\nAction: %s\nObservation: %s\n",
			s.Iteration, s.Action, s.Observation)
	}

	msg := buildReactMessage(alert, tr, trimHistory(history.String()), len(steps))
	msg += "\n\nMAX ITERATIONS REACHED. You MUST return final_answer now based on available evidence."

	raw, tokens, err := a.llm.Complete(ctx, reactSystemPrompt, msg)
	totalTokens += tokens
	if err != nil {
		return nil, fmt.Errorf("react force finish: llm: %w", err)
	}

	resp, err := parseReactResponse(raw)
	if err != nil || resp.Type != "final_answer" {
		return &report.RCAReport{
			AlertID:        alert.ID,
			InvestigatedAt: start,
			DurationMs:     time.Since(start).Milliseconds(),
			RootCause:      "Insufficient evidence collected within iteration limit",
			Confidence:     0.1,
			NextSteps:      []string{"Increase react_max_iter or investigate manually"},
			LLMTokensUsed:  totalTokens,
			ReactSteps:     steps,
		}, nil
	}

	return a.buildFinalReport(alert, start, resp, steps, totalTokens), nil
}

func (a *Agent) buildFinalReport(
	alert report.Alert,
	start time.Time,
	resp *reactLLMResponse,
	steps []report.ReactStep,
	totalTokens int,
) *report.RCAReport {
	rep := &report.RCAReport{
		AlertID:        alert.ID,
		InvestigatedAt: start,
		DurationMs:     time.Since(start).Milliseconds(),
		RootCause:      resp.RootCause,
		Confidence:     resp.Confidence,
		Evidence:       resp.Evidence,
		NextSteps:      resp.NextSteps,
		LLMTokensUsed:  totalTokens,
		ReactSteps:     steps,
	}

	for _, e := range resp.Timeline {
		t, err := time.Parse(time.RFC3339, e.Time)
		if err != nil {
			t = time.Time{}
		}
		rep.Timeline = append(rep.Timeline, report.Event{
			Time:        t,
			Description: e.Description,
			Source:      e.Source,
		})
	}

	a.logger.Info("investigation complete",
		"alert", alert.Name,
		"mode", "react",
		"root_cause", rep.RootCause,
		"confidence", rep.Confidence,
		"iterations", len(steps),
		"duration_ms", rep.DurationMs,
	)

	return rep
}

func parseReactResponse(raw string) (*reactLLMResponse, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var resp reactLLMResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return nil, fmt.Errorf("json parse error: %w (raw: %.200s)", err, raw)
	}
	if resp.Type != "tool_call" && resp.Type != "final_answer" {
		return nil, fmt.Errorf("unexpected type %q (raw: %.200s)", resp.Type, raw)
	}
	return &resp, nil
}
