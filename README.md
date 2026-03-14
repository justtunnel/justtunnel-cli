# justtunnel CLI

Command-line tool that exposes a local HTTP server to the public internet via a WebSocket tunnel to the justtunnel relay server.

## Install

**macOS / Linux:**

```bash
curl -fsSL https://justtunnel.dev/install | sh
```

**Homebrew:**

```bash
brew install justtunnel/tap/justtunnel
```

**Windows (PowerShell):**

```powershell
irm https://justtunnel.dev/install.ps1 | iex
```

Installs to `$env:LOCALAPPDATA\justtunnel\` and adds it to your user PATH.

Pre-built binaries are available for macOS (arm64, amd64), Linux (arm64, amd64), and Windows (amd64, arm64).

## Prerequisites

- Go 1.24+ (for building from source)

## Quick Start

```bash
# Build
go build -o justtunnel .

# Expose localhost:3000 — launches an interactive TUI
./justtunnel 3000

# Multiple tunnels from a config file
./justtunnel --config-file tunnels.yaml

# Both: config tunnels + an extra port
./justtunnel 3000 --config-file tunnels.yaml
```

## Local Development (with local server)

```bash
# Start the justtunnel server locally first (see ../justtunnel-server/README.md)
# Then point the CLI at it:
JUSTTUNNEL_SERVER_URL=ws://localhost:8080/ws go run . 3000
```

## Commands

### `justtunnel [port]`

Expose `localhost:<port>` to the internet. When running in a terminal, launches an interactive TUI that supports multiple tunnels.

```bash
justtunnel 3000                           # single tunnel, random subdomain
justtunnel 3000 --subdomain myapp        # reserved subdomain (Pro)
justtunnel --config-file tunnels.yaml    # multi-tunnel from config
justtunnel 3000 --config-file tunnels.yaml  # config + extra port
justtunnel 3000 --log-level debug        # verbose logging
```

**Flags:**

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--subdomain` | `-s` | — | Request a specific reserved subdomain |
| `--config-file` | — | — | YAML config file with tunnel definitions |
| `--log-level` | — | `info` | `debug`, `info`, `warn`, `error` |
| `--max-reconnect-attempts` | — | `50` | Max reconnection attempts before giving up (0 = unlimited) |
| `--config` | — | `~/.config/justtunnel/config.yaml` | Auth config file path |

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

### Auth config

`~/.config/justtunnel/config.yaml`

```yaml
auth_token: "justtunnel_sk_live_abc123..."
server_url: "wss://api.justtunnel.dev/ws"
log_level: "info"
max_reconnect_attempts: 50
```

All fields can be overridden with environment variables (prefix `JUSTTUNNEL_`):

| Env Variable | Config Key | Default |
|---|---|---|
| `JUSTTUNNEL_AUTH_TOKEN` | `auth_token` | — |
| `JUSTTUNNEL_SERVER_URL` | `server_url` | `wss://api.justtunnel.dev/ws` |
| `JUSTTUNNEL_LOG_LEVEL` | `log_level` | `info` |
| `JUSTTUNNEL_MAX_RECONNECT_ATTEMPTS` | `max_reconnect_attempts` | `50` |

### Tunnel config file

Used with `--config-file` to define multiple tunnels:

```yaml
tunnels:
  - port: 3000
    name: frontend
    subdomain: my-frontend
  - port: 8080
    name: api
  - port: 9090
```

Each tunnel entry supports:

| Field | Required | Description |
|-------|----------|-------------|
| `port` | yes | Local port to expose (1-65535) |
| `name` | no | Display name in the TUI |
| `subdomain` | no | Request a specific subdomain |

The config file is watched for changes — editing it while the TUI is running will automatically add or remove tunnels.

## Interactive TUI

When running in a terminal, the CLI launches a Bubble Tea TUI with a list/detail view for managing multiple tunnels.

**Slash commands** (type in the input bar at the bottom):

| Command | Description |
|---------|-------------|
| `/add <port>` | Add a tunnel. Options: `--name`, `--subdomain` |
| `/remove <index>` | Remove a tunnel by its list number |
| `/list` | Switch to list view |
| `/help` | Show available commands |
| `/quit` | Shut down all tunnels and exit |

**Keyboard shortcuts:**

| Key | Action |
|-----|--------|
| Up/Down | Navigate tunnel list |
| Enter | Detail view (or execute command) |
| Esc | Back to list view / clear input |
| Ctrl+C | Quit |

**Non-TTY mode:** When stdout is piped (e.g., `justtunnel 3000 | cat`), the CLI falls back to plain text output with one line per event.

## Terminal Output

**Color-coded request logs:** 2xx responses are green, 3xx/4xx are yellow, 5xx are red.

**Spinners** show progress during connection, reconnection (with countdown), and device auth flows. In non-TTY environments (e.g., piped output, CI), spinners are replaced with a single static line.

**Reconnection behavior:** When the WebSocket connection drops, the CLI:

1. Prints a timestamped "Disconnected" message
2. Retries with exponential backoff (1s, 2s, 4s, ... up to 30s cap), showing a countdown spinner
3. After reconnecting, prints a status block with the tunnel URL, forwarding target, and total downtime
4. Warns if the subdomain changed (free-tier tunnels may get a new random subdomain)
5. Gives up after 50 attempts by default (configurable via `--max-reconnect-attempts`, 0 = unlimited)
6. Exits immediately on auth errors (401/403) instead of retrying

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
  root.go                       Root command, TTY fork, TUI/non-TTY paths
  auth.go                       justtunnel auth <key>
  status.go                     justtunnel status
  logout.go                     justtunnel logout
  version.go                    justtunnel version
internal/
  config/config.go              Viper-based config (YAML + env)
  tui/
    model.go                    Bubble Tea model, key handling, command dispatch
    views.go                    List/detail view rendering, non-TTY formatting
    messages.go                 Bubble Tea message types
    commands.go                 Slash command parser (/add, /remove, etc.)
    manager.go                  TunnelManager: multi-tunnel coordination
    managed_tunnel.go           ManagedTunnel: lifecycle callbacks, state
    stats.go                    Per-tunnel request stats, rolling log
    plan.go                     Plan info fetch from /api/me
    config.go                   YAML tunnel config loading, validation, diff
    watcher.go                  Config file hot-reload with fsnotify
    errors.go                   Server error parsing (plan limits, auth)
  tunnel/
    tunnel.go                   WebSocket connect, proxy loop, reconnect
    frames.go                   JSON frame types (request, response, etc.)
    proxy.go                    Forward request frames to localhost
  display/
    display.go                  Request logging and output helpers
    banner.go                   ASCII art banner on tunnel start
    color.go                    Color primitives, TTY detection, C1 sanitization
    errors.go                   Structured error types and printing
    reconnect.go                Disconnection/reconnection status output
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
7. On disconnect, the CLI reconnects with exponential backoff (1s -> 30s cap), up to 50 attempts by default. Auth errors (401/403) exit immediately.
