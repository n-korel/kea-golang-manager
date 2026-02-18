# kea-ha-orchestrator

Go-оркестратор для [Kea DHCP](https://kea.readthedocs.io/) с поддержкой **Active-Standby HA**, мониторингом через SNMP и обогащением топологии через LLDP.

Kea — единственный DHCP-движок. Go не реализует протокол DHCP и не манипулирует lease-файлами.

---

## Содержание

- [Архитектура](#архитектура)
- [Требования](#требования)
- [Быстрый старт](#быстрый-старт)
- [Конфигурация](#конфигурация)
- [API](#api)
- [HA и Failover](#ha-и-failover)
- [SNMP и LLDP](#snmp-и-lldp)
- [CLI](#cli)
- [Тестирование](#тестирование)
- [Наблюдаемость](#наблюдаемость)
- [Безопасность](#безопасность)

---

## Архитектура

```
┌─────────────────────────────────────────────────────────┐
│                     host network                        │
│                                                         │
│  ┌──────────────────┐        ┌──────────────────┐       │
│  │   kea-primary    │◄──────►│   kea-standby    │       │
│  │  kea-dhcp4 :67   │HA link │  kea-dhcp4 :67   │       │
│  │  ctrl-agent:8000 │        │  ctrl-agent:8001 │       │
│  └────────┬─────────┘        └────────┬─────────┘       │
│           │                           │                 │
│           └──────────┬────────────────┘                 │
│                      │                                  │
│          ┌───────────▼──────────┐                       │
│          │  kea-ha-orchestrator │  :8080                │
│          │  ┌────────────────┐  │                       │
│          │  │  HA Monitor    │  │  GET  /health         │
│          │  │  State Machine │  │  GET  /ha/status      │
│          │  │  Failover Eng. │  │  POST /ha/demote      │
│          │  ├────────────────┤  │  POST /kea/reload     │
│          │  │  SNMP Poller   │  │  GET  /stats          │
│          │  │  LLDP Collect. │  │  GET  /subnets        │
│          │  └────────────────┘  │  POST /subnets        │
│          └──────────────────────┘                       │
└─────────────────────────────────────────────────────────┘
```

### Стек

| Слой | Технология |
|---|---|
| DHCP-сервер | Kea DHCP 2.x |
| HA-режим | Active-Standby (passive-backup), мультипримари запрещён |
| Lease backend | memfile (БД не используется) |
| Backend | Go 1.22, chi/v5 |
| Deployment | Docker, host network, CAP\_NET\_RAW |
| Мониторинг | SNMP (SNMPv3 preferred) |
| Топология | LLDP (MAC → port → VLAN) |

### Структура пакетов

```
kea-ha-orchestrator/
├── cmd/app/               # Точка входа: CLI + HTTP-сервер
├── internal/
│   ├── api/               # HTTP-обработчики (chi)
│   ├── ha/                # HA state machine, monitor, failover engine
│   ├── kea/               # Клиент Kea Control Agent
│   ├── lldp/              # LLDP topology collector
│   ├── service/           # Бизнес-логика (DHCPService)
│   └── snmp/              # SNMP poller
├── pkg/config/            # Конфигурация приложения
├── config/                # Конфиги Kea (primary, standby, ctrl-agent)
├── docker-compose.yml
├── Makefile
└── README.md
```

---

## Требования

- Go 1.22+
- Docker 24+ и Docker Compose v2
- На хосте: `CAP_NET_RAW`, `CAP_NET_ADMIN` (для DHCP broadcast)
- (Опционально) SNMPv3-агент на сетевом оборудовании
- (Опционально) `lldpd` или доступ к `/sys/bus/pci/...` для LLDP

---

## Быстрый старт

### 1. Клонировать и настроить окружение

```bash
git clone https://github.com/yourorg/kea-ha-orchestrator
cd kea-ha-orchestrator
cp .env.example .env
# Отредактируй .env: IP-адреса узлов, SNMP credentials
```

### 2. Запустить инфраструктуру

```bash
docker compose up -d
```

Проверить статус:

```bash
docker compose ps
docker compose logs kea-primary-ctrl-agent
docker compose logs kea-standby-ctrl-agent
```

Control Agent primary: `http://localhost:8000`
Control Agent standby: `http://localhost:8001`

**Ограничение single-host (одна машина):** при `network_mode: host` оба контейнера Kea DHCP4 пытаются занять один и тот же порт **UDP 67** на хосте. На одной машине порт 67 может слушать только один процесс, поэтому контейнеры `kea-primary-dhcp4` и `kea-standby-dhcp4` будут уходить в цикл **Restarting**. Для полноценного HA нужны **два разных хоста** (на каждом свой порт 67). Для локальной разработки можно поднимать только один узел (например, только primary: `docker compose up -d kea-primary-dhcp4 kea-primary-ctrl-agent`) или использовать два хоста/VM. Если даже один контейнер `kea-primary-dhcp4` уходит в Restarting — смотри причину в логах: `docker compose logs kea-primary-dhcp4`.

**Docker Desktop на Windows:** при `network_mode: host` порты контейнеров привязаны к сети **Linux-VM (WSL2)**, а не к Windows. Оркестратор, запущенный в Windows (`./bin/kea-manager serve-http`), обращается к `localhost:8000` на Windows и получает *connection refused*, даже если Control Agent в контейнере запущен. Решение: запускать оркестратор **в той же среде, где крутится Docker** (например, в WSL: `cd /mnt/c/Users/.../kea-golang-manager && ./bin/kea-manager serve-http`), тогда localhost будет общий. Альтернатива для dev — отдельный `docker-compose.dev-windows.yml` с bridge и публикацией портов 8000, 8001 (без host), чтобы до Control Agent можно было достучаться с Windows (DHCP на 67 в таком режиме с хоста работать не будет).

### 3. Собрать и запустить оркестратор

```bash
make build
./bin/kea-manager serve-http
```

Оркестратор запускается на хосте (в docker-compose отдельного сервиса нет):

```bash
./bin/kea-manager serve-http
```

### 4. Проверить HA-статус

```bash
curl -s http://localhost:8080/ha/status | jq
```

Ожидаемый ответ при здоровом кластере:

```json
{
  "current_role": "primary",
  "peer_status": "online",
  "ha_state": "hot-standby",
  "last_failover_reason": null,
  "last_role_change_timestamp": "2025-11-01T12:00:00Z"
}
```

---

## Конфигурация

### Переменные окружения

| Переменная | По умолчанию | Описание |
|---|---|---|
| `KEA_PRIMARY_URL` | `http://localhost:8000` | URL Control Agent primary |
| `KEA_STANDBY_URL` | `http://localhost:8001` | URL Control Agent standby |
| `HTTP_ADDR` | `:8080` | Адрес HTTP-сервера |
| `REQUEST_TIMEOUT` | `10s` | Таймаут запросов к Kea |
| `HA_POLL_INTERVAL` | `5s` | Интервал опроса HA heartbeat |
| `HA_MIN_FAILURES` | `3` | Минимум подряд неудач до фиксации сбоя |
| `HA_FAILOVER_DELAY` | `10s` | Задержка перед промоцией standby |
| `SNMP_TARGET` | — | IP SNMP-агента (опционально) |
| `SNMP_VERSION` | `3` | Версия SNMP (3 или 2c) |
| `SNMP_V3_USER` | — | SNMPv3 username |
| `SNMP_V3_AUTH_PASS` | — | SNMPv3 auth passphrase |
| `SNMP_V3_PRIV_PASS` | — | SNMPv3 priv passphrase |
| `LOG_LEVEL` | `info` | Уровень логирования (debug/info/warn/error) |

### Kea HA — ключевые параметры

В `config/kea-dhcp4-primary.conf`:

```json
"high-availability": [{
  "this-server-name": "primary",
  "mode": "passive-backup",
  "heartbeat-delay": 10000,
  "max-unacked-clients": 5,
  "sync-timeout": 60000,
  "peers": [
    {
      "name": "primary",
      "url": "http://<PRIMARY_IP>:8000",
      "role": "primary"
    },
    {
      "name": "standby",
      "url": "http://<STANDBY_IP>:8001",
      "role": "standby"
    }
  ]
}]
```

> **Важно:** `heartbeat-delay`, `max-unacked-clients` и `sync-timeout` всегда задаются явно. Implicit defaults запрещены по правилам проекта.

---

## API

Все эндпоинты доступны на `http://localhost:8080`.

### Здоровье и статус

#### `GET /health`

Агрегированный статус кластера.

```bash
curl -s http://localhost:8080/health | jq
```

```json
{
  "status": "ok",
  "primary": { "reachable": true, "ha_state": "hot-standby" },
  "standby": { "reachable": true, "ha_state": "hot-standby" }
}
```

Коды ответа: `200` (ok / degraded), `503` (critical).

#### `GET /ha/status`

Полный HA-статус из state machine оркестратора.

```bash
curl -s http://localhost:8080/ha/status | jq
```

```json
{
  "current_role": "primary",
  "peer_status": "online",
  "ha_state": "hot-standby",
  "last_failover_reason": null,
  "last_role_change_timestamp": "2025-11-01T12:00:00Z"
}
```

### Управление failover

#### `POST /ha/demote`

Принудительно перевести узел в standby (защита от split-brain).

```bash
curl -X POST http://localhost:8080/ha/demote \
  -H "Content-Type: application/json" \
  -d '{"node": "primary", "confirm": true}'
```

> Требует явного `"confirm": true`. Без него — `400 Bad Request`.

### Конфигурация Kea

#### `POST /kea/reload`

Перезагрузить конфигурацию на обоих узлах. Вызывает `config-write` перед `config-reload`.

```bash
curl -X POST http://localhost:8080/kea/reload
```

#### `GET /config`

Получить текущую конфигурацию Kea (с primary).

```bash
curl -s http://localhost:8080/config | jq
```

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

### Статистика

#### `GET /stats`

Агрегация lease-статистики Kea + данных SNMP.

```bash
curl -s http://localhost:8080/stats | jq
```

```json
{
  "ha_state": "hot-standby",
  "leases": {
    "total": 200,
    "assigned": 47,
    "declined": 0
  },
  "snmp": {
    "collected_at": "2025-11-01T12:05:00Z",
    "interfaces": [...]
  }
}
```

---

## HA и Failover

### Состояния HA

| Состояние | Описание |
|---|---|
| `hot-standby` | Оба узла работают, standby синхронизирован |
| `partner-down` | Primary объявил peer недоступным, обслуживает один |
| `communication-recovery` | HA-link восстановлен, идёт синхронизация |
| `ready` | Узел готов, ожидает назначения роли |
| `terminated` | HA остановлен вручную |

### Модели сбоев

Оркестратор детектирует и обрабатывает:

- `kea_process_crash` — процесс kea-dhcp4 упал
- `control_agent_crash` — Control Agent недоступен
- `ha_link_failure` — HA-heartbeat не проходит
- `network_partition` — primary изолирован от сети
- `container_restart` — контейнер перезапустился
- `host_reboot` — хост перезагрузился

### Логика failover

1. HA Monitor опрашивает оба узла каждые `HA_POLL_INTERVAL`
2. После `HA_MIN_FAILURES` подряд неудачных проверок — сбой зафиксирован
3. Quorum check: верификация, что peer действительно недоступен (не просто медленный)
4. Задержка `HA_FAILOVER_DELAY` перед промоцией
5. Вызов `ha-maintenance` на живом узле через Control Agent
6. Логирование причины и временной метки

### Защита от split-brain

- Авто-промоция без peer verification — запрещена
- Dual-primary состояние — детектируется и блокируется
- Принудительная демоция через `POST /ha/demote`
- HA-link loss логируется как отдельное событие

---

## SNMP и LLDP

### SNMP

Используется **только для мониторинга**. Не является источником решений о failover.

- Предпочитается SNMPv3; SNMPv2c — только fallback
- Polling в отдельной горутине, не блокирует основной loop
- Credentials — только из переменных окружения, не хардкодятся
- Данные доступны через `GET /stats`

### LLDP

Используется **только для обогащения топологии** (MAC → switch port → VLAN).

- Читает данные из `lldpctl` или `/sys/...`
- Tolerate stale data: при недоступности LLDP возвращает последние известные данные
- Не блокирует DHCP-операции
- Не участвует в управлении

---

## CLI

```
./bin/kea-manager [глобальные флаги] <команда> [флаги команды]
```

### Глобальные флаги

| Флаг | По умолчанию | Описание |
|---|---|---|
| `-kea-url` | `http://localhost:8000` | URL Kea Control Agent (primary) |
| `-timeout` | `10s` | Таймаут HTTP-запросов |
| `-http-addr` | `:8080` | Адрес HTTP-сервера для `serve-http` |

### Команды

| Команда | Описание |
|---|---|
| `serve-http` | Запустить HTTP API-сервер с HA-монитором |
| `show-config` | Вывести текущую конфигурацию Kea (JSON) |
| `add-subnet` | Добавить подсеть с пулами и резервациями |
| `reload` | Перезагрузить конфигурацию Kea |

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

# Перезагрузить конфигурацию
./bin/kea-manager reload
```

---

## Тестирование

```bash
# Unit-тесты
make test

# Integration-тесты (требуется Docker)
make test-integration

# Конкретный сценарий
go test ./internal/ha/... -run TestFailover_KillPrimary -v
```

### Сценарии integration-тестов

| Сценарий | Проверяет |
|---|---|
| `kill_primary` | Standby получает `role=primary`, `ha_state=partner-down` |
| `restart_secondary` | Primary остаётся активным; после recovery — `hot-standby` |
| `ha_link_drop` | Нет dual-primary при разрыве HA-link |
| `network_partition` | Failover на standby при изоляции primary |
| `renewal_storm` | Нет потери обслуживания при flood DHCP RENEW во время failover |
| `simultaneous_restart` | Корректное восстановление без dual-primary |

> Каждый тест проверяет **финальное HA-состояние**, а не только отсутствие ошибок. Dual-primary явно исключён в каждом сценарии.

---

## Наблюдаемость

### Логирование

Структурированные логи (slog). Уровень задаётся через `LOG_LEVEL`.

Обязательные события:

- Смена роли (`role_transition`)
- Причина failover (`failover_reason`)
- Изменение состояния HA-link (`ha_link_state`)

Пример записи:

```json
{
  "time": "2025-11-01T12:10:00Z",
  "level": "WARN",
  "msg": "role_transition",
  "from": "hot-standby",
  "to": "partner-down",
  "reason": "control_agent_crash",
  "node": "primary"
}
```

### Экспортируемые метрики (`GET /ha/status`)

| Поле | Тип | Описание |
|---|---|---|
| `current_role` | string | `primary` / `standby` / `unknown` |
| `peer_status` | string | `online` / `offline` / `unknown` |
| `ha_state` | string | Текущее состояние HA state machine |
| `last_failover_reason` | string\|null | Причина последнего failover |
| `last_role_change_timestamp` | string\|null | RFC3339 время последней смены роли |

---

## Безопасность

- **Control Agent не доступен публично** — только через loopback или внутреннюю сеть
- **Нет хардкоженых credentials** — все секреты через переменные окружения
- **SNMPv3 preferred** — SNMPv2c только при явной необходимости
- **`/ha/status` и `/ha/demote`** — рекомендуется ограничить на уровне сети или reverse proxy
- **Unix-сокет Kea** — не экспонируется за пределы контейнера

---

## Makefile

| Команда | Описание |
|---|---|
| `make build` | Собрать бинарник в `bin/kea-manager` |
| `make run` | Запустить напрямую через `go run` |
| `make test` | Unit-тесты |
| `make test-integration` | Integration-тесты (Docker) |
| `make docker-up` | Поднять все контейнеры |
| `make docker-down` | Остановить контейнеры |
| `make docker-logs` | Следить за логами |
| `make clean` | Удалить `bin/` |

---
