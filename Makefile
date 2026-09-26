-include .env
export

.PHONY: test test-race vet test-integration test-e2e check generate deps up down migrate-up migrate-down migrate-status migrate-reset smoke

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

check:
	go mod verify
	go mod tidy -diff
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "$$unformatted"; exit 1; fi
	go vet ./...
	go vet -tags integration ./...
	go vet -tags e2e,failpoints ./...
	staticcheck ./...
	govulncheck ./...
	sqlc diff
	go test -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
	$(MAKE) deps
	go run ./cmd/migrate up
	go test -race -tags integration -count=1 -timeout 15m ./...

generate:
	sqlc generate

deps:
	docker compose up -d --wait postgres keycloak aws
	docker compose run --rm aws-init

up:
	docker compose up --build -d --wait

down:
	docker compose down -v

migrate-up:
	go run ./cmd/migrate up

migrate-down:
	go run ./cmd/migrate down

migrate-status:
	go run ./cmd/migrate status

migrate-reset:
	go run ./cmd/migrate reset

smoke:
	./scripts/smoke.sh
