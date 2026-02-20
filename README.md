# kea-golang-manager

Go-менеджер для [Kea DHCP](https://kea.readthedocs.io/) через REST API Control Agent.  
Поддерживает **Active-Standby HA** (hot-standby), CLI и HTTP API.

Kea — единственный DHCP-движок и единственный источник конфигурации.  
Go не реализует протокол DHCP, выборы узлов, failover и не манипулирует lease-файлами.

---

## Содержание

- [Архитектура](#архитектура)
- [Ключевые архитектурные решения](#ключевые-архитектурные-решения)
- [Требования](#требования)
- [Быстрый старт](#быстрый-старт)
- [Конфигурация](#конфигурация)
- [API](#api)
- [CLI](#cli)
- [HA: статус и поведение](#ha-статус-и-поведение)
- [Тестирование](#тестирование)
- [Makefile](#makefile)

---

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

---

## Ключевые архитектурные решения

### Kea как единственный источник конфигурации

Менеджер не хранит конфигурацию ни в памяти, ни в файле на диске. Каждый запрос на чтение (`GET /config`, `GET /subnets`) идёт напрямую в Kea через `config-get`. Kea сохраняет конфиг на диск самостоятельно через `config-write`.

**Последствие:** если конфиг Kea был изменён вне менеджера (вручную или другим клиентом), менеджер об этом не знает и не обнаружит рассинхронизацию.

### Стратегия применения конфига: Guarded Apply

`libdhcp_ha.so` синхронизирует **лизы**, но не конфигурацию. Если применить конфиг только на primary, а primary упадёт — standby поднимется с устаревшей конфигурацией и не будет знать о новых подсетях.

Поэтому все мутации применяются по следующему алгоритму:

```
1. config-set + config-write на активном узле
2. ha-heartbeat → проверить HA-состояние
3a. Если hot-standby → config-set + config-write на standby
3b. Если не hot-standby → пропустить standby, вернуть HTTP 207 с предупреждением
4. config-reload на активном узле
5. config-reload на standby (только если шаг 3a выполнен)
```

Применение конфига на standby в состоянии `hot-standby` **безопасно**: standby пассивен и config-set не влияет на lease state machine. Применение в состоянии `communication-recovery` или `partner-down` **пропускается** для предотвращения split-brain.

### HTTP 207 — частичный успех

Если standby apply был пропущен, API возвращает `207 Multi-Status` вместо `201`/`204`:

```json
{
  "warning": "standby apply skipped",
  "ha_state": "communication-recovery"
}
```

Это не ошибка — операция на активном узле выполнена успешно. Предупреждение информирует, что конфиги узлов временно расходятся.

---

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

В `kea-standby.conf` меняется только `this-server-name: "kea-standby"`.

**Docker (образы jonasal):**  
- Путь к хукам: `/usr/local/lib/kea/hooks` (в конфигах проекта уже указан).  
- URL пиров HA: хук `libdhcp_ha.so` в этом образе принимает только IP-адреса. В конфигах указаны имена сервисов (`kea-primary-ctrl-agent`, `kea-standby-ctrl-agent`); при старте контейнеров скрипт `scripts/kea-dhcp-entrypoint.sh` подставляет их IP в копию конфига, чтобы избежать статических IP в Docker (и ошибки «Address already in use» в VMware и др.).

---

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

---

## CLI

```
./bin/kea-manager [флаги] <команда>
```

| Команда | Описание |
|---|---|
| `serve-http` | Запустить HTTP API-сервер |
| `show-config` | Конфигурация активного узла (JSON) |
| `add-subnet` | Добавить подсеть (guarded apply) |
| `reload` | Сохранить и перезагрузить конфигурацию |
| `ha-status` | HA-статус обоих узлов |

---

## HA: статус и поведение

### Что делает менеджер

- Опрашивает `ha-heartbeat` на обоих узлах
- Выбирает активный узел для чтения и записи
- Применяет guarded apply при мутациях конфига
- Логирует смену HA-состояний и причины пропуска standby

### Что делает Kea (без участия менеджера)

- Обнаруживает недоступность партнёра через heartbeat
- Автоматически переключается в `partner-down`
- Синхронизирует лизы при восстановлении партнёра
- Возвращается в `hot-standby` после sync

### HA-состояния и поведение Guarded Apply

| Состояние | Standby apply | Причина |
|---|---|---|
| `hot-standby` | ✅ выполняется | Standby пассивен, безопасно |
| `communication-recovery` | ⚠️ пропускается | Риск split-brain |
| `partner-down` | ⚠️ пропускается | Партнёр недоступен или изолирован |
| `syncing` | ⚠️ пропускается | Идёт sync lease DB |
| `waiting` | ⚠️ пропускается | Узел стартует |
| `ready` | ⚠️ пропускается | Ожидание подтверждения от партнёра |

При пропуске возвращается `HTTP 207` с полем `ha_state`.

---

## Устранение неполадок

### «Address already in use» при `docker compose up`

В VMware (и в некоторых средах) кастомная подсеть Docker может конфликтовать с сетью хоста. В проекте статические IP не используются: скрипт `scripts/kea-dhcp-entrypoint.sh` при старте подставляет IP ctrl-agent в конфиг. Убедитесь, что скрипт исполняемый: `chmod +x scripts/kea-dhcp-entrypoint.sh`.

### Файл `/etc/docker/daemon.json`

Файла может не быть — его можно создать. Например, отключить IPv6 (иногда помогает при сетевых ошибках в VMware):

```bash
sudo mkdir -p /etc/docker
echo '{"ipv6": false}' | sudo tee /etc/docker/daemon.json
sudo systemctl restart docker
```

После изменений снова выполните `docker compose up -d`.

---

## Тестирование

```bash
make test
```

Тесты без Docker и без живого Kea (только моки).

---

## Makefile

```
make build       — собрать бинарник
make run         — go run
make test        — unit-тесты
make clean       — удалить bin/
make docker-up   — поднять Kea HA-кластер
make docker-down — остановить кластер
make docker-logs — логи контейнеров
make smoke       — smoke-test против запущенного сервера
```