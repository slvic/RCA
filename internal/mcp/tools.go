package mcp

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/slvic/rca-agent/internal/report"
)

// logLine is the shape we expect from Loki MCP.
type logLine struct {
	timestamp string
	message   string
}

// prometheusMetric is the shape we expect from Prometheus MCP.
type prometheusMetric struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

// k8sEvent is what Grafana/k8s MCP returns for cluster events.
type k8sEvent struct {
	Time    string `json:"time"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Message string `json:"message"`
	Source  string `json:"source"`
}

// traceSpan is a single span summary from Tempo.
type traceSpan struct {
	Operation  string  `json:"operation"`
	Service    string  `json:"service"`
	DurationMs float64 `json:"duration_ms"`
	HasError   bool    `json:"has_error"`
	Baseline   float64 `json:"baseline_ms"`
}

// DetectAnomalies fetches golden signals, computes z-scores vs baseline, returns anomalous metrics.
func (c *Client) DetectAnomalies(ctx context.Context, service, namespace string, tr report.TimeRange) (report.Finding, error) {
	// Fetch current golden signals from Prometheus MCP.
	var current []prometheusMetric
	err := c.callJSON(ctx, "prometheus", "query_golden_signals", map[string]any{
		"service":    service,
		"namespace":  namespace,
		"start_time": tr.From.Format(time.RFC3339),
		"end_time":   tr.To.Format(time.RFC3339),
	}, &current)
	if err != nil {
		return report.Finding{}, fmt.Errorf("fetch current metrics: %w", err)
	}

	// Baseline: same 1h slot 7 days ago.
	baselineFrom := tr.From.Add(-7 * 24 * time.Hour)
	baselineTo := tr.To.Add(-7 * 24 * time.Hour)

	var baseline []prometheusMetric
	err = c.callJSON(ctx, "prometheus", "query_golden_signals", map[string]any{
		"service":    service,
		"namespace":  namespace,
		"start_time": baselineFrom.Format(time.RFC3339),
		"end_time":   baselineTo.Format(time.RFC3339),
	}, &baseline)
	if err != nil {
		return report.Finding{}, fmt.Errorf("fetch baseline metrics: %w", err)
	}

	baselineByName := make(map[string]float64, len(baseline))
	for _, m := range baseline {
		baselineByName[m.Name] = m.Value
	}

	// Compute z-scores. We treat the baseline single value as mean with 10% stddev heuristic
	// when only a point-in-time baseline is available (real deployments would send stddev too).
	var anomalies []metricAnomaly
	for _, m := range current {
		bv, ok := baselineByName[m.Name]
		if !ok || bv == 0 {
			continue
		}
		stddev := bv * 0.10 // 10% of baseline as estimated stddev
		z := zScore(m.Value, bv, stddev)
		if math.Abs(z) < 3.0 {
			continue
		}
		anomalies = append(anomalies, metricAnomaly{
			name:     m.Name,
			current:  m.Value,
			mean:     bv,
			stddev:   stddev,
			z:        z,
			severity: classifySeverity(z),
		})
	}

	sort.Slice(anomalies, func(i, j int) bool {
		return math.Abs(anomalies[i].z) > math.Abs(anomalies[j].z)
	})
	if len(anomalies) > 10 {
		anomalies = anomalies[:10]
	}

	if len(anomalies) == 0 {
		return report.Finding{
			Source:   "prometheus",
			Signal:   "golden_signals",
			Summary:  "No anomalous metrics detected (all within 3σ of baseline).",
			Severity: "info",
		}, nil
	}

	severity := "info"
	for _, a := range anomalies {
		if a.severity == "CRITICAL" {
			severity = "critical"
			break
		} else if a.severity == "WARNING" {
			severity = "warning"
		}
	}

	return report.Finding{
		Source:   "prometheus",
		Signal:   "anomaly_detection",
		Summary:  formatAnomalies(anomalies),
		Severity: severity,
		RawData:  anomalies,
	}, nil
}

// GetErrorSummary fetches and clusters error logs from Loki.
func (c *Client) GetErrorSummary(ctx context.Context, service string, tr report.TimeRange, limit int) (report.Finding, error) {
	type lokiResponse struct {
		Lines []struct {
			Timestamp string `json:"timestamp"`
			Message   string `json:"message"`
		} `json:"lines"`
	}

	var resp lokiResponse
	err := c.callJSON(ctx, "loki", "query_logs", map[string]any{
		"service":    service,
		"start_time": tr.From.Format(time.RFC3339),
		"end_time":   tr.To.Format(time.RFC3339),
		"levels":     []string{"ERROR", "FATAL", "CRITICAL"},
		"limit":      5000,
	}, &resp)
	if err != nil {
		return report.Finding{}, fmt.Errorf("fetch logs: %w", err)
	}

	if len(resp.Lines) == 0 {
		return report.Finding{
			Source:   "loki",
			Signal:   "error_summary",
			Summary:  "No ERROR/FATAL log lines found in investigation window.",
			Severity: "info",
		}, nil
	}

	lines := make([]logLine, 0, len(resp.Lines))
	for _, l := range resp.Lines {
		lines = append(lines, logLine{timestamp: l.Timestamp, message: l.Message})
	}

	clusters := clusterLogs(lines, limit)

	severity := "info"
	if len(clusters) > 0 && clusters[0].count > 100 {
		severity = "critical"
	} else if len(clusters) > 0 {
		severity = "warning"
	}

	return report.Finding{
		Source:   "loki",
		Signal:   "error_summary",
		Summary:  formatLogClusters(clusters),
		Severity: severity,
		RawData:  clusters,
	}, nil
}

// CorrelateEvents fetches Kubernetes events and deployment changes near the alert window.
func (c *Client) CorrelateEvents(ctx context.Context, service, namespace string, tr report.TimeRange) (report.Finding, error) {
	// Look 30 minutes before the alert window — cause precedes effect.
	lookbackFrom := tr.From.Add(-30 * time.Minute)

	var events []k8sEvent
	err := c.callJSON(ctx, "grafana", "get_kubernetes_events", map[string]any{
		"service":    service,
		"namespace":  namespace,
		"start_time": lookbackFrom.Format(time.RFC3339),
		"end_time":   tr.To.Format(time.RFC3339),
	}, &events)
	if err != nil {
		return report.Finding{}, fmt.Errorf("fetch k8s events: %w", err)
	}

	if len(events) == 0 {
		return report.Finding{
			Source:   "grafana",
			Signal:   "event_correlation",
			Summary:  "No Kubernetes events or deployments found in window.",
			Severity: "info",
		}, nil
	}

	var sb strings.Builder
	for _, e := range events {
		fmt.Fprintf(&sb, "%s %s %s/%s: %s\n", e.Time, e.Kind, e.Source, e.Name, e.Message)
	}

	return report.Finding{
		Source:   "grafana",
		Signal:   "event_correlation",
		Summary:  sb.String(),
		Severity: "info",
		RawData:  events,
	}, nil
}

// GetSlowTraces fetches traces exceeding thresholdMs or containing error spans.
func (c *Client) GetSlowTraces(ctx context.Context, service string, tr report.TimeRange, thresholdMs int) (report.Finding, error) {
	type tempoResponse struct {
		Traces []traceSpan `json:"traces"`
	}

	var resp tempoResponse
	err := c.callJSON(ctx, "tempo", "search_traces", map[string]any{
		"service":      service,
		"start_time":   tr.From.Format(time.RFC3339),
		"end_time":     tr.To.Format(time.RFC3339),
		"min_duration": fmt.Sprintf("%dms", thresholdMs),
		"limit":        100,
	}, &resp)
	if err != nil {
		return report.Finding{}, fmt.Errorf("fetch traces: %w", err)
	}

	if len(resp.Traces) == 0 {
		return report.Finding{
			Source:   "tempo",
			Signal:   "slow_traces",
			Summary:  fmt.Sprintf("No traces exceeding %dms found.", thresholdMs),
			Severity: "info",
		}, nil
	}

	// Sort by duration descending, take top 5 unique operations.
	sort.Slice(resp.Traces, func(i, j int) bool {
		return resp.Traces[i].DurationMs > resp.Traces[j].DurationMs
	})

	seen := map[string]bool{}
	var top []traceSpan
	for _, t := range resp.Traces {
		key := t.Service + "/" + t.Operation
		if seen[key] {
			continue
		}
		seen[key] = true
		top = append(top, t)
		if len(top) == 5 {
			break
		}
	}

	var sb strings.Builder
	for _, t := range top {
		errTag := ""
		if t.HasError {
			errTag = " [ERROR]"
		}
		baseline := ""
		if t.Baseline > 0 {
			baseline = fmt.Sprintf(" (baseline: %.0fms)", t.Baseline)
		}
		fmt.Fprintf(&sb, "%s → %s %.0fms%s%s\n", t.Service, t.Operation, t.DurationMs, baseline, errTag)
	}

	severity := "warning"
	if top[0].DurationMs > float64(thresholdMs)*5 {
		severity = "critical"
	}

	return report.Finding{
		Source:   "tempo",
		Signal:   "slow_traces",
		Summary:  sb.String(),
		Severity: severity,
		RawData:  top,
	}, nil
}

// CompareProfiles is a stub — profiling not wired in MVP.
func (c *Client) CompareProfiles(_ context.Context, _ string, _, _ report.TimeRange) (report.Finding, error) {
	return report.Finding{
		Source:   "pyroscope",
		Signal:   "cpu_profile_diff",
		Summary:  "Profiling not available in MVP.",
		Severity: "info",
	}, nil
}
