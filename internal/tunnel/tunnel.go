package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

type Callbacks struct {
	OnConnecting    func()
	OnConnected     func(subdomain, url, localTarget string)
	OnRequest       func(method, path string, status int, latency time.Duration)
	OnReconnecting  func(attempt int, backoff time.Duration)
	OnReconnectWait func(attempt int, remaining time.Duration)
	OnReconnected   func()
}

type Tunnel struct {
	serverURL   string
	localTarget string
	authToken   string
	logger      *slog.Logger
	callbacks   Callbacks

	conn   *websocket.Conn
	connMu sync.Mutex // protects WebSocket writes
	wg     sync.WaitGroup

	subdomain      string
	tunnelURL      string
	tunnelID       string
	reconnectToken string
}

func New(serverURL, localTarget, authToken string, logger *slog.Logger, callbacks Callbacks) *Tunnel {
	return &Tunnel{
		serverURL:   serverURL,
		localTarget: localTarget,
		authToken:   authToken,
		logger:      logger,
		callbacks:   callbacks,
	}
}

// Run is the main lifecycle: connect, read loop, reconnect on failure.
func (t *Tunnel) Run(ctx context.Context) error {
	if t.callbacks.OnConnecting != nil {
		t.callbacks.OnConnecting()
	}

	if err := t.connect(ctx); err != nil {
		return fmt.Errorf("initial connection: %w", err)
	}

	for {
		err := t.readLoop(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			t.logger.Error("connection lost", "error", err)
			if reconnErr := t.reconnect(ctx); reconnErr != nil {
				return reconnErr
			}
		}
	}
}

func (t *Tunnel) connect(ctx context.Context) error {
	return t.connectWithURL(ctx, t.serverURL)
}

func (t *Tunnel) connectWithURL(ctx context.Context, dialURL string) error {
	opts := &websocket.DialOptions{}
	if t.authToken != "" {
		opts.HTTPHeader = http.Header{
			"Authorization": []string{"Bearer " + t.authToken},
		}
	}

	conn, _, err := websocket.Dial(ctx, dialURL, opts)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	const maxBodySize = 10 << 20 // 10 MB
	bodyFloat := float64(maxBodySize) * 1.34
	readLimit := int64(bodyFloat) + 4096
	conn.SetReadLimit(readLimit)

	t.connMu.Lock()
	t.conn = conn
	t.connMu.Unlock()

	_, data, err := conn.Read(ctx)
	if err != nil {
		conn.Close(websocket.StatusAbnormalClosure, "")
		return fmt.Errorf("read tunnel assignment: %w", err)
	}

	frame, err := ParseFrame(data)
	if err != nil {
		conn.Close(websocket.StatusAbnormalClosure, "")
		return fmt.Errorf("parse tunnel assignment: %w", err)
	}

	assigned, ok := frame.(*TunnelAssigned)
	if !ok {
		conn.Close(websocket.StatusAbnormalClosure, "")
		return fmt.Errorf("expected tunnel_assigned frame, got %T", frame)
	}

	t.subdomain = assigned.Subdomain
	t.tunnelURL = assigned.URL
	t.tunnelID = assigned.TunnelID
	t.reconnectToken = assigned.ReconnectToken

	if t.callbacks.OnConnected != nil {
		t.callbacks.OnConnected(assigned.Subdomain, assigned.URL, t.localTarget)
	}

	return nil
}

func (t *Tunnel) readLoop(ctx context.Context) error {
	for {
		_, data, err := t.conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		frame, err := ParseFrame(data)
		if err != nil {
			t.logger.Error("parse frame failed", "error", err)
			continue
		}

		switch parsed := frame.(type) {
		case *RequestFrame:
			t.wg.Add(1)
			go t.handleRequest(ctx, parsed)
		case *ErrorFrame:
			t.logger.Error("server error", "message", parsed.Message)
		default:
			t.logger.Warn("unexpected frame type", "frame", fmt.Sprintf("%T", parsed))
		}
	}
}

