package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"nhooyr.io/websocket"

	"github.com/justtunnel/justtunnel-cli/internal/display"
)

// ReconnectInfo contains details about a successful reconnection.
type ReconnectInfo struct {
	Subdomain         string
	PreviousSubdomain string
	TunnelURL         string
	LocalTarget       string
	SubdomainChanged  bool
	DowntimeDuration  time.Duration
}

type Callbacks struct {
	OnConnecting    func()
	OnConnected     func(subdomain, url, localTarget string, passwordProtected bool)
	OnRequest       func(method, path string, status int, latency time.Duration)
	OnReconnecting  func(attempt int, backoff time.Duration)
	OnReconnectWait func(attempt int, remaining time.Duration)
	OnReconnected   func(info ReconnectInfo)
	OnDisconnected  func(timestamp time.Time)
}

type Tunnel struct {
	serverURL   string
	localTarget string
	authToken   string
	logger      *slog.Logger
	callbacks   Callbacks

	conn   *websocket.Conn
	connMu sync.Mutex // protects conn field and WebSocket writes
	wg     sync.WaitGroup

	subdomain      string
	tunnelURL      string
	tunnelID       string
	reconnectToken string

	password          string
	passwordProtected bool // set from tunnel_assigned frame

	maxReconnectAttempts int
	reconnecting         bool
	disconnectedAt       time.Time
}

func New(serverURL, localTarget, authToken string, logger *slog.Logger, callbacks Callbacks) *Tunnel {
	return &Tunnel{
		serverURL:            serverURL,
		localTarget:          localTarget,
		authToken:            authToken,
		logger:               logger,
		callbacks:            callbacks,
		maxReconnectAttempts: 50,
	}
}

// SetMaxReconnectAttempts sets the maximum number of reconnection attempts.
// A value of 0 means unlimited attempts. Negative values are clamped to 0.
func (t *Tunnel) SetMaxReconnectAttempts(maxAttempts int) {
	if maxAttempts < 0 {
		maxAttempts = 0
	}
	t.maxReconnectAttempts = maxAttempts
}

// SetPassword sets the password that will be sent as an X-Tunnel-Password header
// when connecting to the server.
func (t *Tunnel) SetPassword(pw string) {
	t.password = pw
}

// PasswordProtected returns true if the server confirmed that the tunnel is
// password-protected (from the tunnel_assigned frame).
func (t *Tunnel) PasswordProtected() bool {
	return t.passwordProtected
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
			now := time.Now()
			t.disconnectedAt = now
			if t.callbacks.OnDisconnected != nil {
				t.callbacks.OnDisconnected(now)
			}
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
	if t.password != "" {
		if opts.HTTPHeader == nil {
			opts.HTTPHeader = http.Header{}
		}
		opts.HTTPHeader.Set("X-Tunnel-Password", t.password)
	}

	conn, httpResp, err := websocket.Dial(ctx, dialURL, opts)
	if err != nil {
		if httpResp != nil && (httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden) {
			return display.AuthError(fmt.Sprintf("server returned %d: %v", httpResp.StatusCode, err))
		}
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
	t.passwordProtected = assigned.PasswordProtected

	// Only fire OnConnected for the initial connection, not during reconnects.
	if !t.reconnecting && t.callbacks.OnConnected != nil {
		t.callbacks.OnConnected(assigned.Subdomain, assigned.URL, t.localTarget, t.passwordProtected)
	}

	return nil
}

func (t *Tunnel) readLoop(ctx context.Context) error {
	for {
		t.connMu.Lock()
		activeConn := t.conn
		t.connMu.Unlock()

		_, data, err := activeConn.Read(ctx)
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

	// Capture the active connection so we can detect if it changed during proxying.
	t.connMu.Lock()
	activeConn := t.conn
	t.connMu.Unlock()

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
		if writeErr := t.writeJSONTo(ctx, activeConn, errFrame); writeErr != nil {
			t.logger.Error("write error frame failed", "error", writeErr)
		}
		if t.callbacks.OnRequest != nil {
			t.callbacks.OnRequest(frame.Method, frame.Path, 502, latency)
		}
		return
	}

	if err := t.writeJSONTo(ctx, activeConn, resp); err != nil {
		t.logger.Error("write response failed", "id", frame.ID, "error", err)
		return
	}

	if t.callbacks.OnRequest != nil {
		t.callbacks.OnRequest(frame.Method, frame.Path, resp.Status, latency)
	}
}

// writeJSONTo writes JSON to the specified connection, but only if it is still
// the active connection. This prevents stale responses from being written to a
// new connection after a reconnect.
func (t *Tunnel) writeJSONTo(ctx context.Context, targetConn *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	t.connMu.Lock()
	defer t.connMu.Unlock()
	if targetConn != t.conn {
		t.logger.Warn("skipping write to stale connection")
		return fmt.Errorf("connection replaced during request handling")
	}
	return t.conn.Write(ctx, websocket.MessageText, data)
}

// isAuthError checks if an error is an authentication/authorization failure
// by checking if it wraps a display.CLIError with CategoryAuth.
func isAuthError(err error) bool {
	var cliErr *display.CLIError
	return errors.As(err, &cliErr) && cliErr.Category == display.CategoryAuth
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

	// Close old connection before attempting to dial a new one.
	t.connMu.Lock()
	if t.conn != nil {
		t.conn.Close(websocket.StatusAbnormalClosure, "reconnecting")
	}
	t.connMu.Unlock()

	t.reconnecting = true
	previousSubdomain := t.subdomain

	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for attempt := 1; ; attempt++ {
		// Check max reconnect attempts (0 = unlimited).
		if t.maxReconnectAttempts > 0 && attempt > t.maxReconnectAttempts {
			elapsed := time.Since(t.disconnectedAt).Round(time.Second)
			return display.NetworkError(fmt.Sprintf(
				"gave up reconnecting after %d attempts (disconnected for %s). Check your internet connection and restart the tunnel.",
				attempt-1, elapsed,
			))
		}

		if t.callbacks.OnReconnecting != nil {
			t.callbacks.OnReconnecting(attempt, backoff)
		}

		if err := t.waitWithCountdown(ctx, attempt, backoff); err != nil {
			return err
		}

		reconnectURL := t.buildReconnectURL()
		if err := t.connectWithURL(ctx, reconnectURL); err != nil {
			t.logger.Error("reconnect attempt failed", "attempt", attempt, "error", err)

			// Don't retry on auth errors — credentials won't change between attempts.
			if isAuthError(err) {
				return display.AuthError("authentication failed during reconnect - run 'justtunnel auth' to re-authenticate")
			}

			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		t.reconnecting = false

		if t.callbacks.OnReconnected != nil {
			info := ReconnectInfo{
				Subdomain:         t.subdomain,
				PreviousSubdomain: previousSubdomain,
				TunnelURL:         t.tunnelURL,
				LocalTarget:       t.localTarget,
				SubdomainChanged:  t.subdomain != previousSubdomain,
				DowntimeDuration:  time.Since(t.disconnectedAt),
			}
			t.callbacks.OnReconnected(info)
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
