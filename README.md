# stalkerhek

[![Docker Pulls](https://img.shields.io/docker/pulls/kidpoleon/stalkerhek)](https://hub.docker.com/r/kidpoleon/stalkerhek)
[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![GitHub Stars](https://img.shields.io/github/stars/kidpoleon/stalkerhek?style=social)](https://github.com/kidpoleon/stalkerhek)

> **Turn Stalker IPTV portals into local streaming endpoints**

## 📋 Table of Contents
- [🔗 Upstream / Origins](#-upstream--origins)
- [📝 General Description](#-general-description)
- [🎥 Video Tutorial](#-video-tutorial)
- [🚀 Installation](#-installation)
  - [Direct Go Installation](#-direct-go-installation)
  - [Docker Hub](#-docker-hub)
  - [Docker Compose](#-docker-compose)
- [📺 Usage Guide](#-usage-guide)
  - [Accessing WebUI](#accessing-webui)
  - [Adding Credentials](#adding-credentials)
  - [Using HLS Playlists](#using-hls-playlists)
- [❓ Troubleshooting & FAQ](#-troubleshooting--faq)
- [🔌 Ports & Networking](#-ports--networking)
- [🔄 Updating](#-updating)
- [🙏 Credits & License](#-credits--license)

## 🔗 Upstream / Origins
This project is based on and inspired by these repositories:
- [erkexzcx/stalkerhek](https://github.com/erkexzcx/stalkerhek)
- [CrazeeGhost/stalkerhek](https://github.com/CrazeeGhost/stalkerhek)
- [rabilrbl/stalkerhek](https://github.com/rabilrbl/stalkerhek)

## 📝 General Description
`stalkerhek` is a lightweight Go application that transforms Stalker IPTV portal accounts into local streaming endpoints. It provides:

- Multiple profile support
- Web-based management interface
- HLS playlists for media players
- STB-style proxy for compatible clients
- Persistent configuration

## 🎥 Video Tutorial
[![Watch the tutorial](https://i.ibb.co/9kXc1pCk/Untitled-design.png)](https://youtu.be/KVAyiSZ6zSo)

## 🚀 Installation

### 🖥️ Direct Go Installation
```bash
# 1. Install Go 1.21+ from https://go.dev/dl/

# 2. Clone and run
git clone https://github.com/kidpoleon/stalkerhek
cd stalkerhek
go run cmd/stalkerhek/main.go
```

### 🐳 Docker Hub
```bash
# Create data directory
mkdir -p ~/stalkerhek/data

# Run container
docker run -d \
  --name stalkerhek \
  --network host \
  -v ~/stalkerhek/data:/data \
  -e STALKERHEK_PROFILES_FILE=/data/profiles.json \
  kidpoleon/stalkerhek:main
```

### 🐋 Docker Compose
1. Create `docker-compose.yml`:
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
2. Start: `docker-compose up -d`

## 📺 Usage Guide

### Accessing WebUI
- Local: `http://localhost:4400`
- Remote: `http://<your-ip>:4400`

### Adding Credentials
1. Click "Add Profile"
2. Fill in:
   - **Portal URL**: `http://example.com/portal.php`
   - **MAC Address**: `00:1A:79:12:34:56`
   - **Ports**: Choose unique ports (e.g., 4600/4800)
3. Click "Save Profile"

### Using HLS Playlists
1. Find your HLS URL in the dashboard
2. Open in VLC or any IPTV player:
   ```
   http://<your-ip>:4600/
   ```

## ❓ Troubleshooting & FAQ

### Common Issues

#### WebUI Not Accessible
- Check if service is running
- Verify port 4400 is open
- Try `http://localhost:4400` first

#### Profile Won't Start
- Ensure portal URL ends with `portal.php`
- Verify MAC address format
- Check for port conflicts

### FAQ
**Q: Can I run multiple profiles?**  
A: Yes! Each profile runs in parallel with its own ports.

**Q: How do I update?**  
```bash
docker pull kidpoleon/stalkerhek:main
docker-compose down
docker-compose up -d
```

**Q: Where is my data stored?**  
A: In the `./data` directory (Docker) or current directory (direct run).

## 🔌 Ports & Networking
- **WebUI**: 4400
- **HLS Ports**: Configurable (e.g., 4600, 4601...)
- **Proxy Ports**: Configurable (e.g., 4800, 4801...)

## 🔄 Updating
### Docker Compose
```bash
docker-compose pull
docker-compose down
docker-compose up -d
```

### Direct Installation
```bash
cd /path/to/stalkerhek
git pull
go run cmd/stalkerhek/main.go
```

## 🙏 Credits & License
- **Author**: [kidpoleon](https://github.com/kidpoleon)
- **License**: MIT
