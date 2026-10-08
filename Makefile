APP_NAME := hank
COMPOSE := docker compose --env-file .env.cloud

.PHONY: tidy fmt build distribution frontend-install frontend-test frontend-build frontend-check build-all run-cloud run-agent run-db-ops migrate-up migrate-status migrate-baseline schema-drift-check loadtest monitoring-test app-sandbox-test

app-sandbox-test:
	scripts/test-app-sandbox.sh

monitoring-test:
	HANK_TEST_ALERTMANAGER=1 go test ./internal/cloud -run TestMonitoringRendererWithPinnedAlertmanager -count=1
	python3 scripts/tests/check-monitoring-test.py
	python3 scripts/tests/configure-monitoring-test.py
	docker run --rm --network none --user "$$(id -u):$$(id -g)" --entrypoint /bin/promtool -v "$(CURDIR)/ops/prometheus:/etc/prometheus:ro" -w /etc/prometheus prom/prometheus:v3.5.0 check config --syntax-only prometheus.yml
	docker run --rm --network none --user "$$(id -u):$$(id -g)" --entrypoint /bin/promtool -v "$(CURDIR)/ops/prometheus:/etc/prometheus:ro" -w /etc/prometheus prom/prometheus:v3.5.0 test rules alerts.test.yml

tidy:
	go mod tidy

fmt:
	gofmt -w ./cmd ./internal

build: frontend-build
	go build ./...

# Package binaries together with preserved dependency terms and MPL source.
distribution: frontend-build
	mkdir -p dist
	go build -o dist/hank-server ./cmd/hank-server
	go build -o dist/hank-agent ./cmd/hank-agent
	go build -o dist/hank-db-ops ./cmd/hank-db-ops
	python3 scripts/prepare-distribution-notices.py dist

frontend-install:
	npm --prefix web/dashboard install

frontend-test:
	npm --prefix web/dashboard run test:run

frontend-build:
	npm --prefix web/dashboard run build

frontend-check:
	npm --prefix web/dashboard run check

build-all: build

run-cloud:
	go run ./cmd/hank-server

run-agent:
	go run ./cmd/hank-agent

run-db-ops:
	go run ./cmd/hank-db-ops

migrate-up:
	@if [ -f .env.cloud ] && command -v docker >/dev/null 2>&1 && $(COMPOSE) ps --services --status running 2>/dev/null | grep -qx postgres; then \
		$(COMPOSE) run -T --rm --entrypoint /usr/local/bin/hank-server cloud migrate up; \
	else \
		set -a; [ ! -f .env.cloud ] || . ./.env.cloud; set +a; \
		go run ./cmd/hank-server migrate up; \
	fi

migrate-status:
	@if [ -f .env.cloud ] && command -v docker >/dev/null 2>&1 && $(COMPOSE) ps --services --status running 2>/dev/null | grep -qx postgres; then \
		$(COMPOSE) run -T --rm --entrypoint /usr/local/bin/hank-server cloud migrate status --strict; \
	else \
		set -a; [ ! -f .env.cloud ] || . ./.env.cloud; set +a; \
		go run ./cmd/hank-server migrate status --strict; \
	fi

migrate-baseline:
	@if [ -f .env.cloud ] && command -v docker >/dev/null 2>&1 && $(COMPOSE) ps --services --status running 2>/dev/null | grep -qx postgres; then \
		$(COMPOSE) run -T --rm --entrypoint /usr/local/bin/hank-server cloud migrate baseline; \
	else \
		set -a; [ ! -f .env.cloud ] || . ./.env.cloud; set +a; \
		go run ./cmd/hank-server migrate baseline; \
	fi

schema-drift-check:
	scripts/schema-drift-check.sh

loadtest:
	go test ./tools/loadtest
