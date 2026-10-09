SHELL := /bin/sh
.DEFAULT_GOAL := help

COMPOSE := docker compose
SERVICE ?=
RUN ?=
# A Go test filter selects the Go suite unless SUITE is explicitly supplied.
SUITE ?= $(if $(strip $(RUN)),go,all)
export SERVICE RUN SUITE

# Pass a selected service as a single shell argument.
shell_quote = '$(subst ','"'"',$(1))'
SERVICE_ARG = $(if $(SERVICE),$(call shell_quote,$(SERVICE)))

.PHONY: help services check-service up stop down restart logs ps migrate migration-status psql test _test-db _test-go

help:
	@printf '%s\n' \
	  'Usage: make <command> [SERVICE=<name>]' \
	  'Testing: make test [SUITE=all|go|db] [RUN=<regexp>]' \
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
	  'test     Run all tests, one suite, or Go tests matching RUN' \
	  '' \
	  'Service commands apply to all services when SERVICE is omitted.' \
	  'Database commands always target postgres; start it with make up first.' \
	  'Example: make up SERVICE=postgres' \
	  'Example: make test RUN="^TestUsageBatchesHappyPath$$"'

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

test:
	@set -eu; \
	case "$$SUITE" in \
	  all|go|db) ;; \
	  *) printf 'Unknown test suite "%s". Available suites: all, go, db.\n' "$$SUITE" >&2; exit 2 ;; \
	esac; \
	if [ "$$SUITE" = db ] && [ -n "$$RUN" ]; then \
	  printf 'RUN filters Go tests and cannot be used with SUITE=db.\n' >&2; \
	  exit 2; \
	fi; \
	if [ "$$SUITE" = all ] || [ "$$SUITE" = db ]; then \
	  $(MAKE) --no-print-directory _test-db; \
	fi; \
	if [ "$$SUITE" = all ] || [ "$$SUITE" = go ]; then \
	  $(MAKE) --no-print-directory _test-go; \
	fi

_test-go:
	@$(COMPOSE) up --build -d --wait --wait-timeout 120 api
	@$(COMPOSE) run --rm --no-deps -e E2B_API_URL=http://api:8080 api go test -count=1 -v -run "$$RUN" ./...

_test-db: migrate
	@set -eu; \
	for test_zone in UTC Asia/Shanghai; do \
	  printf '[test:db] Running database integrity tests (timezone: %s)\n' "$$test_zone"; \
	  $(COMPOSE) exec -T postgres sh -c 'exec psql -X --set=ON_ERROR_STOP=on --set=test_timezone="$$1" --username="$${POSTGRES_USER}" --dbname="$${POSTGRES_DB}"' sh "$$test_zone" < tests/usage_inbox.sql; \
	done; \
	printf '[test:db] Database integrity tests passed in all configured time zones.\n'
