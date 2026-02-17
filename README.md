# Kea DHCP Manager

Сервис на Go для управления Kea DHCP через REST API Control Agent.

## Структура проекта

```
kea-golang-manager/
├── cmd/app/           # CLI приложение
├── internal/
│   ├── kea/          # HTTP клиент для Kea Control Agent
│   └── service/      # Бизнес-логика
├── pkg/config/       # Конфигурация
├── config/           # Конфигурационные файлы Kea
├── docker-compose.yml
├── Makefile
└── README.md
```

## Быстрый старт

### 1. Запуск Kea в Docker

```bash
docker compose up -d
```

Проверка статуса:
```bash
docker compose ps
docker compose logs kea-control-agent
```

### 2. Сборка приложения

```bash
make build
```

Или напрямую:
```bash
go build -o bin/kea-manager cmd/app/main.go
```

### 3. Использование CLI

#### Показать текущую конфигурацию
```bash
go run cmd/app/main.go show-config
```

#### Добавить подсеть
```bash
go run cmd/app/main.go add-subnet \
  -subnet=192.168.1.0/24 \
  -pools=192.168.1.10-192.168.1.100,192.168.1.150-192.168.1.200
```

#### Добавить подсеть с резервацией
```bash
go run cmd/app/main.go add-subnet \
  -subnet=192.168.1.0/24 \
  -pools=192.168.1.10-192.168.1.100 \
  -hw-address=aa:bb:cc:dd:ee:ff \
  -ip-address=192.168.1.50 \
  -hostname=server1
```

#### Перезагрузить конфигурацию
```bash
go run cmd/app/main.go reload
```

## Проверка через curl

### Получить конфигурацию
```bash
curl -X POST http://localhost:8000 \
  -H "Content-Type: application/json" \
  -d '{
    "command": "config-get",
    "service": ["dhcp4"]
  }'
```

### Добавить подсеть
```bash
curl -X POST http://localhost:8000 \
  -H "Content-Type: application/json" \
  -d '{
    "command": "subnet4-add",
    "service": ["dhcp4"],
    "arguments": {
      "subnet4": {
        "subnet": "192.168.1.0/24",
        "pools": [
          {"pool": "192.168.1.10-192.168.1.100"}
        ]
      }
    }
  }'
```

### Перезагрузить конфигурацию
```bash
curl -X POST http://localhost:8000 \
  -H "Content-Type: application/json" \
  -d '{
    "command": "config-reload",
    "service": ["dhcp4"]
  }'
```

## Архитектура

### Слои

1. **Client (internal/kea/)** - HTTP клиент для взаимодействия с Kea Control Agent
   - Инкапсулирует REST API вызовы
   - Типизированные структуры для запросов/ответов
   - Обработка ошибок и таймаутов

2. **Service (internal/service/)** - Бизнес-логика
   - Валидация подсетей и пулов
   - Конвертация моделей
   - Оркестрация операций

3. **CLI (cmd/app/)** - Командная строка
   - Парсинг флагов
   - Вызов сервисов
   - Форматированный вывод

### Принципы

- **SOLID**: Разделение ответственности между слоями
- **KISS**: Минимум зависимостей, стандартная библиотека Go
- **Context-aware**: Все операции поддерживают context для отмены и таймаутов
- **Error handling**: Явная обработка ошибок на всех уровнях

## Пример вывода

### show-config
```json
{
  "Dhcp4": {
    "interfaces-config": {
      "interfaces": ["eth0"]
    },
    "subnet4": [
      {
        "subnet": "192.168.1.0/24",
        "pools": [
          {
            "pool": "192.168.1.10-192.168.1.100"
          }
        ]
      }
    ]
  }
}
```

### add-subnet
```
Subnet 192.168.1.0/24 added successfully
```

### reload
```
Configuration reloaded successfully
```

## Остановка

```bash
docker compose down
```
