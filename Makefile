.PHONY: build test run docker-up docker-down loadtest loadtest-light loadtest-medium loadtest-heavy loadtest-hour

build:
	go build -o bin/logflow ./cmd/server/

test:
	go test ./... -v -race

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down -v


# Лёгкая нагрузка: 3 сессии, 100 сигналов/сек каждая, 20 сек
loadtest-light:
	go run ./cmd/loadtest \
		--sessions=3 \
		--signals-per-sec=100 \
		--events-per-sec=5 \
		--duration=20s

# Средняя нагрузка: 5 сессий, 300 сигналов/сек каждая, 30 сек
loadtest-medium:
	go run ./cmd/loadtest \
		--sessions=5 \
		--signals-per-sec=300 \
		--events-per-sec=10 \
		--duration=30s

# Высокая нагрузка: 10 сессий, 500 сигналов/сек каждая, 60 сек
loadtest-heavy:
	go run ./cmd/loadtest \
		--sessions=10 \
		--signals-per-sec=500 \
		--events-per-sec=20 \
		--duration=60s

# Длительная высокая нагрузка: 10 сессий, 500 сигналов/сек каждая, 1 час
loadtest-hour:
	go run ./cmd/loadtest \
		--sessions=10 \
		--signals-per-sec=500 \
		--events-per-sec=20 \
		--duration=1h

ws-connect:
	wscat -c "ws://localhost:8080/sessions/42/stream?api_key=dev-secret-key"


curl-signals:
	curl -s "http://localhost:8080/sessions/42/signal-logs?limit=10" \
		-H "X-API-Key: dev-secret-key" | jq .

curl-events:
	curl -s "http://localhost:8080/sessions/42/event-logs?limit=10" \
		-H "X-API-Key: dev-secret-key" | jq .

curl-signals-restart:
	curl -s "http://localhost:8080/sessions/42/signal-logs?restart_id=1&limit=5" \
		-H "X-API-Key: dev-secret-key" | jq .

curl-archive:
	curl -s -X POST "http://localhost:8080/sessions/42/archive" \
		-H "X-API-Key: dev-secret-key" | jq .

curl-delete:
	curl -s -X DELETE "http://localhost:8080/sessions/42" \
		-H "X-API-Key: dev-secret-key" | jq .

curl-health:
	curl -s http://localhost:8080/health | jq .

archives-ls:
	docker compose exec logflow ls -lh /archives/
