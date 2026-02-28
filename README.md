# Outline WS Load-Balancing Client

High-performance **Outline (Shadowsocks) client over WebSocket**
with intelligent load balancing, active health probing, IPv6, fwmark policy routing, optional full-system TUN mode and
native **WebSocket over HTTP/2 (RFC 8441 Extended CONNECT)** support.

---

# ✨ Features

### Core Transport

* ✅ SOCKS5 proxy (CONNECT + UDP ASSOCIATE)
* ✅ TCP + UDP over WebSocket (wss)
* ✅ Native WebSocket over HTTP/2 (RFC 8441, Extended CONNECT)
* ✅ Native WebSocket over HTTP/3 (RFC 9220 Extended CONNECT) (`?h3=1`)
* ✅ `h2-only` strict mode (no fallback)
* ✅ Raw HTTP/2 framing (no net/http WS client)
* ✅ Proper half-close handling (TCP FIN / WS CLOSE / H2 END_STREAM)
* ✅ Stable TLS (no random `SSL_ERROR_SYSCALL`)
* ✅ Automatic reconnect on stream close / RST / network errors

---

### Load Balancing

* ✅ Fastest-first load balancing
* ✅ Sticky routing + hysteresis
* ✅ Runtime failover (instant switch on error)
* ✅ Warm-standby WebSocket connections
* ✅ Separate TCP / UDP health states

---

### Health & Probing

* ✅ Adaptive health-check scheduler
* ✅ Active quality probe (real traffic test)
* ✅ Separate TCP / UDP scoring
* ✅ RTT EWMA scoring
* ✅ Failure penalty model

---

### Networking

* ✅ IPv4 + IPv6 (dual stack)
* ✅ fwmark (SO_MARK) for policy routing (Linux)
* ✅ Embedded tun2socks (optional full-tunnel mode)

---

# Architecture

```
Applications
│
▼
SOCKS5 (127.0.0.1:1080)
│
▼
Load Balancer
├── TCP Health (adaptive)
├── UDP Health (adaptive)
├── Active Quality Probe
├── Fastest-first + Sticky
├── Runtime Failover
└── Warm-standby
│
▼
Shadowsocks AEAD
│
▼
WebSocket (RFC8441 / HTTP2 or classic)
│
▼
Outline Servers
```

---

# WebSocket Transport Modes

## 1️⃣ Classic WebSocket (HTTP/1.1 Upgrade)

Standard WSS handshake.

Used if:

* server does not support RFC 8441
* `h2` mode is not requested

---

## 2️⃣ WebSocket over HTTP/2 (RFC 8441)

Uses:

```
:method = CONNECT
:protocol = websocket
```

Flow:

1. TLS (ALPN=h2)
2. HTTP/2 preface
3. SETTINGS (ENABLE_CONNECT_PROTOCOL=1)
4. Extended CONNECT
5. WebSocket frames inside HTTP/2 DATA frames

No HTTP/1.1 upgrade involved.

---

## 3️⃣ WebSocket over HTTP/3 (RFC 9220)

Use when upstream supports HTTP/3 Extended CONNECT for WebSocket.

Examples:

```
wss://edge.example.com/tcp?h3=1
wss://edge.example.com/udp?http3=1
```

Behavior:

* Enables HTTP/3 dial path via URL flags (`h3`, `http3`, `quic`)
* Performs RFC 9220 Extended CONNECT (`:protocol = websocket`) over QUIC
* `h3=only` / `http3=only` enforces strict HTTP/3 mode (no fallback)
* If `h3=1` and HTTP/3 fails, client falls back to h2/http1 path

---

## 3️⃣ h2-only Mode

Strict HTTP/2 only:

```
wss://example.com/tcp?h2=only
```

Behavior:

* Fails fast if server does not advertise `SETTINGS_ENABLE_CONNECT_PROTOCOL`
* No HTTP/1.1 fallback
* Pure RFC8441 path

---

## Runtime Flag (if required)

Some Go builds gate Extended CONNECT:

```
GODEBUG=http2xconnect=1
```

---

# Debug Mode

Enable detailed transport logs:

```
OUTLINE_WS_DEBUG=1 ./outline-cli-ws -c config.yaml
```

Shows:

