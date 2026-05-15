package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slvic/rca-agent/internal/report"
)

type auditRecord struct {
	Alert   report.Alert    `json:"alert"`
	Report  *report.RCAReport `json:"report"`
	Findings []report.Finding `json:"findings_with_raw,omitempty"`
}

// WriteAuditLog writes the investigation result to ./audits/TIMESTAMP-ALERTNAME.json.
// Always called regardless of LLM success — this is training data.
func WriteAuditLog(alert report.Alert, rep *report.RCAReport) error {
	if err := os.MkdirAll("audits", 0o755); err != nil {
		return fmt.Errorf("create audits dir: %w", err)
	}

	ts := time.Now().UTC().Format("20060102-150405")
	name := sanitize(alert.Name)
	filename := filepath.Join("audits", fmt.Sprintf("%s-%s.json", ts, name))

	rec := auditRecord{
		Alert:    alert,
		Report:   rep,
		Findings: rep.Findings,
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal audit: %w", err)
	}

	return os.WriteFile(filename, data, 0o644)
}

func sanitize(s string) string {
	replacer := strings.NewReplacer(
		" ", "_",
		"/", "_",
		"\\", "_",
		":", "_",
	)
	s = replacer.Replace(s)
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}
