```markdown
# Outline WS Load-Balancing Client

High-performance **Outline (Shadowsocks) client over WebSocket (ws/wss)**  
with intelligent load balancing, active health probing, IPv6, fwmark policy routing and optional full-system TUN mode.

---

## ✨ Features

- ✅ SOCKS5 proxy (CONNECT + UDP ASSOCIATE)
- ✅ TCP + UDP over WebSocket (wss)
- ✅ Fastest-first load balancing
- ✅ Sticky routing + hysteresis
- ✅ Runtime failover (instant switch on error)
- ✅ Adaptive health-check scheduler
- ✅ Separate TCP / UDP health states
- ✅ Active quality probe (real traffic test)
- ✅ IPv4 + IPv6 (dual stack)
- ✅ fwmark (SO_MARK) for policy routing (Linux)
- ✅ Embedded tun2socks (optional full-tunnel mode)
- ✅ Warm-standby WebSocket connections

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
└── Warm-standby (TCP)
│
▼
Shadowsocks AEAD
│
▼
WebSocket (wss)
│
▼
Outline Servers

```

Optional TUN mode:

```

System Traffic
│
▼
TUN (tun0)
│
▼
Embedded tun2socks
│
▼
SOCKS5 → Load Balancer → WSS → Internet

````

---

# Installation

Requires Go 1.22+

```bash
git clone <repo>
cd outline-ws-lb
go mod tidy
go build -o outline-ws-lb
````

---

# Basic Usage

```bash
./outline-ws-lb -c config.yaml
```

Default SOCKS5:

```
127.0.0.1:1080
```

Test:

```bash
curl -x socks5h://127.0.0.1:1080 https://ifconfig.me
```

---

# Configuration

## Minimal config.yaml

```yaml
listen:
  socks5: "127.0.0.1:1080"

upstreams:
  - name: "server-1"
    weight: 1
    tcp_wss: "wss://example.com/TCP_PATH"
    udp_wss: "wss://example.com/UDP_PATH"
    cipher: "chacha20-ietf-poly1305"
    secret: "YOUR_SECRET"
```

---

# Load Balancing

## Fastest-First

Server score is calculated from:

* EWMA RTT
* failure penalties
* staleness penalties
* weight

Lowest score wins.

## Sticky Routing

After selection:

```
sticky_until = now + sticky_ttl
```

Server won't change unless:

* it becomes unhealthy
* a significantly faster server appears (min_switch)

---

# Health Check (Adaptive)

Each upstream has **separate TCP and UDP health states**.

Health interval dynamically adjusts:

| State     | Interval           |
| --------- | ------------------ |
| DOWN      | min_interval       |
| unstable  | moderate           |
| stable UP | up to max_interval |

Supports jitter and exponential backoff.

---

# Active Quality Probe

Instead of checking only WebSocket handshake, real traffic is tested.

## TCP Probe

```
WSS → Shadowsocks → example.com:80 → HEAD /
```

Success if response starts with `HTTP/`.

## UDP Probe (DNS)

```
WSS(packet) → Shadowsocks UDP → 1.1.1.1:53
```

Sends DNS query (A or AAAA).

Config:

```yaml
probe:
  enable_tcp: true
  enable_udp: true
  timeout: "2s"
  tcp_target: "example.com:80"
  udp_target: "1.1.1.1:53"
  dns_name: "example.com"
  dns_type: "A"   # or AAAA
```

---

# IPv6 Support

Fully dual-stack:

* IPv6 SOCKS clients
* IPv6 upstream servers
* IPv6 DNS probe
* IPv6 TUN mode

Example upstream:

```yaml
tcp_wss: "wss://[2001:db8::1]:443/TCP_PATH"
udp_wss: "wss://[2001:db8::1]:443/UDP_PATH"
```

Example IPv6 DNS probe:

```yaml
probe:
  udp_target: "[2606:4700:4700::1111]:53"
  dns_type: "AAAA"
```

---

# fwmark (Linux Policy Routing)

All outgoing upstream connections can be marked with SO_MARK.

```yaml
fwmark: 123
```

Linux routing:

```bash
ip route add default via <GW> dev <DEV> table 100
ip rule add fwmark 123 lookup 100
```

Prevents routing loops when using TUN.

Requires:

* Linux
* CAP_NET_ADMIN or root

---

# TUN Mode (Full System Tunnel)

Embedded tun2socks engine.

## Config

```yaml
tun:
  enable: true
  auto: false
  device: "tun0"
  mtu: 1500
  interface: "eth0"
  loglevel: "info"
```

---

## tun.auto Modes

| enable | auto  | Behavior                     |
| ------ | ----- | ---------------------------- |
| false  | any   | TUN disabled                 |
| true   | false | Expect existing tun0         |
| true   | true  | Attempt to create/manage TUN |

Default:

```
tun.auto = false
```

Safe production behavior.

---

## Startup Logs

Example:

```
TUN mode enabled (auto=false), expecting existing interface "tun0"
TUN engine started (device=tun0, mtu=1500, out-if=eth0, fwmark=123)
```

---

## Linux Setup Example

Create TUN:

```bash
ip tuntap add dev tun0 mode tun
ip addr add 198.18.0.1/15 dev tun0
ip link set tun0 up
```

Redirect traffic:

```bash
ip route replace default dev tun0
```

Policy routing (recommended):

```bash
ip route add default via <GW> dev <DEV> table 100
ip rule add fwmark 123 lookup 100
```

---

# Warm-Standby

Keeps N fastest TCP WebSocket connections pre-opened.

```yaml
selection:
  warm_standby_n: 2
  warm_standby_interval: "2s"
```

Improves failover speed.

---

# Adaptive Health Config

```yaml
healthcheck:
  interval: "5s"
  min_interval: "1s"
  max_interval: "30s"
  jitter: "200ms"
  backoff_factor: 1.6
  rtt_scale: 0.25
  timeout: "3s"
  fail_threshold: 2
  success_threshold: 1
```

---

# Docker

Build:

```bash
docker build -t outline-ws-lb .
```

Run:

```bash
docker run \
  --cap-add NET_ADMIN \
  --device /dev/net/tun \
  -p 127.0.0.1:1080:1080 \
  -v ./config.yaml:/etc/outline/config.yaml:ro \
  outline-ws-lb
```

---

# Recommended Production Setup

```yaml
fwmark: 123

tun:
  enable: true
  auto: false
  device: tun0
  interface: eth0
```

With Linux policy routing via fwmark.

---

# Performance Characteristics

* Instant failover
* No server flapping
* Separate TCP/UDP scoring
* Low RTT jitter
* Full dual-stack
* Supports system-wide tunnel

---

# Limitations

* Linux required for fwmark and TUN
* TUN mode requires root or CAP_NET_ADMIN
* No built-in GUI
* Not a kernel VPN driver (userspace tun2socks)

---

# Roadmap Ideas

* Prometheus metrics
* HTTP stats API
* QUIC transport
* Multi-path routing
* Per-country routing
* Bandwidth-aware balancing

---

# License

  GNU GENERAL PUBLIC LICENSE Version 3
