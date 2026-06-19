package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"github.com/justtunnel/justtunnel-cli/internal/display"
)

// fakeRelay is a controllable WebSocket relay used to drive the CLI through
// disconnect -> reconnect cycles. Each incoming dial is recorded and dispatched
// to handleConn, which the test supplies to decide what the server does for
// that particular connection (e.g. accept then drop, or reject with 401).
type fakeRelay struct {
	server *httptest.Server

	mu              sync.Mutex
	dialCount       int
	rejectFrom      int  // dials at or after this index get rejectStatus (0 = never)
	rejectCode      int  // HTTP status used when rejecting before the WS upgrade
	dropAfterAssign bool // when accepting, drop the connection right after tunnel_assigned
}

func newFakeRelay(t *testing.T, relay *fakeRelay) string {
	t.Helper()
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		relay.mu.Lock()
		relay.dialCount++
		current := relay.dialCount
		rejectFrom := relay.rejectFrom
		rejectCode := relay.rejectCode
		dropAfterAssign := relay.dropAfterAssign
		relay.mu.Unlock()

		if rejectFrom > 0 && current >= rejectFrom {
			http.Error(writer, "rejected", rejectCode)
			return
		}

		conn, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}

		assigned := TunnelAssigned{
			Type:           "tunnel_assigned",
			TunnelID:       "test-tunnel-id",
			Subdomain:      "test-sub",
			URL:            "https://test-sub.justtunnel.dev",
			ReconnectToken: "reconnect-token",
		}
		data, _ := json.Marshal(assigned)
		if writeErr := conn.Write(request.Context(), websocket.MessageText, data); writeErr != nil {
			conn.Close(websocket.StatusAbnormalClosure, "")
			return
		}

		if dropAfterAssign {
			// Simulate the relay dropping the connection. The CLI's read
			// loop will see an abnormal closure and enter reconnect.
			conn.Close(websocket.StatusAbnormalClosure, "server dropping")
			return
		}

		// Hold the connection open until the client goes away.
		<-request.Context().Done()
		conn.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(httpServer.Close)
	relay.server = httpServer
	return "ws" + strings.TrimPrefix(httpServer.URL, "http")
}

func (relay *fakeRelay) dials() int {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	return relay.dialCount
}

// recordingSleep returns a sleep func that records every backoff duration it is
// asked to wait for and returns immediately (respecting ctx cancellation). The
// returned getter yields a copy of the recorded schedule.
func recordingSleep() (func(ctx context.Context, duration time.Duration) error, func() []time.Duration) {
	var mu sync.Mutex
	var schedule []time.Duration
	sleep := func(ctx context.Context, duration time.Duration) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		mu.Lock()
		schedule = append(schedule, duration)
		mu.Unlock()
		return nil
	}
	get := func() []time.Duration {
		mu.Lock()
		defer mu.Unlock()
		out := make([]time.Duration, len(schedule))
		copy(out, schedule)
		return out
	}
	return sleep, get
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestReconnectGivesUpAfterMaxAttempts drives a disconnect followed by a relay
// that rejects every reconnect, and asserts the CLI exhausts maxReconnectAttempts
// and returns a CategoryNetwork "gave up" error.
func TestReconnectGivesUpAfterMaxAttempts(t *testing.T) {
	relay := &fakeRelay{
		dropAfterAssign: true,
		rejectFrom:      2, // first dial accepted+dropped; reconnect dials rejected
		rejectCode:      http.StatusBadGateway,
	}
	wsURL := newFakeRelay(t, relay)

	sleep, _ := recordingSleep()
	tun := New(wsURL, "http://localhost:0", "", discardLogger(), Callbacks{})
	tun.sleep = sleep
	tun.SetMaxReconnectAttempts(3)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := tun.Run(ctx)
	if err == nil {
		t.Fatal("expected give-up error after exhausting reconnect attempts, got nil")
	}
	var cliErr *display.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("expected CLIError, got %T: %v", err, err)
	}
	if cliErr.Category != display.CategoryNetwork {
		t.Errorf("category: got %v, want CategoryNetwork", cliErr.Category)
	}
	if !strings.Contains(cliErr.Error(), "gave up reconnecting") {
		t.Errorf("message: got %q, want it to mention giving up", cliErr.Error())
	}
}

// TestReconnectBackoffSchedule asserts the exponential backoff sequence
// (1s, 2s, 4s, ...) capped at 30s by recording the durations the reconnect
// loop sleeps for before each retry dial.
func TestReconnectBackoffSchedule(t *testing.T) {
	relay := &fakeRelay{
		dropAfterAssign: true,
		rejectFrom:      2, // accept first dial then reject all reconnect dials
		rejectCode:      http.StatusBadGateway,
	}
	wsURL := newFakeRelay(t, relay)

	sleep, schedule := recordingSleep()
	tun := New(wsURL, "http://localhost:0", "", discardLogger(), Callbacks{})
	tun.sleep = sleep
	tun.SetMaxReconnectAttempts(8)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = tun.Run(ctx) // will give up; we only care about the backoff schedule

	want := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second, // 32s capped to 30s
		30 * time.Second,
		30 * time.Second,
	}
	got := schedule()
	if len(got) != len(want) {
		t.Fatalf("backoff schedule length: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("backoff[%d]: got %s, want %s (full schedule %v)", index, got[index], want[index], got)
		}
	}
}

