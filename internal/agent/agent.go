package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/slvic/rca-agent/internal/config"
	"github.com/slvic/rca-agent/internal/llm"
	"github.com/slvic/rca-agent/internal/mcp"
	"github.com/slvic/rca-agent/internal/report"
)

type Agent struct {
	llm    *llm.Client
	mcp    *mcp.Client
	cfg    *config.Config
	logger *slog.Logger
}

func New(llmClient *llm.Client, mcpClient *mcp.Client, cfg *config.Config, logger *slog.Logger) *Agent {
	return &Agent{
		llm:    llmClient,
		mcp:    mcpClient,
		cfg:    cfg,
		logger: logger,
	}
}

func (a *Agent) Investigate(ctx context.Context, alert report.Alert) (*report.RCAReport, error) {
	start := time.Now()

	tr := report.TimeRange{
		From: alert.StartsAt.Add(-5 * time.Minute),
		To:   time.Now().Add(2 * time.Minute),
	}

	service := alert.Labels["service"]
	namespace := alert.Labels["namespace"]
	if namespace == "" {
		namespace = "production"
	}

	a.logger.Info("investigation started",
		"alert", alert.Name,
		"service", service,
		"namespace", namespace,
		"window_from", tr.From,
		"window_to", tr.To,
	)

	findings, err := a.runTools(ctx, service, namespace, tr)
	if err != nil {
		// Non-fatal: synthesize with whatever findings we have.
		a.logger.Warn("some tools failed", "err", err)
	}

	rep, err := a.synthesize(ctx, alert, tr, findings)
	if err != nil {
		return nil, fmt.Errorf("synthesis failed: %w", err)
	}

	rep.AlertID = alert.ID
	rep.InvestigatedAt = start
	rep.DurationMs = time.Since(start).Milliseconds()
	rep.Findings = findings

	a.logger.Info("investigation complete",
		"alert", alert.Name,
		"root_cause", rep.RootCause,
		"confidence", rep.Confidence,
		"duration_ms", rep.DurationMs,
	)

	return rep, nil
}

type toolResult struct {
	finding report.Finding
	err     error
}

func (a *Agent) runTools(ctx context.Context, service, namespace string, tr report.TimeRange) ([]report.Finding, error) {
	// All tool calls run in parallel; each gets 30s (enforced by mcp.Client per-call timeout).
	// We collect results with a 45s combined ceiling.
	collectCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	type toolDef struct {
		name string
		fn   func() (report.Finding, error)
	}

	tools := []toolDef{
		{"detect_anomalies", func() (report.Finding, error) {
			return a.mcp.DetectAnomalies(collectCtx, service, namespace, tr)
		}},
		{"get_error_summary", func() (report.Finding, error) {
			return a.mcp.GetErrorSummary(collectCtx, service, tr, 10)
		}},
		{"correlate_events", func() (report.Finding, error) {
			return a.mcp.CorrelateEvents(collectCtx, service, namespace, tr)
		}},
		{"get_slow_traces", func() (report.Finding, error) {
			return a.mcp.GetSlowTraces(collectCtx, service, tr, 500)
		}},
	}

	ch := make(chan toolResult, len(tools))
	for _, t := range tools {
		go func(name string, fn func() (report.Finding, error)) {
			f, err := fn()
			if err != nil {
				a.logger.Error("tool failed", "tool", name, "err", err)
			}
			ch <- toolResult{finding: f, err: err}
		}(t.name, t.fn)
	}

	var findings []report.Finding
	var errs []string
	for range tools {
		r := <-ch
		if r.err != nil {
			errs = append(errs, r.err.Error())
			continue
		}
		if r.finding.Summary != "" {
			findings = append(findings, r.finding)
		}
	}

	if len(errs) > 0 {
		return findings, fmt.Errorf("tool errors: %s", strings.Join(errs, "; "))
	}
	return findings, nil
}

// llmSynthesisOutput matches what we ask the LLM to return.
type llmSynthesisOutput struct {
	RootCause              string          `json:"root_cause"`
	Confidence             float64         `json:"confidence"`
	Evidence               []report.Evidence `json:"evidence"`
	Timeline               []llmEvent      `json:"timeline"`
	NextSteps              []string        `json:"next_steps"`
	AlternativeHypotheses  []string        `json:"alternative_hypotheses"`
}

type llmEvent struct {
	Time        string `json:"time"`
	Description string `json:"description"`
	Source      string `json:"source"`
}

func (a *Agent) synthesize(ctx context.Context, alert report.Alert, tr report.TimeRange, findings []report.Finding) (*report.RCAReport, error) {
	synthCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	userMsg := buildUserMessage(alert, tr, findings)

	text, tokens, err := a.llm.Complete(synthCtx, systemPrompt, userMsg)
	if err != nil {
		return nil, fmt.Errorf("llm complete: %w", err)
	}

	out, parseErr := parseLLMOutput(text)
	if parseErr != nil {
		// Retry once with explicit JSON reminder.
		a.logger.Warn("LLM output parse failed, retrying", "err", parseErr)
		retry := userMsg + "\n\nIMPORTANT: Return ONLY valid JSON, no markdown code blocks."
		text2, tokens2, err2 := a.llm.Complete(synthCtx, systemPrompt, retry)
		tokens += tokens2
		if err2 != nil {
			return degradedReport(tokens, parseErr), nil
		}
		out, parseErr = parseLLMOutput(text2)
		if parseErr != nil {
			return degradedReport(tokens, parseErr), nil
		}
	}

	rep := &report.RCAReport{
		RootCause:             out.RootCause,
		Confidence:            out.Confidence,
		Evidence:              out.Evidence,
		NextSteps:             out.NextSteps,
		AlternativeHypotheses: out.AlternativeHypotheses,
		LLMTokensUsed:         tokens,
	}

	for _, e := range out.Timeline {
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

	return rep, nil
}

func parseLLMOutput(text string) (*llmSynthesisOutput, error) {
	// Strip markdown code fences if the model slipped them in.
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		first := strings.Index(text, "\n")
		last := strings.LastIndex(text, "```")
		if first != -1 && last > first {
			text = strings.TrimSpace(text[first+1 : last])
		}
	}

	var out llmSynthesisOutput
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, fmt.Errorf("unmarshal LLM JSON: %w (raw: %.300s)", err, text)
	}
	return &out, nil
}

func degradedReport(tokens int, cause error) *report.RCAReport {
	return &report.RCAReport{
		RootCause:     "LLM output parse error — manual investigation required",
		Confidence:    0,
		NextSteps:     []string{"Check audit log for raw findings", cause.Error()},
		LLMTokensUsed: tokens,
	}
}
