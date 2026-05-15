package webhook

import (
	"testing"
	"time"

	"github.com/slvic/rca-agent/internal/report"
	"github.com/stretchr/testify/assert"
)

func TestDeduplicator_FirstSeen(t *testing.T) {
	d := NewDeduplicator(10 * time.Minute)
	a := report.Alert{
		Name:   "HighErrorRate",
		Labels: map[string]string{"service": "checkout", "severity": "critical"},
	}
	assert.False(t, d.IsDuplicate(a))
}

func TestDeduplicator_SecondSeen(t *testing.T) {
	d := NewDeduplicator(10 * time.Minute)
	a := report.Alert{
		Name:   "HighErrorRate",
		Labels: map[string]string{"service": "checkout", "severity": "critical"},
	}
	d.IsDuplicate(a) // first call
	assert.True(t, d.IsDuplicate(a))
}

func TestDeduplicator_DifferentLabels(t *testing.T) {
	d := NewDeduplicator(10 * time.Minute)
	a1 := report.Alert{Name: "HighErrorRate", Labels: map[string]string{"service": "checkout"}}
	a2 := report.Alert{Name: "HighErrorRate", Labels: map[string]string{"service": "payment"}}
	d.IsDuplicate(a1)
	assert.False(t, d.IsDuplicate(a2), "different labels → different key")
}

func TestAlertKey_Deterministic(t *testing.T) {
	a1 := report.Alert{Name: "X", Labels: map[string]string{"b": "2", "a": "1"}}
	a2 := report.Alert{Name: "X", Labels: map[string]string{"a": "1", "b": "2"}}
	assert.Equal(t, alertKey(a1), alertKey(a2), "key should be order-independent")
}
