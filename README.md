# Outline WS Load-Balancing Client

Высокопроизводительный **Outline (Shadowsocks) клиент поверх WebSocket (ws/wss)**
с поддержкой:

* ✅ TCP (`CONNECT`)
* ✅ UDP (`UDP ASSOCIATE`)
* ✅ Балансировки между несколькими серверами
* ✅ Fastest-first + failover
* ✅ Sticky-routing (без дёрганья)
* ✅ Health-check (RTT + состояние)
* ✅ Warm-standby (предварительно прогретые соединения)

Работает с `outline-ss-server`, включая конфигурации с
`websocket-stream` (TCP) и `websocket-packet` (UDP).

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
  ├── Health Check (RTT EWMA)
  ├── Fastest-first + Sticky
  ├── Failover (runtime + HC)
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

# Возможности

## Балансировка

* Выбор **самого быстрого сервера** по EWMA RTT
* Hysteresis (не переключается при мелких колебаниях)
* Sticky TTL (держится за лучший сервер N секунд)
* Runtime failover (мгновенное переключение при ошибке)

## Health-check

* Периодический WebSocket handshake
* RTT измеряется и сглаживается
* Fail threshold / Success threshold
* Сервер автоматически возвращается в пул

## Warm-Standby

* Держит N лучших серверов с **прогретым TCP WebSocket**
* Failover без задержки на handshake
* Автоматический сброс при падении сервера

---

# Установка

Требуется Go 1.22+

```bash
git clone <repo>
cd outline-ws-lb
go mod tidy
```

---

# Запуск

```bash
go run . -c config.yaml
```

SOCKS5 по умолчанию:

```
127.0.0.1:1080
```

Проверка:

```bash
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
  timeout: "3s"
  fail_threshold: 2
  success_threshold: 1

selection:
  sticky_ttl: "60s"
  cooldown: "20s"
  min_switch: "20ms"
  warm_standby_n: 2
  warm_standby_interval: "2s"

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

| Параметр          | Описание                                    |
| ----------------- | ------------------------------------------- |
| interval          | Интервал проверки                           |
| timeout           | Таймаут WS handshake                        |
| fail_threshold    | После скольких ошибок сервер считается DOWN |
| success_threshold | Сколько успешных проверок нужно для UP      |

---

## selection

| Параметр              | Описание                                   |
| --------------------- | ------------------------------------------ |
| sticky_ttl            | Сколько держаться за fastest               |
| cooldown              | На сколько "наказать" упавший сервер       |
| min_switch            | Минимальное улучшение RTT для переключения |
| warm_standby_n        | Сколько серверов держать прогретыми        |
| warm_standby_interval | Как часто обновлять прогрев                |

---

## upstream

| Поле    | Описание                                        |
| ------- | ----------------------------------------------- |
| name    | Имя для логов                                   |
| weight  | Вес (1 = нормальный, 2 = предпочтение)          |
| tcp_wss | WebSocket endpoint для TCP (`websocket-stream`) |
| udp_wss | WebSocket endpoint для UDP (`websocket-packet`) |
| cipher  | AEAD cipher (например `chacha20-ietf-poly1305`) |
| secret  | Ключ Outline                                    |

---

# Как работает выбор сервера

## 1. Fastest-first

Score рассчитывается из:

```
RTT (EWMA)
+ штраф за failCount
+ штраф за stale health-check
- учёт веса
```

Выбирается сервер с минимальным score.

---

## 2. Sticky

После выбора:

```
sticky_until = now + sticky_ttl
```

Пока сервер healthy — клиент остаётся на нём.

---

## 3. Failover

Происходит немедленно при:

* Ошибке Dial WS
* Ошибке TCP туннеля
* Ошибке создания UDP ассоциации
* Health-check пометил DOWN

---

## 4. Warm-standby

Каждые `warm_standby_interval`:

* Выбираются N лучших серверов
* Если нет прогретого TCP WS — создаётся заранее
* При CONNECT используется готовое соединение

Это сокращает задержку первого запроса.

---

# Поддержка TCP и UDP

| SOCKS5 команда | Поддержка |
| -------------- | --------- |
| CONNECT        | Да        |
| UDP ASSOCIATE  | Да        |
| BIND           | Нет       |

UDP реализован через `websocket-packet` (1 WS binary message = 1 UDP датаграмма).

---

# Интеграция

Можно использовать:

* Браузер (SOCKS5 proxy)
* Системный proxy
* tun2socks
* OpenWRT
* Docker
* systemd сервис

---

# Пример systemd unit

```
[Unit]
Description=Outline WS LB Client
After=network.target

[Service]
ExecStart=/usr/local/bin/outline-ws-lb -c /etc/outline/config.yaml
Restart=always
User=nobody

[Install]
WantedBy=multi-user.target
```

---

# Производительность

* Минимальная задержка при failover
* RTT-based routing
* Нет активного “дёрганья”
* Поддержка десятков upstream без деградации

---

# Ограничения

* Не TUN/VPN драйвер (использует SOCKS5)
* Warm-standby реализован только для TCP
* Не поддерживает QUIC (только WebSocket)

---

# Рекомендации

* Использовать 2–3 сервера максимум
* Не делать слишком маленький `healthcheck.interval`
* Warm-standby 1–2 достаточно
* sticky_ttl 30–120s оптимально

---

# Отладка

В логах:

```
[HC] server-1 UP (rtt=42ms)
[HC] server-2 DOWN
```

При failover:

```
tcp tunnel err: ...
switching to next fastest
```

---

# Roadmap (опционально)

* HTTP API для статистики
* Метрики Prometheus
* Adaptive health-check
* QUIC транспорт
* Multi-path TCP

---

# Лицензия

MIT / по выбору автора проекта.

---

Если нужно — могу дополнительно написать:

* Dockerfile
* docker-compose
* OpenWRT init script
* встроенный web-dashboard
* Prometheus exporter
* или сделать из этого production-grade релиз с CI/CD
