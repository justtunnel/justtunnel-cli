# justtunnel CLI

Command-line tool that exposes a local HTTP server to the public internet via a WebSocket tunnel to the justtunnel relay server.

## Prerequisites

- Go 1.22+

## Quick Start

```bash
# Build
go build -o justtunnel .

# Expose localhost:3000 (connects to justtunnel.dev by default)
./justtunnel 3000

# Output:
# justtunnel connected
# https://a3kx9m.justtunnel.dev → http://localhost:3000
#
# GET  /api/users         200  12ms
# POST /webhooks/stripe   200  45ms
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
justtunnel 3000 --domain tunnel.myco.com # custom domain (Team)
justtunnel 3000 --log-level debug        # verbose logging
```

**Flags:**

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--subdomain` | `-s` | — | Request a specific reserved subdomain |
| `--domain` | `-d` | — | Request a specific custom domain |
| `--log-level` | — | `info` | `debug`, `info`, `warn`, `error` |
| `--config` | — | `~/.config/justtunnel/config.yaml` | Config file path |

`--subdomain` and `--domain` are mutually exclusive.

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
  display/display.go            Terminal output (banner, request logs)
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
