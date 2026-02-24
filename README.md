# Outline CLI with WebSocket Support

Command-line client for Outline VPN with support for Shadowsocks-over-WebSocket protocol.

## Features

- Supports standard `ss://` keys
- Supports WebSocket YAML format (Outline dynamic keys)
- WS (WebSocket) and WSS (WebSocket Secure) support
- SOCKS5 proxy server
- Systemd integration
- Optional transparent proxy via iptables

## Installation

```bash
# Download and run installer
curl -sSL https://raw.githubusercontent.com/balookrd/outline-cli-ws/main/scripts/install.sh | sudo bash
