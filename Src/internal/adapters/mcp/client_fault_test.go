package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConnectTimeoutIsRedactedAndClassified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	health, err := Connect(ctx, `{"transport":"streamable_http","url":"`+server.URL+`"}`, "")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || health.LastError != "MCP connection timed out" {
		t.Fatalf("health=%#v err=%v", health, err)
	}
}

func TestConnectRejectsInvalidToolInputSchema(t *testing.T) {
	server := newMCPFaultFixture(t, func(request rpcRequest) map[string]any {
		if request.Method == "tools/list" {
			return map[string]any{"tools": []map[string]any{{"name": "bad", "inputSchema": map[string]any{"type": "string"}}}}
		}
		return nil
	})
	defer server.Close()
	if _, err := Connect(context.Background(), `{"transport":"streamable_http","url":"`+server.URL+`"}`, ""); err == nil || !strings.Contains(err.Error(), "must describe an object") {
		t.Fatalf("invalid tool schema err=%v", err)
	}
}

func TestCallRejectsOversizedMCPOutput(t *testing.T) {
	server := newMCPFaultFixture(t, func(request rpcRequest) map[string]any {
		if request.Method == "tools/call" {
			return map[string]any{"content": []map[string]any{{"type": "text", "text": strings.Repeat("x", (1<<20)+1)}}}
		}
		return nil
	})
	defer server.Close()
	_, err := Call(context.Background(), `{"transport":"streamable_http","url":"`+server.URL+`"}`, "", "large", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "exceeds the 1 MiB safety limit") {
		t.Fatalf("oversized output err=%v", err)
	}
}

func TestConnectReportsCrashedOrMissingStdioExecutable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := Connect(ctx, `{"transport":"stdio","command":"oktapod-missing-mcp-fixture"}`, ""); err == nil {
		t.Fatal("missing stdio MCP executable connected")
	}
}

func newMCPFaultFixture(t *testing.T, result func(rpcRequest) map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "server/discover":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fault-fixture", "version": "1.0.0"}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result(request)})
		}
	}))
}
