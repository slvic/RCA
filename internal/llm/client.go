package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const anthropicAPI = "https://api.anthropic.com/v1/messages"

type Client struct {
	httpClient *http.Client
	apiKey     string
	model      string
}

func NewClient(apiKey, model string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 90 * time.Second},
		apiKey:     apiKey,
		model:      model,
	}
}

// Complete sends a single-turn request and returns (text, totalTokens, error).
func (c *Client) Complete(ctx context.Context, system, user string) (string, int, error) {
	text, tokens, err := c.complete(ctx, system, user)
	if err != nil {
		return "", 0, err
	}
	return text, tokens, nil
}

func (c *Client) complete(ctx context.Context, system, user string) (string, int, error) {
	req := Request{
		Model:     c.model,
		MaxTokens: 2048,
		System:    system,
		Messages:  []Message{{Role: "user", Content: user}},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", 0, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicAPI, bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", 0, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, respBody)
	}

	var result Response
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", 0, fmt.Errorf("decode response: %w", err)
	}

	if result.Error != nil {
		return "", 0, fmt.Errorf("anthropic error %s: %s", result.Error.Type, result.Error.Message)
	}

	if len(result.Content) == 0 {
		return "", 0, fmt.Errorf("empty response from LLM")
	}

	totalTokens := result.Usage.InputTokens + result.Usage.OutputTokens
	return result.Content[0].Text, totalTokens, nil
}