// TestReconnectStopsOnAuthErrorMidReconnect asserts that a 401 returned by the
// relay during a reconnect attempt short-circuits the loop: no further attempts,
// and a CategoryAuth error is surfaced.
func TestReconnectStopsOnAuthErrorMidReconnect(t *testing.T) {
	relay := &fakeRelay{
		dropAfterAssign: true,
		rejectFrom:      2, // first dial accepted+dropped; reconnect dial returns 401
		rejectCode:      http.StatusUnauthorized,
	}
	wsURL := newFakeRelay(t, relay)

	sleep, _ := recordingSleep()
	tun := New(wsURL, "http://localhost:0", "", discardLogger(), Callbacks{})
	tun.sleep = sleep
	tun.SetMaxReconnectAttempts(10)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := tun.Run(ctx)
	if err == nil {
		t.Fatal("expected auth error on 401 mid-reconnect, got nil")
	}
	var cliErr *display.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("expected CLIError, got %T: %v", err, err)
	}
	if cliErr.Category != display.CategoryAuth {
		t.Errorf("category: got %v, want CategoryAuth", cliErr.Category)
	}
	// One initial dial + exactly one reconnect dial (the 401), then it stops.
	if dials := relay.dials(); dials != 2 {
		t.Errorf("dial count: got %d, want 2 (initial + one 401 reconnect, no further retries)", dials)
	}
}

// TestReconnectStopsOnForbiddenMidReconnect asserts a 403 during reconnect is
// also terminal: it surfaces a CategoryForbidden error and does not keep retrying.
func TestReconnectStopsOnForbiddenMidReconnect(t *testing.T) {
	relay := &fakeRelay{
		dropAfterAssign: true,
		rejectFrom:      2,
		rejectCode:      http.StatusForbidden,
	}
	wsURL := newFakeRelay(t, relay)

	sleep, _ := recordingSleep()
	tun := New(wsURL, "http://localhost:0", "", discardLogger(), Callbacks{})
	tun.sleep = sleep
	tun.SetMaxReconnectAttempts(10)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := tun.Run(ctx)
	if err == nil {
		t.Fatal("expected forbidden error on 403 mid-reconnect, got nil")
	}
	var cliErr *display.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("expected CLIError, got %T: %v", err, err)
	}
	if cliErr.Category != display.CategoryForbidden {
		t.Errorf("category: got %v, want CategoryForbidden", cliErr.Category)
	}
	if dials := relay.dials(); dials != 2 {
		t.Errorf("dial count: got %d, want 2 (initial + one 403 reconnect, no further retries)", dials)
	}
}

// TestReconnectSucceedsAfterDrop drives a full disconnect -> reconnect cycle
// where the relay accepts the reconnect, and asserts OnReconnected fires and
// the loop resumes without surfacing an error (until ctx is cancelled).
func TestReconnectSucceedsAfterDrop(t *testing.T) {
	var dialCount int
	var mu sync.Mutex
	reconnected := make(chan ReconnectInfo, 1)

	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		dialCount++
		current := dialCount
		mu.Unlock()

		conn, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}
		assigned := TunnelAssigned{
			Type:           "tunnel_assigned",
			TunnelID:       "test-tunnel-id",
			Subdomain:      "test-sub",
			URL:            "https://test-sub.justtunnel.dev",
			ReconnectToken: "reconnect-token",
		}
		data, _ := json.Marshal(assigned)
		conn.Write(request.Context(), websocket.MessageText, data)

		if current == 1 {
			// Drop the first connection to trigger a reconnect.
			conn.Close(websocket.StatusAbnormalClosure, "drop")
			return
		}
		// Second (reconnect) connection: hold it open.
		<-request.Context().Done()
		conn.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(httpServer.Close)
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http")

	sleep, schedule := recordingSleep()
	tun := New(wsURL, "http://localhost:0", "", discardLogger(), Callbacks{
		OnReconnected: func(info ReconnectInfo) {
			select {
			case reconnected <- info:
			default:
			}
		},
	})
	tun.sleep = sleep
	tun.SetMaxReconnectAttempts(5)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- tun.Run(ctx)
	}()

	select {
	case info := <-reconnected:
		if info.Subdomain != "test-sub" {
			t.Errorf("reconnect subdomain: got %q, want test-sub", info.Subdomain)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reconnect to succeed")
	}

	// Exactly one backoff wait happened before the successful reconnect.
	if got := schedule(); len(got) != 1 || got[0] != time.Second {
		t.Errorf("backoff before successful reconnect: got %v, want [1s]", got)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned unexpected error after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
