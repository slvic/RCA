package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseReactResponse_ToolCall(t *testing.T) {
	raw := `{"type": "tool_call", "tool": "detect_anomalies", "reason": "Check golden signals first"}`
	resp, err := parseReactResponse(raw)
	require.NoError(t, err)
	assert.Equal(t, "tool_call", resp.Type)
	assert.Equal(t, "detect_anomalies", resp.Tool)
	assert.Equal(t, "Check golden signals first", resp.Reason)
}

func TestParseReactResponse_FinalAnswer(t *testing.T) {
	raw := `{
		"type": "final_answer",
		"root_cause": "PostgreSQL connection pool exhausted",
		"confidence": 0.9,
		"evidence": [{"description": "error rate 4.5%", "source": "prometheus"}],
		"next_steps": ["Check pg_stat_activity"],
		"timeline": []
	}`
	resp, err := parseReactResponse(raw)
	require.NoError(t, err)
	assert.Equal(t, "final_answer", resp.Type)
	assert.Equal(t, "PostgreSQL connection pool exhausted", resp.RootCause)
	assert.InDelta(t, 0.9, resp.Confidence, 0.001)
	assert.Len(t, resp.Evidence, 1)
}

func TestParseReactResponse_MarkdownFences(t *testing.T) {
	raw := "```json\n{\"type\":\"tool_call\",\"tool\":\"get_error_summary\",\"reason\":\"look at logs\"}\n```"
	resp, err := parseReactResponse(raw)
	require.NoError(t, err)
	assert.Equal(t, "tool_call", resp.Type)
	assert.Equal(t, "get_error_summary", resp.Tool)
}

func TestParseReactResponse_InvalidJSON(t *testing.T) {
	_, err := parseReactResponse("not json at all")
	require.Error(t, err)
}

func TestParseReactResponse_UnknownType(t *testing.T) {
	_, err := parseReactResponse(`{"type": "something_else", "tool": "foo"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected type")
}

func TestTrimHistory_NoTruncation(t *testing.T) {
	short := "step 1\nstep 2"
	assert.Equal(t, short, trimHistory(short))
}

func TestTrimHistory_TruncatesHead(t *testing.T) {
	// Build a string longer than reactMaxHistoryChars.
	long := strings.Repeat("x", reactMaxHistoryChars+500)
	result := trimHistory(long)
	assert.LessOrEqual(t, len(result), reactMaxHistoryChars+len("...[earlier steps trimmed]\n"))
	assert.Contains(t, result, "...[earlier steps trimmed]")
	// Tail should be preserved.
	assert.True(t, strings.HasSuffix(result, strings.Repeat("x", reactMaxHistoryChars)))
}

func TestReactSystemPromptContainsTools(t *testing.T) {
	for _, tool := range []string{
		"detect_anomalies",
		"get_error_summary",
		"correlate_events",
		"get_slow_traces",
		"compare_profiles",
	} {
		assert.Contains(t, reactSystemPrompt, tool, "system prompt must list tool %s", tool)
	}
}
