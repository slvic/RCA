package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeLogLine(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   "failed to connect to 192.168.1.1:5432",
			want: "failed to connect to <ip>:<N>",
		},
		{
			in:   "request id=550e8400-e29b-41d4-a716-446655440000 failed after 3 retries",
			want: "request id=<uuid> failed after <N> retries",
		},
		{
			in:   "OOM killed pid 12345",
			want: "OOM killed pid <N>",
		},
	}

	for _, tc := range cases {
		got := normalizeLogLine(tc.in)
		assert.Equal(t, tc.want, got, "input: %s", tc.in)
	}
}

func TestClusterLogs_GroupsByTemplate(t *testing.T) {
	lines := []logLine{
		{timestamp: "14:00:01", message: "context deadline exceeded: conn 192.168.0.1"},
		{timestamp: "14:00:02", message: "context deadline exceeded: conn 10.0.0.2"},
		{timestamp: "14:00:03", message: "failed to acquire lock on table orders id=42"},
		{timestamp: "14:00:04", message: "context deadline exceeded: conn 172.16.0.1"},
	}

	clusters := clusterLogs(lines, 10)
	assert.Len(t, clusters, 2)
	// Most frequent first.
	assert.Equal(t, 3, clusters[0].count)
	assert.Equal(t, 1, clusters[1].count)
}

func TestClusterLogs_RespectsTopN(t *testing.T) {
	lines := make([]logLine, 20)
	for i := range lines {
		lines[i] = logLine{timestamp: "t", message: "error type " + string(rune('A'+i))}
	}
	clusters := clusterLogs(lines, 5)
	assert.Len(t, clusters, 5)
}

func TestZScore(t *testing.T) {
	assert.InDelta(t, 0.0, zScore(10, 10, 0), 0.001, "zero stddev returns 0")
	assert.InDelta(t, 2.0, zScore(12, 10, 1), 0.001)
	assert.InDelta(t, -2.0, zScore(8, 10, 1), 0.001)
}

func TestClassifySeverity(t *testing.T) {
	assert.Equal(t, "CRITICAL", classifySeverity(7))
	assert.Equal(t, "CRITICAL", classifySeverity(-6))
	assert.Equal(t, "WARNING", classifySeverity(5))
	assert.Equal(t, "INFO", classifySeverity(3.5))
}
