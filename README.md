# kea-golang-manager

Go-менеджер для Kea DHCP через REST API Control Agent.  
Поддерживает **Active-Standby HA** (hot-standby), CLI и HTTP API.

Kea — единственный DHCP-движок и единственный источник конфигурации.  
Go не реализует протокол DHCP, выборы узлов, failover и не манипулирует lease-файлами.


## Архитектура

```
┌──────────────────────────────────────────────────────┐
│               Docker Compose (bridge)                │
│                                                      │
│  ┌─────────────────────┐  ┌─────────────────────┐    │
│  │    kea-primary      │  │    kea-standby      │    │
│  │  lease: memfile     │◄─►  lease: memfile     │    │
│  │  role: primary      │  │  role: standby      │    │
│  └──────────┬──────────┘  └──────────┬──────────┘    │
│      unix socket                unix socket          │
│  ┌──────────▼──────────┐  ┌──────────▼──────────┐    │
│  │  kea-ctrl-agent     │  │  kea-ctrl-agent     │    │
│  │  primary  :8001     │  │  standby  :8002     │    │
│  └──────────┬──────────┘  └──────────┬──────────┘    │
└─────────────┼────────────────────────┼───────────────┘
              │ HTTP                   │ HTTP
┌─────────────▼────────────────────────▼───────────────┐
│           kea-golang-manager  :8080                  │
│                                                      │
│  GET  /health        GET  /stats                     │
│  GET  /config        GET  /subnets                   │
│  POST /kea/reload    POST /subnets                   │
│  GET  /ha/status     DELETE /subnets/:id             │
└──────────────────────────────────────────────────────┘
```

### Стек

| Слой | Технология |
|---|---|
| DHCP-сервер | Kea DHCP 2.x |
| HA | hot-standby (Kea built-in) |
| Lease sync | libdhcp_ha.so + libdhcp_lease_cmds.so |
| Lease backend | memfile |
| Backend | Go 1.22, chi/v5 |
| Deployment | Docker Compose (bridge) |

### Структура пакетов

```
kea-golang-manager/
├── cmd/app/               # Точка входа: CLI + HTTP-сервер
├── internal/
│   ├── api/               # HTTP-обработчики (chi)
│   ├── kea/               # Клиент Kea Control Agent
│   ├── ha/                # HAManager: статус, выбор узла, guarded apply
│   └── service/           # Бизнес-логика (DHCPService)
├── pkg/config/            # Конфигурация (primary URL, standby URL, timeout)
├── config/
│   ├── kea-primary.conf
│   ├── kea-standby.conf
│   ├── kea-ctrl-agent-primary.conf
│   └── kea-ctrl-agent-standby.conf
├── docker-compose.yml
├── Makefile
└── README.md
```

## Требования

- Go 1.22+
- Docker 24+ и Docker Compose v2

---

## Быстрый старт

### 1. Запустить Kea HA-кластер

```bash
docker compose up -d
```

```bash
docker compose ps
docker compose logs kea-primary-ctrl-agent
docker compose logs kea-standby-ctrl-agent
```

Control Agent primary: `http://localhost:8001`  
Control Agent standby: `http://localhost:8002`

### 2. Собрать менеджер

```bash
make build
```

### 3. Запустить HTTP-сервер

```bash
./bin/kea-manager serve-http \
  -kea-primary-url=http://localhost:8001 \
  -kea-standby-url=http://localhost:8002
```

### 4. Проверить кластер

```bash
# Здоровье
curl -s http://localhost:8080/health | jq

# HA-статус
curl -s http://localhost:8080/ha/status | jq
```

Ожидаемый ответ после полного старта:

```json
{
  "primary": { "state": "hot-standby", "role": "primary", "reachable": true },
  "standby": { "state": "hot-standby", "role": "standby", "reachable": true }
}
```

---

## Конфигурация

### Флаги запуска

| Флаг | По умолчанию | Описание |
|---|---|---|
| `-kea-primary-url` | `http://localhost:8001` | URL primary Control Agent |
| `-kea-standby-url` | `http://localhost:8002` | URL standby Control Agent |
| `-timeout` | `10s` | Таймаут HTTP-запросов к Kea |
| `-http-addr` | `:8080` | Адрес HTTP-сервера |

### HA-конфигурация в Kea

Оба файла (`kea-primary.conf`, `kea-standby.conf`) должны содержать hooks:

```json
{
  "hooks-libraries": [
    { "library": "/usr/lib/kea/hooks/libdhcp_lease_cmds.so" },
    {
      "library": "/usr/lib/kea/hooks/libdhcp_ha.so",
      "parameters": {
        "high-availability": [{
          "this-server-name": "kea-primary",
          "mode": "hot-standby",
          "heartbeat-delay": 10000,
          "max-response-delay": 30000,
          "max-ack-delay": 5000,
          "max-unacked-clients": 5,
          "peers": [
            {
              "name": "kea-primary",
              "url": "http://kea-primary-ctrl-agent:8001",
              "role": "primary",
              "auto-failover": true
            },
            {
              "name": "kea-standby",
              "url": "http://kea-standby-ctrl-agent:8002",
              "role": "standby",
              "auto-failover": true
            }
          ]
        }]
      }
    }
  ]
}
```

## API

Все эндпоинты на `http://localhost:8080`.

### `GET /health`

```json
{
  "status": "ok",
  "kea": {
    "primary": { "reachable": true },
    "standby": { "reachable": true }
  }
}
```

`200` — OK, `503` — primary недоступен.

### `GET /ha/status`

```json
{
  "primary": { "state": "hot-standby", "role": "primary", "reachable": true },
  "standby": { "state": "hot-standby", "role": "standby", "reachable": true }
}
```

Возможные значения `state`: `hot-standby`, `partner-down`, `communication-recovery`, `syncing`, `waiting`, `ready`.

### `GET /config`

Конфигурация активного узла (`config-get`).

### `GET /stats`

```json
{
  "total-addresses": 191,
  "assigned-addresses": 12,
  "declined-addresses": 0
}
```

### `GET /subnets`

Список подсетей с активного узла.

### `POST /subnets`

```bash
curl -X POST http://localhost:8080/subnets \
  -H "Content-Type: application/json" \
  -d '{
    "subnet": "192.168.2.0/24",
    "pools": ["192.168.2.10-192.168.2.200"],
    "reservations": [
      { "hw-address": "aa:bb:cc:dd:ee:ff", "ip-address": "192.168.2.50", "hostname": "server1" }
    ]
  }'
```

- `201` — применено на оба узла
- `207` — применено только на активный (standby пропущен, см. тело ответа)
- `400` — ошибка валидации

### `DELETE /subnets/{id}`

- `204` — удалено на обоих
- `207` — удалено только на активном

### `POST /kea/reload`

Полная последовательность reload (6 шагов). Алиас: `POST /reload`.