func (t *Tunnel) handleRequest(ctx context.Context, frame *RequestFrame) {
	defer t.wg.Done()

	start := time.Now()
	resp, err := ProxyRequest(ctx, *frame, t.localTarget, t.logger)
	latency := time.Since(start)

	if err != nil {
		t.logger.Error("proxy failed", "id", frame.ID, "error", err)
		errFrame := ErrorFrame{
			Type:    "error",
			ID:      frame.ID,
			Message: "target unreachable",
		}
		if writeErr := t.writeJSON(ctx, errFrame); writeErr != nil {
			t.logger.Error("write error frame failed", "error", writeErr)
		}
		if t.callbacks.OnRequest != nil {
			t.callbacks.OnRequest(frame.Method, frame.Path, 502, latency)
		}
		return
	}

	if err := t.writeJSON(ctx, resp); err != nil {
		t.logger.Error("write response failed", "id", frame.ID, "error", err)
		return
	}

	if t.callbacks.OnRequest != nil {
		t.callbacks.OnRequest(frame.Method, frame.Path, resp.Status, latency)
	}
}

func (t *Tunnel) writeJSON(ctx context.Context, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	t.connMu.Lock()
	defer t.connMu.Unlock()
	return t.conn.Write(ctx, websocket.MessageText, data)
}

// buildReconnectURL appends reconnect token parameters to the server URL
// so the server can reuse the same subdomain on reconnection.
func (t *Tunnel) buildReconnectURL() string {
	if t.subdomain == "" || t.reconnectToken == "" || t.tunnelID == "" {
		return t.serverURL
	}
	parsed, err := url.Parse(t.serverURL)
	if err != nil {
		return t.serverURL
	}
	query := parsed.Query()
	query.Set("subdomain", t.subdomain)
	query.Set("tunnel_id", t.tunnelID)
	query.Set("reconnect_token", t.reconnectToken)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// reconnect attempts to re-establish the WebSocket connection with
// exponential backoff: 1s, 2s, 4s, 8s, 16s, capped at 30s.
func (t *Tunnel) reconnect(ctx context.Context) error {
	// Wait for in-flight requests from the old connection to finish
	// so they don't write stale responses to the new connection.
	drainDone := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(drainDone)
	}()
	select {
	case <-drainDone:
	case <-time.After(5 * time.Second):
		t.logger.Warn("timed out waiting for in-flight requests before reconnect")
	}

	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for attempt := 1; ; attempt++ {
		if t.callbacks.OnReconnecting != nil {
			t.callbacks.OnReconnecting(attempt, backoff)
		}

		if err := t.waitWithCountdown(ctx, attempt, backoff); err != nil {
			return err
		}

		reconnectURL := t.buildReconnectURL()
		if err := t.connectWithURL(ctx, reconnectURL); err != nil {
			t.logger.Error("reconnect attempt failed", "attempt", attempt, "error", err)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		if t.callbacks.OnReconnected != nil {
			t.callbacks.OnReconnected()
		}
		return nil
	}
}

// waitWithCountdown waits for the given backoff duration, calling OnReconnectWait
// every second with the remaining time.
func (t *Tunnel) waitWithCountdown(ctx context.Context, attempt int, backoff time.Duration) error {
	if t.callbacks.OnReconnectWait == nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
			return nil
		}
	}

	deadline := time.Now().Add(backoff)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}

		t.callbacks.OnReconnectWait(attempt, remaining)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Shutdown gracefully closes the WebSocket connection and waits for in-flight
// requests to complete, up to the given timeout.
func (t *Tunnel) Shutdown(timeout time.Duration) {
	t.connMu.Lock()
	conn := t.conn
	t.connMu.Unlock()

	if conn != nil {
		conn.Close(websocket.StatusNormalClosure, "shutting down")
	}

	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		t.logger.Warn("shutdown timed out waiting for in-flight requests")
	}
}
