SHELL := /bin/sh
.DEFAULT_GOAL := help

COMPOSE := docker compose
PAGES_URL ?= https://pupitooo.github.io/e2b-billing-api/
SERVICE ?=
RUN ?=
# Test regexes are data; preserve dollar anchors in command-line overrides.
override RUN := $(value RUN)
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
SCENARIO_FILE ?= $(if $(filter custom,$(SCENARIO)),/scenarios/custom-scenario.json,$(if $(filter billing-%,$(SCENARIO)),/scenarios/$(SCENARIO).json,))
export SCENARIO ACTION MODE ADVANCE SOURCE STATE SANDBOXES INTERVAL BATCH_SIZE
export DELAY TIMEOUT RETRY_MIN RETRY_MAX MAX_ATTEMPTS DUPLICATES LOSE_RESPONSE
export REVERSE SIM_API_URL SCENARIO_FILE

# Pass a selected service as a single shell argument.
shell_quote = '$(subst ','"'"',$(1))'
SERVICE_ARG = $(if $(SERVICE),$(call shell_quote,$(SERVICE)))

# Make treats the positional date as another goal; consume only this command's argument.
ifneq ($(filter delorean,$(MAKECMDGOALS)),)
KEEP_TIMELINE ?= 0
ifneq ($(KEEP_TIMELINE),0)
ifneq ($(KEEP_TIMELINE),1)
$(error KEEP_TIMELINE must be 0 or 1)
endif
endif
DELOREAN_DATE := $(word 2,$(MAKECMDGOALS))
ifneq ($(words $(MAKECMDGOALS)),2)
$(error Usage: make delorean YYYY-MM-DD [KEEP_TIMELINE=0|1])
endif
ifneq ($(firstword $(MAKECMDGOALS)),delorean)
$(error Usage: make delorean YYYY-MM-DD [KEEP_TIMELINE=0|1])
endif
# Reject malformed arguments before they can become executable Make goals.
ifneq ($(shell printf '%s\n' $(call shell_quote,$(DELOREAN_DATE)) | sed -n '/^[1-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]$$/p'),$(DELOREAN_DATE))
$(error Usage: make delorean YYYY-MM-DD [KEEP_TIMELINE=0|1])
endif
.PHONY: $(DELOREAN_DATE)
$(DELOREAN_DATE): ; @:
override E2B_DEMO_NOW := $(DELOREAN_DATE)T00:00:00Z
export E2B_DEMO_NOW
endif

.PHONY: help services check-service up delorean stop down restart logs ps links migrate migration-status psql test go-test db-test api-test inbox-test e2e-test _test-db _test-go _test-worker docs api-docs docs-check docs-export demo-check adminer-check simulate simulate-help fmt fmt-check vet check install-hooks

help:
	@printf '%s\n' \
	  'Usage: make <command> [SERVICE=<name>]' \
	  'Testing: make test [SUITE=all|go|db|e2e] [RUN=<regexp>]' \
	  '' \
	  'up       Build and start services; wait up to 120 seconds for readiness' \
	  'delorean <date> Delete project volumes, start all services, and migrate at midnight UTC' \
	  '                Add KEEP_TIMELINE=1 to preserve database history and simulator checkpoints' \
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
	  'e2e-test Run isolated backend E2E scenarios and preserve evidence' \
	  'docs     Start the Markdown guides, Scalar reference, and API' \
	  'api-docs Start the portal and print the Scalar reference link' \
	  'docs-check Validate published guides, links, and assets without services' \
	  'docs-export Export guides and read-only Scalar to tools/pages for GitHub Pages' \
	  'demo-check Run the static GUI tests in an ephemeral Node container' \
	  'adminer-check Check exact integer display with the pinned Adminer runtime' \
	  'simulate Run or resume platform usage; MODE=step releases one step' \
	  'simulate-help Show all simulator options' \
	  'fmt      Format Go source and whitespace' \
	  'fmt-check Check gofmt and whitespace' \
	  'vet      Run go vet with default and integration build tags' \
	  'check    Run formatting and static checks together' \
	  'install-hooks Enable staged Go checks before every commit in this clone' \
	  '' \
	  'Service commands apply to all services when SERVICE is omitted.' \
	  'Database commands always target postgres; start it with make up first.' \
	  'Example: make up SERVICE=postgres' \
	  'Example: make delorean 2026-11-01' \
	  'Example: make delorean 2026-12-01 KEEP_TIMELINE=1' \
	  'Example: make simulate SCENARIO=assignment MODE=step' \
	  'Example: make test RUN="^TestPostUsageBatches$$/^usage_batches_happy_path$$"'

services:
	@$(COMPOSE) config --services