* TLS handshake
* ALPN
* HTTP/2 SETTINGS
* WINDOW_UPDATE
* HEADERS
* DATA frames
* WebSocket CLOSE codes
* Stream reopen events

---

# Installation

Requires:

* Go 1.25+ (Extended CONNECT support required)

```
git clone <repo>
cd outline-cli-ws
go mod tidy
go build -o outline-cli-ws ./cmd/outline-cli-ws
```

---

# Basic Usage

```
cp examples/config.example.yaml config.yaml
./outline-cli-ws -c config.yaml
```

Default SOCKS5:

```
127.0.0.1:1080
```

Test:

```
curl -x socks5h://127.0.0.1:1080 https://ifconfig.me
```

---

# Minimal Config

```yaml
listen:
  socks5: "127.0.0.1:1080"

upstreams:
  - name: "server-1"
    weight: 1
    tcp_wss: "wss://example.com/TCP_PATH?h2=only"
    udp_wss: "wss://example.com/UDP_PATH?h2=only"
    cipher: "chacha20-ietf-poly1305"
    secret: "YOUR_SECRET"
```

---

# Half-Close Handling (Important)

Correctly handles:

* Client TCP FIN
* Remote TCP FIN
* WebSocket CLOSE (1000)
* HTTP/2 END_STREAM
* Proper stream shutdown without RST

Prevents:

* TLS stalls
* random SSL_ERROR_SYSCALL
* hanging curl sessions
* half-open socket leaks

---

# Load Balancing

## Fastest-First

Score includes:

* EWMA RTT
* Failure penalties
* Staleness penalties
* Weight

Lowest score selected.

---

## Sticky Routing

Server stays selected until:

* unhealthy
* significantly slower than competitor

Prevents flapping.

---

# Adaptive Health Check

Dynamic intervals:

| State     | Interval           |
|-----------|--------------------|
| DOWN      | min_interval       |
| unstable  | medium             |
| stable UP | up to max_interval |

Supports jitter + exponential backoff.

---

# Active Quality Probe

### TCP Probe

```
WSS → Shadowsocks → target:80 → HEAD /
```

Success if response starts with `HTTP/`.

### UDP Probe

DNS query via upstream:

```
WSS → Shadowsocks UDP → DNS server
```

---

# IPv6 Support

* IPv6 SOCKS clients
* IPv6 upstreams
* IPv6 DNS probe
* IPv6 TUN mode

Example:

```yaml
tcp_wss: "wss://[2001:db8::1]:443/TCP_PATH?h2=only"
```

---

# fwmark (Linux)

Mark upstream sockets:

```yaml
fwmark: 123
```

Routing example:

```
ip rule add fwmark 123 lookup 100
ip route add default via <GW> dev <DEV> table 100
```

Prevents routing loops in TUN mode.

Requires:

* Linux
* CAP_NET_ADMIN

---

# TUN Mode

```yaml
tun:
  enable: true
  device: "tun0"
  mtu: 1500
```

---

# Warm-Standby

Keeps N TCP connections pre-opened:

```yaml
selection:
  warm_standby_n: 2
```

Instant failover without cold handshake.

---

# Performance Characteristics

* Instant failover
* No flapping
* Separate TCP/UDP scoring
* Stable TLS behavior
* Clean half-close
* Full dual-stack
* High concurrency safe
* No random RST on TLS

---

# Limitations

* Linux required for fwmark and TUN
* Root or CAP_NET_ADMIN needed for TUN
* No GUI
* No HTTP/3 (yet)

---

# License

GNU GENERAL PUBLIC LICENSE Version 3

---

## Unit tests

These tests can run **offline** using the `unit` build tag (it excludes external networking/TUN dependencies):

```bash
go test ./... -tags unit
```


## Prometheus metrics

Run with metrics enabled:

```bash
./outline-cli-ws -c config.yaml -metrics :9100
```

Then scrape `http://localhost:9100/metrics`.

## Grafana dashboard

Ready-to-import dashboard JSON is available in:

* `examples/outlinews_grafana_dashboard_plain.json`
* `examples/outlinews_grafana_dashboard_api.json`

Or run the full monitoring stack via Docker Compose (OutlineWS + Prometheus + Grafana):

```bash
docker compose up --build
```

Provisioned files are in `deploy/prometheus` and `deploy/grafana`.
