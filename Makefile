SHELL := /bin/sh
.DEFAULT_GOAL := help

COMPOSE := docker compose
SERVICE ?=
export SERVICE

# Pass a selected service as a single shell argument.
shell_quote = '$(subst ','"'"',$(1))'
SERVICE_ARG = $(if $(SERVICE),$(call shell_quote,$(SERVICE)))

.PHONY: help services check-service up stop down restart logs ps migrate migration-status psql test go-test db-test api-test

help:
	@printf '%s\n' \
	  'Usage: make <command> [SERVICE=<name>]' \
	  '' \
	  'up       Build and start services; wait up to 120 seconds for readiness' \
	  'stop     Stop services; keep containers and volumes' \
	  'down     Stop and remove containers; keep volumes' \
	  'restart  Restart services and wait for readiness' \
	  'logs     Show the last 100 log lines per service' \
	  'ps       Show running and stopped containers' \
	  'services List service names from compose.yaml' \
	  'migrate  Apply pending database migrations' \
	  'migration-status List applied database migrations' \
	  'psql     Open an interactive database session' \
	  'test     Run all implemented test suites' \
	  'db-test  Apply migrations and run database integrity tests' \
	  'go-test  Run Go package tests without external services' \
	  'api-test Build and start the API; run its HTTP happy-path test' \
	  '' \
	  'Service commands apply to all services when SERVICE is omitted.' \
	  'Database commands always target postgres; start it with make up first.' \
	  'Example: make up SERVICE=postgres'

services:
	@$(COMPOSE) config --services

check-service:
	@set -eu; \
	if [ -n "$$SERVICE" ]; then \
	  available="$$($(COMPOSE) config --services)"; \
	  found=0; \
	  for name in $$available; do \
	    if [ "$$name" = "$$SERVICE" ]; then found=1; fi; \
	  done; \
	  if [ "$$found" -ne 1 ]; then \
	    printf 'Unknown service "%s". Available services:\n%s\n' "$$SERVICE" "$$available" >&2; \
	    exit 2; \
	  fi; \
	fi

up: check-service
	@$(COMPOSE) up --build -d --wait --wait-timeout 120 $(SERVICE_ARG)

stop: check-service
	@$(COMPOSE) stop $(SERVICE_ARG)

down: check-service
	@if [ -n "$$SERVICE" ]; then \
	  $(COMPOSE) rm --stop --force $(SERVICE_ARG); \
	else \
	  $(COMPOSE) down; \
	fi

restart: check-service
	@$(COMPOSE) restart --no-deps $(SERVICE_ARG)
	@$(COMPOSE) up -d --wait --wait-timeout 120 $(SERVICE_ARG)

logs: check-service
	@$(COMPOSE) logs --tail=100 $(SERVICE_ARG)

ps: check-service
	@$(COMPOSE) ps --all $(SERVICE_ARG)

migrate:
	@$(COMPOSE) exec -T postgres sh -c 'exec psql -X --set=ON_ERROR_STOP=on --username="$${POSTGRES_USER}" --dbname="$${POSTGRES_DB}" --file=/migrations/migrate.sql'

migration-status:
	@$(COMPOSE) exec -T postgres sh -c 'exec psql -X --set=ON_ERROR_STOP=on --username="$${POSTGRES_USER}" --dbname="$${POSTGRES_DB}" --command="TABLE schema_migrations"'

psql:
	@$(COMPOSE) exec postgres sh -c 'exec psql -X --username="$${POSTGRES_USER}" --dbname="$${POSTGRES_DB}"'

test: go-test db-test api-test

go-test:
	@$(COMPOSE) build api
	@$(COMPOSE) run --rm --no-deps api go test -count=1 -v ./...

api-test:
	@$(COMPOSE) up --build -d --wait --wait-timeout 120 api
	@$(COMPOSE) run --rm --no-deps -e E2B_API_URL=http://api:8080 api go test -tags=integration -count=1 -v ./tests/api

db-test: migrate
	@set -eu; \
	for test_zone in UTC Asia/Shanghai; do \
	  printf '[db-test] Running database integrity tests (timezone: %s)\n' "$$test_zone"; \
	  $(COMPOSE) exec -T postgres sh -c 'exec psql -X --set=ON_ERROR_STOP=on --set=test_timezone="$$1" --username="$${POSTGRES_USER}" --dbname="$${POSTGRES_DB}"' sh "$$test_zone" < tests/sql/usage_inbox.sql; \
	done; \
	printf '[db-test] Database integrity tests passed in all configured time zones.\n'
