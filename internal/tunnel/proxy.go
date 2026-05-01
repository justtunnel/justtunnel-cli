package tunnel

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"syscall"
	"time"

	"github.com/justtunnel/justtunnel-cli/internal/display"
)

const maxBodySize = 10 << 20 // 10 MB

// DefaultLocalTimeout is the per-request timeout when proxying to the local
// target. Was 120s; trimmed to 30s so a slow/dead local target doesn't pin
// goroutines + memory on the server side for two whole minutes per request.
const DefaultLocalTimeout = 30 * time.Second

// ProxyRequest forwards a RequestFrame to the local target server and returns
// the corresponding ResponseFrame. Pass timeout=0 for the package default.
func ProxyRequest(ctx context.Context, frame RequestFrame, target string, timeout time.Duration, logger *slog.Logger) (ResponseFrame, error) {
	var bodyReader io.Reader
	var bodyBytes []byte
	if frame.Body != "" {
		var err error
		bodyBytes, err = base64.StdEncoding.DecodeString(frame.Body)
		if err != nil {
			return ResponseFrame{}, fmt.Errorf("decode request body: %w", err)
		}
		if len(bodyBytes) > maxBodySize {
			return ResponseFrame{}, fmt.Errorf("request body exceeds %d bytes", maxBodySize)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	url := target + frame.Path

	req, err := http.NewRequestWithContext(ctx, frame.Method, url, bodyReader)
	if err != nil {
		return ResponseFrame{}, fmt.Errorf("create request: %w", err)
	}
	for k, v := range frame.Headers {
		req.Header[k] = v
	}

	if logger.Enabled(ctx, slog.LevelDebug) {
		display.LogRequestDetail("Request", frame.Headers, bodyBytes)
	}

	if timeout <= 0 {
		timeout = DefaultLocalTimeout
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return ResponseFrame{}, fmt.Errorf("%s is not reachable", target)
		}
		return ResponseFrame{}, fmt.Errorf("proxy request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	if err != nil {
		return ResponseFrame{}, fmt.Errorf("read response body: %w", err)
	}
	if len(respBody) > maxBodySize {
		return ResponseFrame{}, fmt.Errorf("response body exceeds %d bytes", maxBodySize)
	}

	if logger.Enabled(ctx, slog.LevelDebug) {
		display.LogRequestDetail("Response", resp.Header, respBody)
	}

	return ResponseFrame{
		Type:    "response",
		ID:      frame.ID,
		Status:  resp.StatusCode,
		Headers: resp.Header,
		Body:    base64.StdEncoding.EncodeToString(respBody),
	}, nil
}
