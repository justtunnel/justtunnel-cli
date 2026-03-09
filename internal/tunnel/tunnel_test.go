package tunnel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestTunnelEndToEnd(t *testing.T) {
	// Local HTTP server that responds to /healthz
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer httpServer.Close()

	responseCh := make(chan ResponseFrame, 1)
	serverDone := make(chan struct{})

	// Mock WebSocket relay server
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(serverDone)

		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		ctx := r.Context()

		// Send tunnel_assigned
		assigned := TunnelAssigned{
			Type:      "tunnel_assigned",
			TunnelID:  "test-tunnel-id",
			Subdomain: "test-sub",
			URL:       "https://test-sub.justtunnel.dev",
		}
		data, _ := json.Marshal(assigned)
		if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
			t.Errorf("write tunnel_assigned: %v", err)
			return
		}

		// Send request frame for GET /healthz
		reqFrame := RequestFrame{
			Type:    "request",
			ID:      "req-1",
			Method:  "GET",
			Path:    "/healthz",
			Headers: map[string][]string{},
			Body:    "",
		}
		data, _ = json.Marshal(reqFrame)
		if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
			t.Errorf("write request frame: %v", err)
			return
		}

		// Read response frame
		_, respData, err := conn.Read(ctx)
		if err != nil {
			t.Errorf("read response: %v", err)
			return
		}

		var resp ResponseFrame
		if err := json.Unmarshal(respData, &resp); err != nil {
			t.Errorf("unmarshal response: %v", err)
			return
		}
		responseCh <- resp
	}))
	defer wsServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tun := New(wsURL, httpServer.URL, "", logger, Callbacks{})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- tun.Run(ctx)
	}()

	select {
	case resp := <-responseCh:
		if resp.Status != http.StatusOK {
			t.Errorf("expected status 200, got %d", resp.Status)
		}
		if resp.ID != "req-1" {
			t.Errorf("expected id req-1, got %s", resp.ID)
		}
		body, err := base64.StdEncoding.DecodeString(resp.Body)
		if err != nil {
			t.Fatalf("decode response body: %v", err)
		}
		if string(body) != "ok" {
			t.Errorf("expected body %q, got %q", "ok", string(body))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for response frame")
	}

	cancel()
	tun.Shutdown(2 * time.Second)

	// Wait for server handler to finish so t.Errorf calls are safe
	<-serverDone
}
