# kea-golang-manager

Go-менеджер для [Kea DHCP](https://kea.readthedocs.io/) через REST API Control Agent. Поддерживает CLI и HTTP API.

Kea — единственный DHCP-движок. Go не реализует протокол DHCP и не манипулирует lease-файлами напрямую.

---

## Содержание

- [Архитектура](#архитектура)
- [Требования](#требования)
- [Быстрый старт](#быстрый-старт)
- [Конфигурация](#конфигурация)
- [API](#api)
- [CLI](#cli)
- [Тестирование](#тестирование)
- [Makefile](#makefile)

---

## Архитектура

```
┌──────────────────────────────────────────┐
│           Docker Compose (bridge)        │
│                                          │
│  ┌─────────────────────┐                 │
│  │      kea-dhcp4      │  UDP :67        │
│  │  lease: memfile     │                 │
│  └──────────┬──────────┘                 │
│             │ unix socket                │
│  ┌──────────▼──────────┐                 │
│  │  kea-control-agent  │  HTTP :8000     │
│  └──────────┬──────────┘                 │
└─────────────┼────────────────────────────┘
              │ HTTP
┌─────────────▼────────────────────────────┐
│         kea-golang-manager  :8080        │
│                                          │
│  GET  /health        GET  /stats         │
│  GET  /config        GET  /subnets       │
│  POST /kea/reload    POST /subnets       │
│                      DELETE /subnets/:id │
└──────────────────────────────────────────┘
```

### Стек

| Слой | Технология |
|---|---|
| DHCP-сервер | Kea DHCP 2.x |
| Lease backend | memfile (БД не используется) |
| Backend | Go 1.22, chi/v5 |
| Deployment | Docker Compose (bridge) |

### Структура пакетов

```
kea-golang-manager/
├── cmd/app/               # Точка входа: CLI + HTTP-сервер
├── internal/
│   ├── api/               # HTTP-обработчики (chi)
│   ├── kea/               # Клиент Kea Control Agent
│   └── service/           # Бизнес-логика (DHCPService)
├── pkg/config/            # Конфигурация приложения
├── config/                # Конфиги Kea (kea-dhcp4.conf, kea-ctrl-agent.conf)
├── docker-compose.yml
├── Makefile
└── README.md
```

---

## Требования

- Go 1.22+
- Docker 24+ и Docker Compose v2

---

## Быстрый старт

### 1. Запустить Kea

```bash
docker compose up -d
```

Проверить статус:

```bash
docker compose ps
docker compose logs kea-control-agent
```

Control Agent доступен на `http://localhost:8000`.

### 2. Собрать менеджер

```bash
make build
```

### 3. Запустить HTTP-сервер

```bash
./bin/kea-manager serve-http
```

### 4. Проверить здоровье

```bash
curl -s http://localhost:8080/health | jq
```

```json
{
  "status": "ok",
  "kea": { "reachable": true }
}
```

---

## Конфигурация

### Флаги запуска

| Флаг | По умолчанию | Описание |
|---|---|---|
| `-kea-url` | `http://localhost:8000` | URL Kea Control Agent |
| `-timeout` | `10s` | Таймаут HTTP-запросов к Kea |
| `-http-addr` | `:8080` | Адрес HTTP-сервера |

### Kea DHCP конфигурация

Файл `config/kea-dhcp4.conf`. Ключевые параметры:

```json
{
  "Dhcp4": {
    "interfaces-config": { "interfaces": ["eth0"] },
    "lease-database": {
      "type": "memfile",
      "name": "/kea/leases/kea-dhcp4.leases"
    },
    "control-socket": {
      "socket-type": "unix",
      "socket-name": "/kea/sockets/kea-dhcp4-ctrl.sock"
    },
    "subnet4": [
      {
        "id": 1,
        "subnet": "192.168.1.0/24",
        "pools": [{ "pool": "192.168.1.10-192.168.1.200" }]
      }
    ]
  }
}
```

> Все изменения конфигурации через API автоматически сохраняются на диск (`config-write`) и применяются без перезапуска (`config-reload`).

---

## API

Все эндпоинты доступны на `http://localhost:8080`.

### Здоровье

#### `GET /health`

```bash
curl -s http://localhost:8080/health | jq
```

```json
{
  "status": "ok",
  "kea": { "reachable": true }
}
```

Коды: `200` (ok), `503` (Kea недоступен).

### Статистика

#### `GET /stats`

Lease-статистика напрямую от Kea (команда `statistic-get-all`).

```bash
curl -s http://localhost:8080/stats | jq
```

```json
{
  "total-addresses": 191,
  "assigned-addresses": 12,
  "declined-addresses": 0
}
```

### Конфигурация Kea

#### `GET /config`

Получить текущую конфигурацию Kea.

```bash
curl -s http://localhost:8080/config | jq
```

#### `POST /kea/reload`

Сохранить конфигурацию на диск (`config-write`) и перезагрузить (`config-reload`).

```bash
curl -X POST http://localhost:8080/kea/reload
```

> Алиас: `POST /reload` — работает так же.

### Подсети

#### `GET /subnets`

```bash
curl -s http://localhost:8080/subnets | jq
```

#### `POST /subnets`

```bash
curl -X POST http://localhost:8080/subnets \
  -H "Content-Type: application/json" \
  -d '{
    "subnet": "192.168.2.0/24",
    "pools": ["192.168.2.10-192.168.2.200"],
    "reservations": [
      {
        "hw-address": "aa:bb:cc:dd:ee:ff",
        "ip-address": "192.168.2.50",
        "hostname": "server1"
      }
    ]
  }'
```

#### `DELETE /subnets/{id}`

```bash
curl -X DELETE http://localhost:8080/subnets/2
```

---

## CLI

```
./bin/kea-manager [глобальные флаги] <команда> [флаги команды]
```

### Глобальные флаги

| Флаг | По умолчанию | Описание |
|---|---|---|
| `-kea-url` | `http://localhost:8000` | URL Kea Control Agent |
| `-timeout` | `10s` | Таймаут HTTP-запросов |
| `-http-addr` | `:8080` | Адрес для `serve-http` |

### Команды

| Команда | Описание |
|---|---|
| `serve-http` | Запустить HTTP API-сервер |
| `show-config` | Вывести текущую конфигурацию Kea (JSON) |
| `add-subnet` | Добавить подсеть с пулами и резервациями |
| `reload` | Сохранить и перезагрузить конфигурацию Kea |

### Примеры

```bash
# Запуск сервера
./bin/kea-manager serve-http -http-addr=:8080

# Показать конфигурацию
./bin/kea-manager show-config

# Добавить подсеть
./bin/kea-manager add-subnet \
  -subnet=192.168.10.0/24 \
  -pools=192.168.10.10-192.168.10.200

# Добавить подсеть с резервацией
./bin/kea-manager add-subnet \
  -subnet=192.168.10.0/24 \
  -pools=192.168.10.10-192.168.10.200 \
  -hw-address=aa:bb:cc:dd:ee:ff \
  -ip-address=192.168.10.50 \
  -hostname=server1

# Сохранить и перезагрузить конфигурацию
./bin/kea-manager reload
```

---

## Прямые запросы к Kea Control Agent

Для отладки напрямую на порту 8000:

```bash
# Получить конфигурацию
curl -X POST http://localhost:8000 \
  -H "Content-Type: application/json" \
  -d '{"command":"config-get","service":["dhcp4"]}' | jq

# Перезагрузить конфигурацию
curl -X POST http://localhost:8000 \
  -H "Content-Type: application/json" \
  -d '{"command":"config-reload","service":["dhcp4"]}'
```