fmt:
	@sh scripts/go-quality.sh fmt

fmt-check:
	@sh scripts/go-quality.sh fmt-check

vet:
	@sh scripts/go-quality.sh vet

check:
	@sh scripts/go-quality.sh check

install-hooks:
	@git config --local core.hooksPath .githooks
	@printf '%s\n' 'Pre-commit Go checks enabled for this clone.'

docs api-docs:
	@$(MAKE) up SERVICE=docs

docs-check:
	@$(COMPOSE) build docs
	@$(COMPOSE) run --rm --no-deps --entrypoint mkdocs docs build --strict --config-file /source/site/mkdocs.yml
	@$(COMPOSE) run --rm --no-deps --entrypoint python docs /source/site/test_publication.py

docs-export:
	@$(COMPOSE) build docs
	@mkdir -p tools/pages
	@$(COMPOSE) run --rm --no-deps --user "$$(id -u):$$(id -g)" \
	  --volume "$(CURDIR)/tools/pages:/export" --entrypoint python docs \
	  /source/site/export_pages.py --site-url $(call shell_quote,$(PAGES_URL)) --output /export

demo-check:
	@docker run --rm --volume "$(CURDIR):/workspace:ro" --workdir /workspace \
	  node:24-alpine node --test tests/demo/*.test.mjs

adminer-check:
	@$(COMPOSE) run --rm --no-deps --volume "$(CURDIR):/workspace:ro" --entrypoint php adminer \
	  -l /workspace/adminer/plugins-enabled/integer-format.php
	@$(COMPOSE) run --rm --no-deps --volume "$(CURDIR):/workspace:ro" --entrypoint php adminer \
	  /workspace/tests/adminer/integer-format.php

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

delorean:
	@set -eu; \
	demo_date=$(call shell_quote,$(DELOREAN_DATE)); \
	year=$${demo_date%%-*}; month_day=$${demo_date#*-}; \
	month=$${month_day%%-*}; day=$${month_day#*-}; \
	case "$$month" in \
	  01|03|05|07|08|10|12) days_in_month=31 ;; \
	  04|06|09|11) days_in_month=30 ;; \
	  02) \
	    days_in_month=28; \
	    if [ $$((year % 4)) -eq 0 ] && { [ $$((year % 100)) -ne 0 ] || [ $$((year % 400)) -eq 0 ]; }; then \
	      days_in_month=29; \
	    fi ;; \
	  *) days_in_month=0 ;; \
	esac; \
	if [ "$$day" -lt 1 ] || [ "$$day" -gt "$$days_in_month" ]; then \
	  printf 'Invalid calendar date: %s\n' "$$demo_date" >&2; exit 2; \
	fi; \
	if [ $(call shell_quote,$(KEEP_TIMELINE)) = 1 ]; then \
	  printf 'DeLorean KEEP_TIMELINE: preserving database history and simulator checkpoints; travelling to %s.\n' "$$E2B_DEMO_NOW"; \
	else \
	  printf 'DeLorean: resetting project volumes and travelling to %s.\n' "$$E2B_DEMO_NOW"; \
	fi
	@set -eu; \
	if [ $(call shell_quote,$(KEEP_TIMELINE)) = 1 ]; then \
	  $(COMPOSE) stop api worker; \
	else \
	  $(COMPOSE) down --volumes --remove-orphans; \
	fi
	@$(MAKE) --no-print-directory up SERVICE= E2B_DEMO_NOW="$$E2B_DEMO_NOW"
	@$(MAKE) --no-print-directory migrate E2B_DEMO_NOW="$$E2B_DEMO_NOW"

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
	      printf 'Documentation portal: http://%s/\n' "$$address"; \
	      printf 'API reference: http://%s/reference/\n' "$$address"; \
	      printf 'Billing demo GUI: http://%s/demo/\n' "$$address"; \
	      printf 'Adminer database UI: http://%s/database/\n' "$$address" ;; \
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
	  all|go|db|e2e) ;; \
	  *) printf 'Unknown test suite "%s". Available suites: all, go, db, e2e.\n' "$$SUITE" >&2; exit 2 ;; \
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
	fi; \
	if [ "$$SUITE" = all ] || [ "$$SUITE" = e2e ]; then \
	  $(MAKE) --no-print-directory e2e-test; \
	fi

e2e-test:
	@bash scripts/e2e.sh

go-test:
	@$(COMPOSE) build api
	@$(COMPOSE) run --rm --no-deps api go test -count=1 -v -run "$$RUN" ./...

db-test: _test-db

api-test:
	@$(COMPOSE) up -d --wait --wait-timeout 120 postgres
	@$(MAKE) --no-print-directory migrate
	@$(COMPOSE) stop worker
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
