package report

import "time"

type Alert struct {
	ID          string            `json:"id"`
	Name        string            `json:"alertname"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
	Status      string            `json:"status"` // "firing" | "resolved"
}

type TimeRange struct {
	From time.Time
	To   time.Time
}

type Finding struct {
	Source   string `json:"source"`   // "prometheus", "loki", "tempo"
	Signal   string `json:"signal"`   // "error_rate_spike", "db_latency"
	Summary  string `json:"summary"`  // goes into LLM context
	Severity string `json:"severity"` // "critical", "warning", "info"
	RawData  any    `json:"-"`        // NOT sent to LLM, for audit log only
}

type RCAReport struct {
	AlertID        string    `json:"alert_id"`
	InvestigatedAt time.Time `json:"investigated_at"`
	DurationMs     int64     `json:"duration_ms"`

	RootCause  string  `json:"root_cause"`
	Confidence float64 `json:"confidence"` // 0.0 - 1.0

	Evidence  []Evidence `json:"evidence"`
	Timeline  []Event    `json:"timeline"`
	Findings  []Finding  `json:"findings"`
	NextSteps []string   `json:"next_steps"`

	AlternativeHypotheses []string    `json:"alternative_hypotheses,omitempty"`
	ReactSteps            []ReactStep `json:"react_steps,omitempty"`
	LLMTokensUsed         int         `json:"llm_tokens_used"`
}

type Evidence struct {
	Description string `json:"description"`
	Source      string `json:"source"`
	Metric      string `json:"metric,omitempty"`
	Value       string `json:"value,omitempty"`
	Baseline    string `json:"baseline,omitempty"`
}

type Event struct {
	Time        time.Time `json:"time"`
	Description string    `json:"description"`
	Source      string    `json:"source"` // "deployment", "alert", "config_change"
}

type ReactStep struct {
	Iteration   int       `json:"iteration"`
	Thought     string    `json:"thought"`      // LLM reasoning
	Action      string    `json:"action"`       // tool name or "final_answer"
	ActionInput string    `json:"action_input"` // params passed to tool
	Observation string    `json:"observation"`  // tool result summary
	Timestamp   time.Time `json:"timestamp"`
}
