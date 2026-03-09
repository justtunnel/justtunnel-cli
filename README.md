# justtunnel CLI

Command-line tool that exposes a local HTTP server to the public internet via a WebSocket tunnel to the justtunnel relay server.

## Prerequisites

- Go 1.24+

## Quick Start

```bash
# Build
go build -o justtunnel .

# Expose localhost:3000 (connects to justtunnel.dev by default)
./justtunnel 3000

# Output (with color in a real terminal):
#    _         _  _                       _
#   (_)_  _ __| || |_ _  _ _ _  _ _  ___| |
#   | | || (_-<  _|  _| || | ' \| ' \/ -_) |
#  _/ |\_,_/__/\__|\__|\_,_|_||_|_||_\___|_|
# |__/
#
#   Forwarding:    https://a3kx9m.justtunnel.dev -> http://localhost:3000
#   Subdomain:     a3kx9m
#
#   GET     /api/users                     200  12ms
#   POST    /webhooks/stripe               200  45ms
```

## Local Development (with local server)

```bash
# Start the justtunnel server locally first (see ../justtunnel-server/README.md)
# Then point the CLI at it:
JUSTTUNNEL_SERVER_URL=ws://localhost:8080/ws go run . 3000
```

## Commands

### `justtunnel <port>`

Expose `localhost:<port>` to the internet.

```bash
justtunnel 3000                           # random subdomain
justtunnel 3000 --subdomain myapp        # reserved subdomain (Pro)
justtunnel 3000 --log-level debug        # verbose logging
```

**Flags:**

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--subdomain` | `-s` | — | Request a specific reserved subdomain |
| `--log-level` | — | `info` | `debug`, `info`, `warn`, `error` |
| `--config` | — | `~/.config/justtunnel/config.yaml` | Config file path |

### `justtunnel auth <key>`

Save your API key locally. Validates the key against the server first.

```bash
justtunnel auth justtunnel_abc123def456...
# Authenticated as matt@example.com (pro).
```

### `justtunnel status`

Show your account info and active tunnels.

```bash
justtunnel status
# Email: matt@example.com
# Plan:  pro
# Active tunnels: 2
```

### `justtunnel logout`

Remove your saved API key.

```bash
justtunnel logout
# Logged out. API key removed from config.
```

### `justtunnel version`

Print version, commit, and build date.

## Configuration

Config file: `~/.config/justtunnel/config.yaml`

```yaml
auth_token: "justtunnel_sk_live_abc123..."
server_url: "wss://api.justtunnel.dev/ws"
log_level: "info"
```

All fields can be overridden with environment variables (prefix `JUSTTUNNEL_`):

| Env Variable | Config Key | Default |
|---|---|---|
| `JUSTTUNNEL_AUTH_TOKEN` | `auth_token` | — |
| `JUSTTUNNEL_SERVER_URL` | `server_url` | `wss://api.justtunnel.dev/ws` |
| `JUSTTUNNEL_LOG_LEVEL` | `log_level` | `info` |

## Terminal Output

The CLI uses colored output, animated spinners, and an ASCII banner when running in an interactive terminal.

**Color-coded request logs:** 2xx responses are green, 3xx/4xx are yellow, 5xx are red.

**Spinners** show progress during connection, reconnection (with countdown), and device auth flows. In non-TTY environments (e.g., piped output, CI), spinners are replaced with a single static line.

**Disabling color:**

- Set `NO_COLOR=1` (or any value) to disable all ANSI color output. This follows the [no-color.org](https://no-color.org) convention and is handled automatically by the `fatih/color` library.
- When stdout/stderr is not a TTY (e.g., `justtunnel 3000 2>log.txt`), spinners and the ASCII banner are automatically suppressed. Plain text output is used instead.

**Structured errors:** Errors are categorized for clearer messaging:

| Category | Prefix shown | Auto-suggestion |
|----------|-------------|-----------------|
| Network | `Connection error` | Check your internet connection and try again. |
| Auth | `Auth error` | Run \`justtunnel auth\` to re-authenticate. |
| Input | `Error` | _(none)_ |
| Server | `Server error` | Try again, or check https://status.justtunnel.dev |

## Tests

```bash
go test ./...            # run all tests
go test -race ./...      # with race detector
```

## Project Structure

```
main.go                         Entry point
cmd/
  root.go                       Root command (tunnel creation)
  auth.go                       justtunnel auth <key>
  status.go                     justtunnel status
  logout.go                     justtunnel logout
  version.go                    justtunnel version
internal/
  config/config.go              Viper-based config (YAML + env)
  tunnel/
    tunnel.go                   WebSocket connect, proxy loop, reconnect
    frames.go                   JSON frame types (request, response, etc.)
    proxy.go                    Forward request frames to localhost
  display/
    display.go                  Request logging and output helpers
    banner.go                   ASCII art banner on tunnel start
    color.go                    Color primitives, TTY detection
    errors.go                   Structured error types and printing
    spinner.go                  Animated spinner (TTY-aware)
  version/version.go            Build-time version variables
```

## How It Works

1. CLI dials a WebSocket to the relay server (`wss://api.justtunnel.dev/ws`)
2. Server assigns a subdomain and sends a `tunnel_assigned` frame
3. When someone hits `https://<subdomain>.justtunnel.dev`, the server wraps the HTTP request in a JSON frame and sends it over the WebSocket
4. CLI receives the frame, forwards the request to `localhost:<port>`, and sends the response back
5. Server writes the response to the original caller
6. Heartbeat pings every 30s keep the connection alive
7. On disconnect, the CLI reconnects with exponential backoff (1s → 30s cap)
