package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"nhooyr.io/websocket"

	"github.com/justtunnel/justtunnel-cli/internal/display"
)

type Tunnel struct {
	serverURL   string
	localTarget string
	authToken   string
	logger      *slog.Logger

	conn   *websocket.Conn
	connMu sync.Mutex // protects WebSocket writes
	wg     sync.WaitGroup

	subdomain string
	tunnelURL string
}

func New(serverURL, localTarget, authToken string, logger *slog.Logger) *Tunnel {
	return &Tunnel{
		serverURL:   serverURL,
		localTarget: localTarget,
		authToken:   authToken,
		logger:      logger,
	}
}

// Run is the main lifecycle: connect, read loop, reconnect on failure.
func (t *Tunnel) Run(ctx context.Context) error {
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
	opts := &websocket.DialOptions{}
	if t.authToken != "" {
		opts.HTTPHeader = http.Header{
			"Authorization": []string{"Bearer " + t.authToken},
		}
	}

	conn, _, err := websocket.Dial(ctx, t.serverURL, opts)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

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

	display.PrintBanner(assigned.Subdomain, assigned.URL, t.localTarget)

	t.startHeartbeat(ctx)

	return nil
}

func (t *Tunnel) startHeartbeat(ctx context.Context) {
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := t.conn.Ping(ctx); err != nil {
					t.logger.Warn("heartbeat ping failed", "error", err)
					return
				}
			}
		}
	}()
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
			Message: err.Error(),
		}
		if writeErr := t.writeJSON(ctx, errFrame); writeErr != nil {
			t.logger.Error("write error frame failed", "error", writeErr)
		}
		display.LogRequest(frame.Method, frame.Path, 502, latency)
		return
	}

	if err := t.writeJSON(ctx, resp); err != nil {
		t.logger.Error("write response failed", "id", frame.ID, "error", err)
		return
	}

	display.LogRequest(frame.Method, frame.Path, resp.Status, latency)
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

// reconnect attempts to re-establish the WebSocket connection with
// exponential backoff: 1s, 2s, 4s, 8s, 16s, capped at 30s.
func (t *Tunnel) reconnect(ctx context.Context) error {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for attempt := 1; ; attempt++ {
		display.LogReconnecting(attempt, backoff)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		if err := t.connect(ctx); err != nil {
			t.logger.Error("reconnect attempt failed", "attempt", attempt, "error", err)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		display.LogReconnected()
		return nil
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
