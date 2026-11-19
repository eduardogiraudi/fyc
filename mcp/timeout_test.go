package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"fuckyourcli/types"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStartupTimeoutDoesNotDiscardHealthyServer(t *testing.T) {
	server := echoServer()
	healthy := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server {
		return server
	}, &sdk.StreamableHTTPOptions{JSONResponse: true}))
	defer healthy.Close()
	stalled := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		select {
		case <-req.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer stalled.Close()
	manager := NewMCPManager("test")
	defer manager.Shutdown(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := manager.Initialize(ctx, []types.MCPServerConfig{
		{Name: "stalled", Transport: "streamableHttp", Endpoint: stalled.URL},
		{Name: "healthy", Transport: "streamableHttp", Endpoint: healthy.URL},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected initialization deadline: %v", err)
	}
	listCtx, listCancel := context.WithTimeout(context.Background(), time.Second)
	defer listCancel()
	listed, err := manager.ListTools(listCtx)
	if err != nil || len(listed) != 1 || listed[0].Function.Name != "healthy__echo__path" {
		t.Fatalf("startup timeout hid healthy tools: %v, %v", listed, err)
	}
}
