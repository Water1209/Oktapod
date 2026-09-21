package mcpregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/oktapod/oktapod/Src/internal/application"
)

const defaultEndpoint = "https://registry.modelcontextprotocol.io/v0.1/servers"

type Client struct {
	endpoint string
	http     *http.Client
}

func New(client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{endpoint: defaultEndpoint, http: client}
}

func NewWithEndpoint(endpoint string, client *http.Client) *Client {
	registry := New(client)
	registry.endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	return registry
}

func (c *Client) Search(ctx context.Context, query string, limit int) ([]application.MCPRegistryServer, error) {
	if limit < 1 || limit > 20 {
		limit = 8
	}
	target, err := url.Parse(c.endpoint)
	if err != nil || target.Scheme != "https" || target.Host == "" {
		return nil, fmt.Errorf("invalid MCP Registry endpoint")
	}
	values := target.Query()
	values.Set("search", strings.TrimSpace(query))
	values.Set("limit", fmt.Sprintf("%d", limit))
	target.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("MCP Registry returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Servers []struct {
			Server application.MCPRegistryServer `json:"server"`
		} `json:"servers"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode MCP Registry response: %w", err)
	}
	servers := make([]application.MCPRegistryServer, 0, len(payload.Servers))
	for _, item := range payload.Servers {
		servers = append(servers, item.Server)
	}
	return servers, nil
}
