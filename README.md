# stalkerhek

[![Docker Pulls](https://img.shields.io/docker/pulls/kidpoleon/stalkerhek)](https://hub.docker.com/r/kidpoleon/stalkerhek)
[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![GitHub Stars](https://img.shields.io/github/stars/kidpoleon/stalkerhek?style=social)](https://github.com/kidpoleon/stalkerhek)

---

## Tutorial Video

[![Stalkerhek Tutorial](https://i.ibb.co/b53gtx6G/STALKERHEK-BANNER-3840x2160.png)](https://www.youtube.com/watch?v=7AvkvlGfv64)

*Click the image above to watch the tutorial video and get started with stalkerhek*

## Screenshots

<div align="center">
<table>
<tr>
<td align="center"><strong>Create Profile</strong></td>
<td align="center"><strong>Filter Management</strong></td>
</tr>
<tr>
<td><img src="https://i.ibb.co/67YdPgTz/create-page.png" alt="create page" width="100%"></td>
<td><img src="https://i.ibb.co/zTcQLk91/filter-page.png" alt="filter page" width="100%"></td>
</tr>
<tr>
<td align="center"><strong>Profile Management</strong></td>
<td align="center"><strong>Tuning Settings</strong></td>
</tr>
<tr>
<td><img src="https://i.ibb.co/zVNTtg2W/manage-page.png" alt="manage page" width="100%"></td>
<td><img src="https://i.ibb.co/8DVWCFJg/tuning-page.png" alt="tuning page" width="100%"></td>
</tr>
</table>
</div>

---

Turn Stalker IPTV portal accounts into local streaming endpoints.

You get:
- Multiple profiles
- Web UI for management
- HLS playlist endpoint (works great in VLC / IPTV players)
- Stalker-style proxy endpoint (for clients that expect STB-ish behavior)
- Per-profile filtering (Categories -> Genres -> Channels)

## Table of contents
- [What this is](#what-this-is)
- [Quick start](#quick-start)
- [Docker](#docker)
- [Ports](#ports)
- [Web UI usage](#web-ui-usage)
- [Filters (per-profile)](#filters-per-profile)
- [Advanced settings (stability)](#advanced-settings-stability)
- [Persistence (where data is stored)](#persistence-where-data-is-stored)
- [Troubleshooting](#troubleshooting)
- [Security notes](#security-notes)
- [Credits and license](#credits-and-license)

## What this is
`stalkerhek` is a single-binary Go application.

It authenticates to a Stalker portal (typically `portal.php`) using your profile credentials (MAC address, and internally MAG-style device identifiers), fetches your channel list, then exposes:
- An **HLS endpoint** your players can read
- A **Proxy endpoint** that mimics STB interactions for clients that need it

It also includes a Filters UI to safely enable/disable content per profile.

## Quick start

### 1) Run from source (Go 1.21+)
```bash
git clone https://github.com/kidpoleon/stalkerhek
cd stalkerhek
go run cmd/stalkerhek/main.go
```

Open:
- Web UI: `http://localhost:4400/dashboard`

### 2) Create a profile
In the Web UI:
- Click **Add Profile**
- Fill in:
  - **Portal URL**
  - **MAC address**
  - **HLS port** and **Proxy port** (must be unique per profile)
- Click **Save Profile**

The service will:
- Validate the portal URL + MAC format
- Authenticate
- Fetch channels
- Start HLS + Proxy for that profile

## Docker

### Docker run (host networking recommended)
```bash
mkdir -p ~/stalkerhek/data

docker run -d \
  --name stalkerhek \
  --network host \
  -v ~/stalkerhek/data:/data \
  -e STALKERHEK_PROFILES_FILE=/data/profiles.json \
  kidpoleon/stalkerhek:main
```

### Docker compose
Example `docker-compose.yml`:
```yaml
version: '3.8'
services:
  stalkerhek:
    image: kidpoleon/stalkerhek:main
    container_name: stalkerhek
    network_mode: host
    restart: unless-stopped
    environment:
      - STALKERHEK_PROFILES_FILE=/data/profiles.json
    volumes:
      - ./data:/data
```

Start:
```bash
docker-compose up -d
```

Update:
```bash
docker-compose pull
docker-compose up -d
```

## Ports

- **Web UI**: `4400`
- **HLS**: per profile (example `4600`, `4601`, ...)
- **Proxy**: per profile (example `4800`, `4801`, ...)

If something does not start:
- You likely have a **port conflict**
- Or your firewall blocks the port

## Web UI usage

### Dashboard
`/dashboard` is the main page.

You can:
- Create/edit/delete profiles
- Start/Stop profiles
- Copy HLS/Proxy URLs
- Open logs (`/logs`) for troubleshooting

### Logs
Open `/logs` to see live logs.

If a profile fails to start, logs usually show:
- wrong portal URL
- wrong MAC address
- portal handshake/auth issues

## Filters (per-profile)

Filters are designed for speed and safety.

Open from Dashboard:
- Click **Filters** on a profile

Flow:
1. **Categories** (derived grouping from portal genre names)
2. **Genres** (within a category)
3. **Channels** (within a genre)

You can:
- Bulk select and enable/disable
- Fine-tune individual channels

Keyboard shortcuts in Channels:
- Up/Down: move active row
- Enter: open details
- Space: toggle selection
- Esc: clear selection

Note: Filters UI is intentionally **desktop-focused**.

## Advanced settings (stability)

In the Dashboard "Advanced Settings":
- **Playlist delay (segments)**: adds latency but often reduces buffering
- **Upstream header timeout**: increase if the provider is slow
- **Max idle conns/host**: helps with multiple concurrent streams

If you experience buffering:
- Increase Playlist delay first
- Then increase Upstream header timeout

## Persistence (where data is stored)

Profiles and filters are persisted to JSON.

- `STALKERHEK_PROFILES_FILE` controls where `profiles.json` is stored.
- `filters.json` defaults to the **same directory** as `profiles.json` (e.g. `/data/filters.json` if profiles are `/data/profiles.json`).

For Docker:
- Mount a `/data` volume and set `STALKERHEK_PROFILES_FILE=/data/profiles.json`.

## Troubleshooting

### "Profile won't start"
Check these first:
- Portal URL must look like:
  - `http(s)://HOST/portal.php`
  - Some providers use `/stalker_portal/portal.php`
- MAC address must look like:
  - `00:1A:79:AA:BB:CC`
- Ports must be free

Open `/logs` and look for the first error.

### Web UI not reachable
- Confirm the process is running
- Confirm port `4400` is open
- Try `http://localhost:4400/dashboard`

### Playlist works but channels buffer
- Try increasing Playlist delay (segments)
- Increase Upstream header timeout
- If you have many devices, increase Max idle conns/host

## Security notes

- Do not expose this directly to the public internet.
- Use a private LAN, VPN, or reverse proxy with authentication.
- Profiles contain sensitive information (portal URL + MAC).

See `SECURITY.md` for details.

## Credits and license

Origins/inspiration:
- https://github.com/erkexzcx/stalkerhek
- https://github.com/CrazeeGhost/stalkerhek
- https://github.com/rabilrbl/stalkerhek

Author:
- https://github.com/kidpoleon

License: MIT
