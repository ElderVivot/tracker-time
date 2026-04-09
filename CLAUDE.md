# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Linux productivity monitoring daemon (Go) that tracks the active window and idle time, stores records in SQLite, and syncs them to a REST API. It auto-detects the graphical session type (X11 or Wayland/GNOME) and uses the appropriate provider.

The codebase is in **Portuguese (pt-BR)** — log messages, comments, and docs are written in Portuguese.

## Build & Run

```bash
# Build (CGO required for mattn/go-sqlite3)
CGO_ENABLED=1 go build -o tracker-time .

# Run
./tracker-time

# Run as systemd user service
cp tracker-time ~/.local/bin/
cp tracker-time.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now tracker-time
```

## Architecture

Two goroutines run concurrently:
- **Monitor loop** (every 2s): reads the active window + idle time via the provider, upserts records in SQLite.
- **Sync loop** (every 10min): POSTs all records to the ingest API, deletes them locally on 200 OK. Also enforces a TTL (default 7 days) to prevent unbounded table growth.

### Provider pattern

`providers.go` defines two interfaces — `WindowProvider` (active window) and `IdleProvider` (idle duration). Two implementations:

| Provider | Window source | Idle source | Files |
|---|---|---|---|
| X11 | `xgb` library (X properties) | `xprintidle` CLI | `provider_x11.go` |
| Wayland/GNOME | Custom GNOME Shell extension via D-Bus (`gdbus`) | Mutter IdleMonitor via D-Bus | `provider_wayland.go` |

Session detection uses `XDG_SESSION_TYPE` + `XDG_CURRENT_DESKTOP`. When running as a systemd service, env vars are auto-detected by scanning `/proc` for processes of the same UID (`detectEnvFromProc`).

### GNOME Shell extension (`gnome-extension/`)

A small GNOME Shell extension (`tracker-time@autmais`) that exposes the focused window's WM class and title over D-Bus. Required for Wayland since direct window access isn't available. Supports GNOME Shell 45-47.

### Configuration (environment variables)

| Variable | Purpose | Default |
|---|---|---|
| `TRACKER_DB_PATH` | SQLite file path | `~/.local/share/tracker-time/tracker.db` |
| `TRACKER_INGEST_URL` / `TRACKER_API_URL` | REST API endpoint | `https://api.dashboard.com/v1/ingest` |
| `TRACKER_IDLE_THRESHOLD` | Idle timeout (Go duration) | `2s` |
| `TRACKER_TTL` / `TRACKER_TTL_HOURS` | Record expiration | 7 days |

## Key Dependencies

- `github.com/mattn/go-sqlite3` — SQLite driver (requires CGO)
- `github.com/jezek/xgb` — X11 protocol (pure Go)
