SHELL := /bin/sh
.DEFAULT_GOAL := help

COMPOSE := docker compose
SERVICE ?=
RUN ?=
# A Go test filter selects the Go suite unless SUITE is explicitly supplied.
SUITE ?= $(if $(strip $(RUN)),go,all)
export SERVICE RUN SUITE

SCENARIO ?= assignment
ACTION ?= run
MODE ?= fast
ADVANCE ?= 0
SOURCE ?= platform-simulator
STATE ?= /state/run.json
SANDBOXES ?= 1
INTERVAL ?= 1h
BATCH_SIZE ?= 100
DELAY ?= 0s
TIMEOUT ?= 15s
RETRY_MIN ?= 1s
RETRY_MAX ?= 30s
MAX_ATTEMPTS ?= 0
DUPLICATES ?= 0
LOSE_RESPONSE ?= 0
REVERSE ?= 0
SIM_API_URL ?= http://api:8080
SCENARIO_FILE ?= $(if $(filter custom,$(SCENARIO)),/scenarios/custom-scenario.json,)
export SCENARIO ACTION MODE ADVANCE SOURCE STATE SANDBOXES INTERVAL BATCH_SIZE
export DELAY TIMEOUT RETRY_MIN RETRY_MAX MAX_ATTEMPTS DUPLICATES LOSE_RESPONSE
export REVERSE SIM_API_URL SCENARIO_FILE

# Pass a selected service as a single shell argument.
shell_quote = '$(subst ','"'"',$(1))'
SERVICE_ARG = $(if $(SERVICE),$(call shell_quote,$(SERVICE)))

.PHONY: help services check-service up stop down restart logs ps links migrate migration-status psql test go-test db-test api-test inbox-test _test-db _test-go _test-worker docs simulate simulate-help

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
	  'links    Show browser links for running HTTP services' \
	  'services List service names from compose.yaml' \
	  'migrate  Apply pending database migrations' \
	  'migration-status List applied database migrations' \
	  'psql     Open an interactive database session' \
	  'test     Run all tests, one suite, or Go tests matching RUN' \
	  'go-test  Run Go package tests without external services' \
	  'db-test  Run SQL integrity tests in both time zones' \
	  'api-test Start the API and run HTTP integration tests' \
	  'inbox-test Run PostgreSQL repository tests in private schemas' \
	  'docs     Start Scalar documentation and the API for browser requests' \
	  'simulate Run or resume platform usage; MODE=step releases one step' \
	  'simulate-help Show all simulator options' \
	  '' \
	  'Service commands apply to all services when SERVICE is omitted.' \
	  'Database commands always target postgres; start it with make up first.' \
	  'Example: make up SERVICE=postgres' \
	  'Example: make simulate SCENARIO=assignment MODE=step' \
	  'Example: make test RUN="^TestUsageBatchesHappyPath$$"'

services:
	@$(COMPOSE) config --services

docs:
	@$(MAKE) up SERVICE=docs

simulate-help:
	@$(COMPOSE) build simulator
	@$(COMPOSE) run --rm --no-deps simulator platform-simulator --help

simulate:
	@$(COMPOSE) build simulator
	@$(COMPOSE) run --rm --no-deps simulator platform-simulator \
	  "--action=$$ACTION" "--scenario=$$SCENARIO" "--mode=$$MODE" \
	  "--advance=$$ADVANCE" "--source=$$SOURCE" "--state=$$STATE" \
	  "--sandboxes=$$SANDBOXES" "--interval=$$INTERVAL" "--file=$$SCENARIO_FILE" \
	  "--api-url=$$SIM_API_URL" "--batch-size=$$BATCH_SIZE" "--delay=$$DELAY" \
	  "--timeout=$$TIMEOUT" "--retry-min=$$RETRY_MIN" "--retry-max=$$RETRY_MAX" \
	  "--max-attempts=$$MAX_ATTEMPTS" "--duplicates=$$DUPLICATES" \
	  "--lose-response=$$LOSE_RESPONSE" "--reverse=$$REVERSE"

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
	@$(MAKE) --no-print-directory links

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
	@$(MAKE) --no-print-directory links

logs: check-service
	@$(COMPOSE) logs --tail=100 $(SERVICE_ARG)

ps: check-service
	@$(COMPOSE) ps --all $(SERVICE_ARG)
	@$(MAKE) --no-print-directory links

links:
	@set -eu; \
	running_services="$$($(COMPOSE) ps --services --status running)"; \
	for service_name in $$running_services; do \
	  case "$$service_name" in \
	    api) \
	      address="$$($(COMPOSE) port api 8080)"; \
	      printf 'API health: http://%s/healthz\n' "$$address" ;; \
	    docs) \
	      address="$$($(COMPOSE) port docs 8080)"; \
	      printf 'API documentation: http://%s/\n' "$$address" ;; \
	  esac; \
	done

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

go-test:
	@$(COMPOSE) build api
	@$(COMPOSE) run --rm --no-deps api go test -count=1 -v -run "$$RUN" ./...

db-test: _test-db

api-test:
	@$(COMPOSE) up -d --wait --wait-timeout 120 postgres
	@$(MAKE) --no-print-directory migrate
	@$(COMPOSE) up --build -d --wait --wait-timeout 120 api
	@$(COMPOSE) run --rm --no-deps -e E2B_API_URL=http://api:8080 api go test -tags=integration -count=1 -v -run "$$RUN" ./tests/api

inbox-test:
	@$(COMPOSE) up -d --wait --wait-timeout 120 postgres
	@$(COMPOSE) build api
	@$(COMPOSE) run --rm --no-deps api go test -tags=integration -count=1 -v -run "$$RUN" ./tests/inbox

_test-go: go-test inbox-test api-test _test-worker

_test-worker:
	@$(COMPOSE) up --build -d --wait --wait-timeout 120 worker
	@$(COMPOSE) run --rm --no-deps api go test -tags=integration -count=1 -v -run "$$RUN" ./tests/worker

_test-db: migrate
	@set -eu; \
	for test_zone in UTC Asia/Shanghai; do \
	  printf '[test:db] Running database integrity tests (timezone: %s)\n' "$$test_zone"; \
	  for test_file in tests/sql/*.sql; do \
	    printf '[test:db] Running %s\n' "$$test_file"; \
	    $(COMPOSE) exec -T postgres sh -c 'exec psql -X --set=ON_ERROR_STOP=on --set=test_timezone="$$1" --username="$${POSTGRES_USER}" --dbname="$${POSTGRES_DB}"' sh "$$test_zone" < "$$test_file"; \
	  done; \
	done; \
	printf '[test:db] Database integrity tests passed in all configured time zones.\n'
