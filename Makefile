-include .env
export

TEMPL_VERSION = v0.3.1070
SQLC_VERSION  = v1.31.1
TEMPL = go run github.com/a-h/templ/cmd/templ@$(TEMPL_VERSION)
SQLC  = go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
GOOSE = go run github.com/pressly/goose/v3/cmd/goose@v3.28.0 # only for `migrate-new`; applying uses ./cmd/migrate
TAILWIND_VERSION = v4.3.3

TW_OS   = $(if $(filter Darwin,$(shell uname -s)),macos,linux)
TW_ARCH = $(if $(filter arm64 aarch64,$(shell uname -m)),arm64,x64)
TAILWIND = bin/tailwindcss

.PHONY: up down generate css migrate migrate-new seed run build docker test test-integration lint

up:
	docker compose up -d postgres

down:
	docker compose down

# templ views and sqlc queries -> Go (generated code is committed)
generate:
	$(TEMPL) generate
	$(SQLC) generate

$(TAILWIND):
	mkdir -p bin
	curl -sL -o $@ https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-$(TW_OS)-$(TW_ARCH)
	chmod +x $@

css: $(TAILWIND)
	$(TAILWIND) -i web/input.css -o web/static/app.css --minify

migrate:
	go run ./cmd/migrate

migrate-new:
	$(GOOSE) -dir migrations create $(name) sql

seed:
	go run ./cmd/seed

run: generate css
	go run ./cmd/server

build: generate css
	go build -o bin/server ./cmd/server
	go build -o bin/migrate ./cmd/migrate

docker:
	docker build -t goat-store .

test:
	go test ./...

TEST_DB_URL = $(subst /goat?,/goat_test?,$(DATABASE_URL))

# Runs against a separate goat_test database that is dropped and re-created each time, so
# tests start from a clean slate and leftovers from earlier runs cannot hide or cause failures
# (needs `make up`).
test-integration:
	docker compose exec -T postgres psql -U goat -d postgres -c "DROP DATABASE IF EXISTS goat_test WITH (FORCE)"
	docker compose exec -T postgres psql -U goat -d postgres -c "CREATE DATABASE goat_test"
	DATABASE_URL="$(TEST_DB_URL)" go run ./cmd/migrate
	TEST_DATABASE_URL="$(TEST_DB_URL)" go test -race -count=1 ./...

# vet plus the architecture boundary test
lint:
	go vet ./...
	go test ./internal/archtest/
