package webhook

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/slvic/rca-agent/internal/agent"
	"github.com/slvic/rca-agent/internal/report"
)

// AMPayload is the Alertmanager webhook payload shape.
type AMPayload struct {
	Receiver string         `json:"receiver"`
	Status   string         `json:"status"`
	Alerts   []report.Alert `json:"alerts"`
}

type Handler struct {
	agent             *agent.Agent
	dedup             *Deduplicator
	logger            *slog.Logger
	investigationTimeout time.Duration
	allowedSeverities map[string]bool
}

func NewHandler(ag *agent.Agent, dedup *Deduplicator, logger *slog.Logger, timeout time.Duration, severities []string) *Handler {
	allowed := make(map[string]bool, len(severities))
	for _, s := range severities {
		allowed[s] = true
	}
	return &Handler{
		agent:             ag,
		dedup:             dedup,
		logger:            logger,
		investigationTimeout: timeout,
		allowedSeverities: allowed,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload AMPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		h.logger.Warn("bad webhook payload", "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Respond immediately so Alertmanager doesn't time out.
	w.WriteHeader(http.StatusOK)

	for _, a := range payload.Alerts {
		if a.Status != "firing" {
			continue
		}
		if !h.allowedSeverities[a.Labels["severity"]] {
			continue
		}
		if h.dedup.IsDuplicate(a) {
			h.logger.Debug("duplicate alert suppressed", "alert", a.Name)
			continue
		}

		go h.investigate(a)
	}
}

func (h *Handler) investigate(a report.Alert) {
	ctx, cancel := context.WithTimeout(context.Background(), h.investigationTimeout)
	defer cancel()

	rep, err := h.agent.Investigate(ctx, a)
	if err != nil {
		h.logger.Error("investigation failed", "alert", a.Name, "err", err)
		return
	}

	// Always write audit log regardless of success.
	if auditErr := agent.WriteAuditLog(a, rep); auditErr != nil {
		h.logger.Warn("audit log write failed", "err", auditErr)
	}

	// Log the report as structured JSON until Slack output is wired.
	data, _ := rep.ToJSON()
	h.logger.Info("rca_report",
		"alert", a.Name,
		"root_cause", rep.RootCause,
		"confidence", rep.Confidence,
		"report_json", string(data),
	)
}
