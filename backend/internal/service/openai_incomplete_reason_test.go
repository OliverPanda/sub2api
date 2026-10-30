package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeOpenAIIncompleteEventAddsDefaultReason(t *testing.T) {
	got, changed := normalizeOpenAIIncompleteEvent([]byte(`{"type":"response.incomplete","response":{"status":"incomplete"}}`), "response.incomplete")
	require.True(t, changed)
	require.JSONEq(t, `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, string(got))
}

func TestNormalizeOpenAIIncompleteEventPreservesExplicitReason(t *testing.T) {
	raw := []byte(`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`)
	got, changed := normalizeOpenAIIncompleteEvent(raw, "response.incomplete")
	require.False(t, changed)
	require.Equal(t, string(raw), string(got))
}

func TestExtractCodexFinalResponseAcceptsIncompleteEvent(t *testing.T) {
	got, ok := extractCodexFinalResponse("data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n\n")
	require.True(t, ok)
	require.JSONEq(t, `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, string(got))
}
