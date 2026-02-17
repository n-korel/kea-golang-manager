# Kea DHCP Manager

Go-сервис для управления [Kea DHCP](https://kea.readthedocs.io/) через REST API Control Agent. Поддерживает CLI и HTTP API.

## Возможности

- **CLI**: добавление подсетей, просмотр конфигурации, перезагрузка Kea
- **HTTP API**: REST-эндпоинты для интеграции с другими сервисами
- Работа с пулами адресов и резервациями (reservations)
- Минимальные зависимости, стандартная библиотека + chi для роутинга

## Требования

- Go 1.21+
- Kea DHCP с Control Agent (например, через Docker)

## Структура проекта

```
kea-golang-manager/
├── cmd/app/           # Точка входа: CLI и HTTP-сервер
├── internal/
│   ├── api/           # HTTP-обработчики (chi)
│   ├── kea/           # Клиент Kea Control Agent
│   └── service/       # Бизнес-логика (DHCPService)
├── pkg/config/        # Конфигурация приложения
├── config/            # Конфиги Kea (DHCP4, ctrl-agent)
├── docker-compose.yml # Kea в контейнерах
├── Makefile
└── README.md
```

## Быстрый старт

### 1. Запуск Kea

```bash
docker compose up -d
```

Проверка:

```bash
docker compose ps
docker compose logs kea-control-agent
```

Control Agent по умолчанию доступен на `http://localhost:8000`.

### 2. Сборка

```bash
make build
```

Или:

```bash
go build -o bin/kea-manager cmd/app/main.go
```

### 3. Режимы работы

#### CLI

| Команда | Описание |
|--------|----------|
| `show-config` | Вывести текущую конфигурацию Kea (JSON) |
| `add-subnet` | Добавить подсеть с пулами и опциональными резервациями |
| `reload` | Перезагрузить конфигурацию Kea |
| `serve-http` | Запустить HTTP API-сервер |

**Глобальные флаги** (до команды):

- `-kea-url` — URL Kea Control Agent (по умолчанию `http://localhost:8000`)
- `-timeout` — таймаут запросов (по умолчанию `10s`)
- `-http-addr` — адрес HTTP-сервера для `serve-http` (по умолчанию `:8080`)

#### Примеры CLI

Показать конфигурацию:

```bash
go run cmd/app/main.go show-config
```

Добавить подсеть с пулами:

```bash
go run cmd/app/main.go add-subnet \
  -subnet=192.168.1.0/24 \
  -pools=192.168.1.10-192.168.1.100,192.168.1.150-192.168.1.200
```

Добавить подсеть с резервацией:

```bash
go run cmd/app/main.go add-subnet \
  -subnet=192.168.1.0/24 \
  -pools=192.168.1.10-192.168.1.100 \
  -hw-address=aa:bb:cc:dd:ee:ff \
  -ip-address=192.168.1.50 \
  -hostname=server1
```

Перезагрузить конфигурацию:

```bash
go run cmd/app/main.go reload
```

Запуск HTTP-сервера:

```bash
go run cmd/app/main.go serve-http -http-addr=:8080
```

---

#### HTTP API (режим `serve-http`)

| Метод | Путь | Описание |
|-------|------|----------|
| GET | `/config` | Получить конфигурацию Kea |
| GET | `/subnets` | Список подсетей |
| POST | `/subnets` | Добавить подсеть |
| DELETE | `/subnets/{id}` | Удалить подсеть по индексу |
| POST | `/reload` | Перезагрузить конфигурацию |

**Добавление подсети** — `POST /subnets`:

```json
{
  "subnet": "192.168.1.0/24",
  "pools": ["192.168.1.10-192.168.1.100"],
  "reservations": [
    {
      "hw_address": "aa:bb:cc:dd:ee:ff",
      "ip_address": "192.168.1.50",
      "hostname": "server1"
    }
  ]
}
```

Примеры запросов:

```bash
# Конфигурация
curl -s http://localhost:8080/config | jq

# Список подсетей
curl -s http://localhost:8080/subnets | jq

# Добавить подсеть
curl -X POST http://localhost:8080/subnets \
  -H "Content-Type: application/json" \
  -d '{"subnet":"192.168.2.0/24","pools":["192.168.2.10-192.168.2.100"]}'

# Перезагрузка
curl -X POST http://localhost:8080/reload
```

## Прямые запросы к Kea Control Agent (curl)

Для отладки можно вызывать Agent напрямую на порту 8000:

```bash
# Конфигурация
curl -X POST http://localhost:8000 \
  -H "Content-Type: application/json" \
  -d '{"command":"config-get","service":["dhcp4"]}'

# Добавить подсеть
curl -X POST http://localhost:8000 \
  -H "Content-Type: application/json" \
  -d '{
    "command": "subnet4-add",
    "service": ["dhcp4"],
    "arguments": {
      "subnet4": {
        "subnet": "192.168.1.0/24",
        "pools": [{"pool": "192.168.1.10-192.168.1.100"}]
      }
    }
  }'

# Перезагрузка
curl -X POST http://localhost:8000 \
  -H "Content-Type: application/json" \
  -d '{"command":"config-reload","service":["dhcp4"]}'
```

## Архитектура

- **internal/kea** — HTTP-клиент к Kea Control Agent: типизированные запросы/ответы, таймауты, обработка ошибок.
- **internal/service** — бизнес-логика: валидация подсетей и пулов, преобразование моделей, вызовы клиента.
- **internal/api** — HTTP-обработчики (chi): REST для конфигурации, подсетей и reload.
- **cmd/app** — парсинг флагов, выбор команды (CLI или `serve-http`), вывод в консоль.

Принципы: разделение слоёв (SOLID), минимум зависимостей (KISS), использование `context` для отмены и таймаутов, явная обработка ошибок.

## Остановка

```bash
docker compose down
```
