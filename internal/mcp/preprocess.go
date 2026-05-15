package mcp

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

var (
	rUUID    = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	rNumbers = regexp.MustCompile(`\b\d+\b`)
	rIP      = regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`)
)

func normalizeLogLine(line string) string {
	line = rUUID.ReplaceAllString(line, "<uuid>")
	line = rIP.ReplaceAllString(line, "<ip>")
	line = rNumbers.ReplaceAllString(line, "<N>")
	return line
}

type logCluster struct {
	template  string
	count     int
	firstSeen string
	lastSeen  string
}

// clusterLogs groups raw log lines by normalized template, returns top-n by frequency.
func clusterLogs(lines []logLine, topN int) []logCluster {
	type entry struct {
		cluster logCluster
	}
	index := map[string]*logCluster{}

	for _, l := range lines {
		tmpl := normalizeLogLine(l.message)
		if c, ok := index[tmpl]; ok {
			c.count++
			c.lastSeen = l.timestamp
		} else {
			index[tmpl] = &logCluster{
				template:  tmpl,
				count:     1,
				firstSeen: l.timestamp,
				lastSeen:  l.timestamp,
			}
		}
	}

	clusters := make([]logCluster, 0, len(index))
	for _, c := range index {
		clusters = append(clusters, *c)
	}
	sort.Slice(clusters, func(i, j int) bool {
		return clusters[i].count > clusters[j].count
	})

	if topN > 0 && len(clusters) > topN {
		clusters = clusters[:topN]
	}
	return clusters
}

func formatLogClusters(clusters []logCluster) string {
	var sb strings.Builder
	for _, c := range clusters {
		fmt.Fprintf(&sb, "[%dx] %s (first: %s, last: %s)\n", c.count, c.template, c.firstSeen, c.lastSeen)
	}
	return sb.String()
}

func zScore(current, baselineMean, baselineStddev float64) float64 {
	if baselineStddev == 0 {
		return 0
	}
	return (current - baselineMean) / baselineStddev
}

type metricAnomaly struct {
	name     string
	current  float64
	mean     float64
	stddev   float64
	z        float64
	severity string
}

func classifySeverity(z float64) string {
	abs := math.Abs(z)
	switch {
	case abs >= 6:
		return "CRITICAL"
	case abs >= 4:
		return "WARNING"
	default:
		return "INFO"
	}
}

func formatAnomalies(anomalies []metricAnomaly) string {
	var sb strings.Builder
	for _, a := range anomalies {
		direction := ""
		if a.z < 0 {
			direction = " (dropping)"
		}
		fmt.Fprintf(&sb, "%s: current=%.2f baseline_mean=%.2f zscore=%.1f (%s)%s\n",
			a.name, a.current, a.mean, a.z, a.severity, direction)
	}
	return sb.String()
}
