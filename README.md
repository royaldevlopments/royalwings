# RoyalWings

Game server control daemon for [Royal Panel](https://github.com/royaldevlopments/panel). Manages Docker containers, server lifecycle, file uploads, backups, and WebSocket streams via a HTTP API.

## Features

- **Server Management** — Create, start, stop, restart, and delete game servers
- **Docker Integration** — Isolated containers with resource limits (CPU, memory, disk)
- **Installation Scripts** — Automated server setup via Panel-defined egg configurations
- **SFTP Server** — Built-in SFTP for file transfers, authenticates via Panel
- **WebSocket Console** — Real-time server console output and command input
- **Backup & Transfer** — Server backups and cross-node transfers
- **File Management** — Browse, upload, download, edit server files
- **Crash Detection** — Automatic restart with configurable timeout
- **Resource Monitoring** — CPU, memory, disk usage tracking per server

## Requirements

- Linux (x86_64 or arm64)
- Docker
- Royal Panel instance

## Quick Start

```bash
# Install binary
sudo cp royalwings /usr/local/bin/

# Create config
sudo mkdir -p /etc/royalwings
sudo royalwings configure --panel https://panel.example.com

# Start service
sudo systemctl enable --now royalwings
```

## Configuration

Config file: `/etc/royalwings/config.yml`

Key settings:
- `token_id` / `token` — Authentication credentials from Panel node settings
- `remote` — Panel URL
- `api.host` / `api.port` — Daemon listen address (use `127.0.0.1:8080` behind nginx)
- `docker.network` — Docker network settings for containers

## API

All API endpoints require `Authorization: Bearer <token>` header.

| Endpoint | Method | Description |
|---|---|---|
| `/api/system` | GET | System information |
| `/api/servers` | GET | List all servers |
| `/api/servers` | POST | Create a new server |
| `/api/servers/:uuid/power` | POST | Send power action (start/stop/restart/kill) |
| `/api/servers/:uuid/ws` | GET | WebSocket console |

## License

MIT
