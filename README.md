# Outline WS Load-Balancing Client

Высокопроизводительный **Outline (Shadowsocks) клиент поверх WebSocket (ws/wss)**
с интеллектуальной балансировкой и активной проверкой качества каналов.

Поддерживает:

* ✅ TCP (`CONNECT`)
* ✅ UDP (`UDP ASSOCIATE`)
* ✅ Fastest-first + sticky
* ✅ Runtime failover
* ✅ Warm-standby (прогретые TCP WebSocket)
* ✅ Adaptive health-check
* ✅ Separate TCP/UDP health-check
* ✅ Active quality probe (реальная проверка трафика)

Работает с `outline-ss-server` (websocket-stream + websocket-packet).

---

# Архитектура

```
Client Apps
     │
     ▼
Local SOCKS5 (127.0.0.1:1080)
     │
     ▼
Load Balancer
  ├── Separate TCP/UDP HC
  ├── Adaptive Scheduler
  ├── Active Quality Probe
  ├── Fastest-first + Sticky
  ├── Runtime Failover
  └── Warm-standby (TCP WS)
     │
     ▼
Shadowsocks AEAD
     │
     ▼
WebSocket (wss)
     │
     ▼
Outline Server
```

---

# Основные возможности

## 1. Fastest-First + Sticky

* Выбор сервера с минимальным EWMA RTT
* Hysteresis (min_switch)
* Sticky TTL — не дёргается при мелких колебаниях
* Failover при runtime-ошибке

---

## 2. Separate TCP / UDP Health Check

TCP и UDP endpoints проверяются **независимо**:

* TCP может быть UP, UDP DOWN
* UDP может быть UP, TCP DOWN
* Балансировка TCP и UDP происходит отдельно

---

## 3. Adaptive Health-Check

Интервал проверок динамически меняется:

* DOWN → проверки чаще (min_interval)
* стабильный UP → проверки реже (до max_interval)
* backoff_factor предотвращает DDOS самого сервера
* jitter предотвращает синхронные всплески

---

## 4. Active Quality Probe

Проверяется не просто WS handshake, а **реальный трафик**:

### TCP Quality Probe

```
wss → Shadowsocks → example.com:80 → HEAD /
```

Считается успешным, если получен `HTTP/` ответ.

### UDP Quality Probe (DNS)

```
wss(packet) → Shadowsocks UDP → 1.1.1.1:53 → DNS A example.com
```

Считается успешным при получении корректного DNS ответа.

Это гарантирует:

* шифрование работает
* сервер реально маршрутизирует трафик
* обратный канал работает

---

## 5. Warm-Standby

* Держит N лучших серверов с уже открытым TCP WebSocket
* Новый CONNECT не ждёт handshake
* Failover происходит почти мгновенно

---

# Установка

Требуется Go 1.22+

```
git clone <repo>
cd outline-ws-lb
go mod tidy
```

---

# Запуск

```
go run . -c config.yaml
```

SOCKS5 по умолчанию:

```
127.0.0.1:1080
```

Проверка:

```
curl -x socks5h://127.0.0.1:1080 https://ifconfig.me
```

---

# Конфигурация

## config.yaml

```yaml
listen:
  socks5: "127.0.0.1:1080"

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

selection:
  sticky_ttl: "60s"
  cooldown: "20s"
  min_switch: "20ms"
  warm_standby_n: 2
  warm_standby_interval: "2s"

probe:
  enable_tcp: true
  enable_udp: true
  timeout: "2s"
  tcp_target: "example.com:80"
  udp_target: "1.1.1.1:53"
  dns_name: "example.com"

upstreams:
  - name: "server-1"
    weight: 1
    tcp_wss: "wss://example.com/TCP_PATH"
    udp_wss: "wss://example.com/UDP_PATH"
    cipher: "chacha20-ietf-poly1305"
    secret: "YOUR_SECRET"

  - name: "server-2"
    weight: 1
    tcp_wss: "wss://example2.com/TCP_PATH"
    udp_wss: "wss://example2.com/UDP_PATH"
    cipher: "chacha20-ietf-poly1305"
    secret: "YOUR_SECRET"
```

---

# Параметры

## healthcheck

| Параметр          | Описание                     |
| ----------------- | ---------------------------- |
| interval          | Базовый интервал             |
| min_interval      | Минимальный (DOWN)           |
| max_interval      | Максимальный (стабильный UP) |
| backoff_factor    | Рост интервала при фейлах    |
| rtt_scale         | Влияние RTT на частоту       |
| jitter            | Рандомизация                 |
| fail_threshold    | DOWN после N ошибок          |
| success_threshold | UP после N успехов           |

---

## selection

| Параметр              | Описание                                 |
| --------------------- | ---------------------------------------- |
| sticky_ttl            | Сколько держаться за fastest             |
| cooldown              | Наказание упавшего                       |
| min_switch            | Минимальный выигрыш RTT для переключения |
| warm_standby_n        | Сколько серверов греть                   |
| warm_standby_interval | Частота прогрева                         |

---

## probe

| Параметр   | Описание                   |
| ---------- | -------------------------- |
| enable_tcp | Включить TCP quality probe |
| enable_udp | Включить UDP DNS probe     |
| tcp_target | Цель HTTP probe            |
| udp_target | DNS сервер                 |
| dns_name   | DNS имя для запроса        |
| timeout    | Таймаут probe              |

---

# Поведение при сбоях

| Событие               | Реакция              |
| --------------------- | -------------------- |
| TCP dial error        | Немедленный failover |
| UDP association error | Немедленный failover |
| Active probe fail     | Сервер DOWN          |
| Health-check fail     | Сервер DOWN          |
| Сервер восстановился  | Возвращается в пул   |

---

# Производительность

* Failover без ожидания HC
* Warm-standby устраняет WS latency
* RTT-based routing
* Separate TCP/UDP logic
* Adaptive probing

Подходит для:

* Desktop
* Серверов
* Docker
* OpenWRT
* Cloud failover
* High-availability setups

---

# Ограничения

* Не TUN/VPN (использует SOCKS5)
* UDP probe использует DNS (можно заменить на echo-сервис)
* Warm-standby только для TCP

---

# Рекомендуемые настройки

| Параметр       | Рекомендация |
| -------------- | ------------ |
| sticky_ttl     | 30–120s      |
| warm_standby_n | 1–2          |
| min_interval   | 1–2s         |
| max_interval   | 20–60s       |
| backoff_factor | 1.5–2.0      |

---

# Возможные улучшения

* Prometheus exporter
* Web dashboard
* QUIC transport
* Multipath routing
* Latency histogram
* Active bandwidth probe

---

# Лицензия

GNU GENERAL PUBLIC LICENSE Version 3
