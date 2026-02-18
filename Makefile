.PHONY: build run test clean docker-up docker-down docker-logs

build:
	go build -o bin/kea-manager cmd/app/main.go

run:
	go run cmd/app/main.go

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

test:
	go test ./... -count=1

clean:
	rm -rf bin/
