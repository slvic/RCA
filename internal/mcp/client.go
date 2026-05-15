package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/slvic/rca-agent/internal/config"
)

// Client is a thin MCP client that dispatches JSON-RPC calls to configured MCP servers.
type Client struct {
	servers map[string]config.MCPServerConfig
	http    *http.Client
	logger  *slog.Logger
}

func NewClient(servers map[string]config.MCPServerConfig, logger *slog.Logger) *Client {
	return &Client{
		servers: servers,
		http:    &http.Client{Timeout: 60 * time.Second},
		logger:  logger,
	}
}

// mcpRequest is a JSON-RPC 2.0 tools/call request.
type mcpRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int            `json:"id"`
	Method  string         `json:"method"`
	Params  mcpCallParams  `json:"params"`
}

type mcpCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type mcpResponse struct {
	Result *mcpResult `json:"result,omitempty"`
	Error  *mcpError  `json:"error,omitempty"`
}

type mcpResult struct {
	Content []mcpContent `json:"content"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// call invokes a tool on the named MCP server and returns the text response.
func (c *Client) call(ctx context.Context, serverName, toolName string, args map[string]any) (string, error) {
	srv, ok := c.servers[serverName]
	if !ok {
		return "", fmt.Errorf("unknown MCP server %q", serverName)
	}

	// Apply per-server timeout on top of the passed context.
	callCtx, cancel := context.WithTimeout(ctx, srv.Timeout)
	defer cancel()

	req := mcpRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params: mcpCallParams{
			Name:      toolName,
			Arguments: args,
		},
	}

	var resp mcpResponse
	if err := doJSON(callCtx, c.http, srv.URL+"/rpc", req, &resp); err != nil {
		return "", fmt.Errorf("mcp %s/%s: %w", serverName, toolName, err)
	}
	if resp.Error != nil {
		return "", fmt.Errorf("mcp %s/%s error %d: %s", serverName, toolName, resp.Error.Code, resp.Error.Message)
	}
	if resp.Result == nil || len(resp.Result.Content) == 0 {
		return "", fmt.Errorf("mcp %s/%s: empty result", serverName, toolName)
	}

	var texts []string
	for _, c := range resp.Result.Content {
		if c.Type == "text" {
			texts = append(texts, c.Text)
		}
	}
	if len(texts) == 0 {
		return "", fmt.Errorf("mcp %s/%s: no text content in result", serverName, toolName)
	}
	return texts[0], nil
}

// callJSON calls an MCP tool and JSON-decodes the result into dest.
func (c *Client) callJSON(ctx context.Context, serverName, toolName string, args map[string]any, dest any) error {
	text, err := c.call(ctx, serverName, toolName, args)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(text), dest); err != nil {
		return fmt.Errorf("decode mcp response: %w (raw: %.200s)", err, text)
	}
	return nil
}
