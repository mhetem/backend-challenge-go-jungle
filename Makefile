.PHONY: test test-race vet test-integration test-e2e up down migrate-up migrate-down smoke

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...
	go vet -tags integration ./...
	go vet -tags e2e,failpoints ./...

test-integration:
	go test -race -tags integration -count=1 -timeout 15m ./...

test-e2e:
	go test -race -tags e2e -count=1 -timeout 20m ./test/e2e/...

up:
	docker compose up --build -d --wait

down:
	docker compose down -v

migrate-up:
	go run ./cmd/migrate up

migrate-down:
	go run ./cmd/migrate down

smoke:
	./scripts/smoke.sh
