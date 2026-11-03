#!make

ROOT_DIR := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))
APP_NAME := pronogti
CMD_PATH := ./cmd/$(APP_NAME)
MIGRATIONS_DIR := $(ROOT_DIR)migrations
SQL_MIGRATIONS_DIR := $(MIGRATIONS_DIR)/sql
DEPLOY_DIR := $(ROOT_DIR)internal/scripts
BUILD_OUTPUT := $(ROOT_DIR)$(APP_NAME)

GO ?= go
CONFIG_FILE ?= $(ROOT_DIR)config.json
API_BASE_URL ?= http://127.0.0.1:59000

-include $(ROOT_DIR).env

VERSION ?= dev
COMMIT ?= $(shell git -C "$(ROOT_DIR)" rev-parse --short HEAD 2>/dev/null || printf 'none')
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

.DEFAULT_GOAL := build

.PHONY: \
	build \
	run \
	test \
	test-race \
	test-integration \
	codegen \
	clean \
	prod \
	migup \
	migdown \
	migfix \
	migadd \
	migver \
	runsql \
	deploy \
	deploy-full \
	deploy-backend \
	deploy-no-migrations

# Development build with debugging information.
build:
	@echo "Building $(APP_NAME)..."
	@$(GO) -C "$(ROOT_DIR)" build \
		-o "$(BUILD_OUTPUT)" \
		"$(CMD_PATH)"

# Run the backend with the selected project configuration.
run:
	@echo "Running $(APP_NAME)..."
	@$(GO) -C "$(ROOT_DIR)" run "$(CMD_PATH)" -config "$(CONFIG_FILE)"

# Run all unit tests.
test:
	@echo "Running unit tests..."
	@$(GO) -C "$(ROOT_DIR)" test ./...

test-race:
	@echo "Running tests with the race detector..."
	@$(GO) -C "$(ROOT_DIR)" test -race ./...

# Run API integration tests when this project has an integration-test package.
test-integration:
	@test -d "$(ROOT_DIR)internal/apitest" || \
		(echo "API integration tests are not configured: internal/apitest is missing."; exit 1)
	@echo "Running integration tests against $(API_BASE_URL)..."
	@API_BASE_URL="$(API_BASE_URL)" \
		$(GO) -C "$(ROOT_DIR)" test \
			./internal/apitest \
			-tags=integration \
			-count=1 \
			-v

# Remove generated build artifacts and clear the Go test cache.
clean:
	@echo "Cleaning build artifacts..."
	@rm -f "$(BUILD_OUTPUT)"
	@$(GO) -C "$(ROOT_DIR)" clean -testcache

# Optimized production build with version metadata.
prod:
	@echo "Building production $(APP_NAME)..."
	@CGO_ENABLED=0 $(GO) -C "$(ROOT_DIR)" build \
		-trimpath \
		-ldflags="-s -w -X 'main.Version=$(VERSION)' -X 'main.Commit=$(COMMIT)' -X 'main.BuildDate=$(BUILD_DATE)'" \
		-o "$(BUILD_OUTPUT)" \
		"$(CMD_PATH)"

migup:
	@test -n "$(DB_CONN)" || (echo "Set DB_CONN in .env or on the make command line."; exit 1)
	@migrate \
		-database "$(DB_CONN)" \
		-path "$(MIGRATIONS_DIR)" \
		up 1

migdown:
	@test -n "$(DB_CONN)" || (echo "Set DB_CONN in .env or on the make command line."; exit 1)
	@migrate \
		-database "$(DB_CONN)" \
		-path "$(MIGRATIONS_DIR)" \
		down 1

migfix:
	@test -n "$(word 2, $(MAKECMDGOALS))" || \
		(echo "Usage: make migfix <version>"; exit 1)
	@test -n "$(DB_CONN)" || (echo "Set DB_CONN in .env or on the make command line."; exit 1)
	@migrate \
		-database "$(DB_CONN)" \
		-path "$(MIGRATIONS_DIR)" \
		force "$(word 2, $(MAKECMDGOALS))"

migadd:
	@test -n "$(word 2, $(MAKECMDGOALS))" || \
		(echo "Usage: make migadd <migration_name>"; exit 1)
	@migrate create \
		-ext sql \
		-dir "$(MIGRATIONS_DIR)" \
		-seq "$(word 2, $(MAKECMDGOALS))"

migver:
	@test -n "$(DB_CONN)" || (echo "Set DB_CONN in .env or on the make command line."; exit 1)
	@migrate \
		-database "$(DB_CONN)" \
		-path "$(MIGRATIONS_DIR)" \
		version

runsql:
	@test -n "$(word 2, $(MAKECMDGOALS))" || \
		(echo "Usage: make runsql <script_name>"; exit 1)
	@test -f "$(DEPLOY_DIR)/run_sql.php" || \
		(echo "SQL helper is missing: internal/scripts/run_sql.php."; exit 1)
	@test -n "$(DB_CONN)" || (echo "Set DB_CONN in .env or on the make command line."; exit 1)
	@php "$(DEPLOY_DIR)/run_sql.php" \
		"$(DB_CONN)" \
		"$(word 2, $(MAKECMDGOALS))" \
		"$(SQL_MIGRATIONS_DIR)"

# Every deployment command is restricted to the backend.
deploy-backend:
	@test -x "$(DEPLOY_DIR)/deploy.sh" || \
		(echo "Executable deployment helper is missing: internal/scripts/deploy.sh."; exit 1)
	@echo "Deploying backend..."
	@"$(DEPLOY_DIR)/deploy.sh" --backend-only

deploy-no-migrations:
	@test -x "$(DEPLOY_DIR)/deploy.sh" || \
		(echo "Executable deployment helper is missing: internal/scripts/deploy.sh."; exit 1)
	@echo "Deploying backend without migrations..."
	@"$(DEPLOY_DIR)/deploy.sh" --backend-only --skip-migrations

deploy: deploy-backend
deploy-full: deploy-backend

# Usage: make codegen [gen|check|valid]
# Requires schema definitions and a codegen tool declared in go.mod.
codegen:
	@case "$(word 2, $(MAKECMDGOALS))" in \
		""|"gen") action=generate ;; \
		"check") action=check ;; \
		"valid") action=validate ;; \
		*) echo "Usage: make codegen [gen|check|valid]"; exit 1 ;; \
	esac; \
	test -f "$(ROOT_DIR)codegen.yaml" || \
		{ echo "Code generation is not configured: codegen.yaml is missing."; exit 1; }; \
	$(GO) -C "$(ROOT_DIR)" tool codegen "$$action"

# Ignore positional arguments used by migration, SQL and codegen targets.
%:
	@:
