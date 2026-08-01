# Versioning
VERSION ?= $(shell cat VERSION)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
# Check if dirty
ifneq ($(shell git status --porcelain 2>/dev/null),)
	DIRTY := -dirty
endif

MODULE_NAME := github.com/jaab-tech/fluxrig

# Go settings
GO_FLAGS ?= -v
# Use local GOPATH/GOMODCACHE if system is locked
GOMODCACHE ?= $(shell pwd)/.gomod_cache
export GOMODCACHE

LDFLAGS := -w -s \
	-X '$(MODULE_NAME)/pkg/version.Version=$(VERSION)' \
	-X '$(MODULE_NAME)/pkg/version.Commit=$(COMMIT)' \
	-X '$(MODULE_NAME)/pkg/version.BuildDate=$(DATE)' \
	-X '$(MODULE_NAME)/pkg/version.Dirty=$(DIRTY)'

ifeq ($(shell uname -s),Darwin)
	LDFLAGS += -extldflags=-Wl,-w
endif

.PHONY: all build build-bin test lint clean distclean help catalog

all: lint test build

# Binary-specific targets to allow parallel builds (make -j)
bin/fluxrig: catalog $(shell find cmd/fluxrig -name "*.go")
	@echo "Building fluxrig..."
	@mkdir -p bin
	go build $(GO_FLAGS) -ldflags "$(LDFLAGS)" -o bin/fluxrig ./cmd/fluxrig

bin/fluxrig-mixer: catalog openapi $(shell find cmd/fluxrig-mixer -name "*.go")
	@echo "Building fluxrig-mixer..."
	@mkdir -p bin
	go build $(GO_FLAGS) -ldflags "$(LDFLAGS)" -o bin/fluxrig-mixer ./cmd/fluxrig-mixer

bin/iso8583-tool: cmd/iso8583-tool/main.go
	@echo "Building iso8583-tool..."
	@mkdir -p bin
	go build -o bin/iso8583-tool ./cmd/iso8583-tool

# ── Lean variant (-tags nobento) ─────────────────────────────────────────────
# The Bento gear is ~18MB of the ~32MB stripped Rack binary (~57%) and pulls
# protobuf, cue, avro and gojq transitively. Deployments that do not use the
# `bento` gear can ship these instead. A scenario declaring `type: bento` fails
# loudly ("unknown gear type: bento") on a lean binary rather than misbehaving.
bin/fluxrig-lite: catalog $(shell find cmd/fluxrig -name "*.go")
	@echo "Building fluxrig-lite (no bento)..."
	@mkdir -p bin
	go build $(GO_FLAGS) -tags nobento -ldflags "$(LDFLAGS)" -o bin/fluxrig-lite ./cmd/fluxrig

# Only the Rack needs a lean variant: the Mixer is the control plane and never
# links pkg/gears (and therefore never links Bento), so a `nobento` Mixer is
# byte-identical to the normal one.
build-lite: bin/fluxrig-lite ## Build the lean Rack without the Bento gear
	@echo "--------------------------------------------------"
	@echo "Lean Build Complete (-tags nobento)"
	@ls -lh bin/fluxrig-lite | awk '{printf "  %-28s %s\n", $$9, $$5}'
	@echo "--------------------------------------------------"

build-all-variants: build-bin build-lite ## Build both the full and lean Rack binaries
	@echo "--------------------------------------------------"
	@ls -lh bin/fluxrig bin/fluxrig-lite bin/fluxrig-mixer 2>/dev/null | awk '{printf "  %-28s %s\n", $$9, $$5}'
	@echo "--------------------------------------------------"

build: lint bin/fluxrig bin/fluxrig-mixer bin/iso8583-tool ## Build all binaries
	@echo "--------------------------------------------------"
	@echo "Build Complete (v$(VERSION))"
	@echo "--------------------------------------------------"

build-bin: bin/fluxrig bin/fluxrig-mixer bin/iso8583-tool ## Build all binaries without linting
	@echo "--------------------------------------------------"
	@echo "Build Complete (No Lint)"
	@echo "--------------------------------------------------"

# Workspace sync destinations — set in .env.local (gitignored, never committed).
# Copy .env.local.example to .env.local and set paths for your local setup.
# Targets that use these variables skip silently when left unset.
-include .env.local
LOG_CATALOG   ?=
OPENAPI_DEST  ?=
STATIC_SYNC_DIR ?=
GEAR_DOCS_DIR ?=

gear-docs: build-bin ## Refresh AUTOGEN manifest sections in the gear reference docs
	@if [ -z "$(GEAR_DOCS_DIR)" ]; then \
		echo "Skipping gear docs (GEAR_DOCS_DIR not set — configure .env.local)"; \
	else \
		echo "Refreshing gear manifest docs in $(GEAR_DOCS_DIR)..."; \
		./bin/fluxrig gears doc --write "$(GEAR_DOCS_DIR)"; \
	fi

catalog: ## Generate log message catalog
	@# The redirect target MUST stay quoted. The shell parses this whole if/else
	@# before running it, so an unquoted empty $(LOG_CATALOG) becomes `> ` — a
	@# syntax error that kills the recipe even when the skip branch would be taken
	@# (i.e. `make build` failed on any checkout without .env.local, such as CI).
	@if [ -z "$(LOG_CATALOG)" ]; then \
		echo "Skipping log catalog (LOG_CATALOG not set — configure .env.local)"; \
	else \
		echo "Generating log catalog..."; \
		go run scripts/catalog_logs.go > "$(LOG_CATALOG)" || true; \
	fi

test: ## Run unit tests with race detection and coverage
	@echo "Running tests..."
	@mkdir -p test/test_logs
	go test -race -ldflags "$(LDFLAGS)" -coverprofile=test/test_logs/coverage.out $$(go list ./... | grep -v '/test/utils' | grep -v 'cmd/fluxrig$$' | grep -v 'cmd/fluxrig-mixer$$')
	@go tool cover -func=test/test_logs/coverage.out | grep total | awk '{print "Total Coverage: " $$3}'

lint: ## Run golangci-lint
	@echo "Linting..."
	@if command -v golangci-lint >/dev/null; then \
		golangci-lint run ./...; \
	else \
		$(shell go env GOPATH)/bin/golangci-lint run ./...; \
	fi

install: build ## Install binaries to $GOPATH/bin
	@echo "Installing..."
	cp bin/fluxrig $(shell go env GOPATH)/bin/fluxrig
	cp bin/fluxrig-mixer $(shell go env GOPATH)/bin/fluxrig-mixer
	@echo "Installed to $(shell go env GOPATH)/bin"

verify: build ## Run quick E2E verification script
	@./test/debug_e2e.sh

regression: build ## Run full E2E regression suite (Unified Runner)
	@echo "--------------------------------------------------"
	@echo "Running Regression Suite (Unified)..."
	@test/e2e/run_all.sh

clean: ## Remove local build artifacts (Fast)
	@echo "Cleaning local artifacts..."
	rm -rf bin/
	rm -rf test/test_logs/
	rm -rf /tmp/fluxrig*
	# Clean E2E test artifacts (recursive data/logs)
	find test/e2e -name "data" -type d -exec rm -rf {} +
	find test/e2e -name "logs" -type d -exec rm -rf {} +
	find test/e2e -name "*.log" -delete
	rm -rf test/e2e/*/work
	rm -rf test/e2e/*/work_*
	# Clean comprehensive test artifacts
	rm -rf pkg/ingest/comprehensive-mixer/
	rm -rf pkg/ingest/test-mixer/
	$(MAKE) clean-robot
	rm -rf test_data/
	rm -rf data/
	rm -rf logs/
	rm -f coverage.out
	rm -f *.log
	rm -f stdout
	rm -f mixer_debug.pid
	rm -f fluxrig_test.toml
	rm -f cluster.key
	rm -f cluster.key.pub
	@# LOG_CATALOG is deliberately not removed here. It resolves to a path inside
	@# the ops repository where the catalog is a tracked file, so deleting it makes
	@# `make clean` stage a deletion in a sibling repo. `make catalog` regenerates it.
	rm -f pkg/mixer/api/docs/.generated

distclean: clean ## Full cleanup including caches (Slow)
	@echo "Cleaning caches..."
	@if [ -d .gomod_cache ]; then go clean -modcache; fi
	rm -rf .gomod_cache
	go clean -cache

examples-validate: ## Validate example configuration files
	@echo "Validating example configs..."
	go run scripts/validate_examples/main.go examples/configs/fluxrig.toml.example
	go run scripts/validate_examples/main.go examples/configs/fluxrig-mixer.toml.example

# Python / Test Automation
VENV := .venv
PIP := $(VENV)/bin/pip
ROBOT := $(VENV)/bin/robot

$(VENV):
	@echo "Creating Python virtual environment..."
	python3 -m venv $(VENV)
	$(PIP) install --upgrade pip
	$(PIP) install -r test/robot/requirements.txt

test-api: $(VENV) build ## Run Robot Framework API tests
	@echo "Running API tests..."
	# Start Mixer in background
	@mkdir -p test/test_logs
	@./bin/fluxrig-mixer > test/test_logs/mixer.log 2>&1 & echo $$! > mixer.pid; \
	PID=$$(cat mixer.pid); \
	echo "Mixer started with PID $$PID"; \
	sleep 2; \
	$(ROBOT) --outputdir test/robot/api test/robot/api/api_tests.robot || EXIT_CODE=$$?; \
	kill $$PID; \
	rm mixer.pid; \
	exit $$EXIT_CODE

test-scenarios: $(VENV) build ## Run Robot Framework Scenarios (Multi-Rack)
	@echo "Running Scenario tests..."
	@mkdir -p test/robot/scenarios
	$(ROBOT) --outputdir test/robot/scenarios test/robot/scenarios/setup_scenarios.robot

robot-prep: ## Kill lingering Mixer/Rack/NATS/ISO processes (Hardening)
	@echo "--------------------------------------------------"
	@echo "Locking down environment for Robot execution..."
	-@pkill -9 fluxrig 2>/dev/null || true
	-@pkill -9 fluxrig-mixer 2>/dev/null || true
	-@pkill -9 iso8583-tool 2>/dev/null || true
	-@lsof -ti:8080,4222,8583,54321,9120 2>/dev/null | xargs kill -9 2>/dev/null || true
	@echo "Environment Secured."
	@echo "--------------------------------------------------"
	@sleep 1

help: ## Display this help screen
	@grep -h -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
# Robot Framework (Validation)
.PHONY: robot
robot: build-bin iso8583-tool ## Run Robot Framework validation suite
	@echo "Running Robot Framework tests..."
	@cd test/robot && ./run.sh




.PHONY: clean-robot
clean-robot: ## Clean Robot Framework artifacts
	@rm -rf test/robot/.venv
	@rm -rf test/robot/results # Legacy symlink
	@rm -f test/robot/log.html test/robot/report.html test/robot/output.xml
	@find test/robot -name "results" -type l -delete
	@find test/robot -name "work" -type l -delete

.PHONY: openapi
openapi: pkg/mixer/api/docs/.generated ## Generate OpenAPI specification and sync to ops

pkg/mixer/api/docs/.generated: cmd/fluxrig-mixer/main.go pkg/mixer/api/server.go pkg/mixer/api/types.go VERSION
	@echo "Generating OpenAPI spec for version $(VERSION) (Clean Source Strategy)..."
	@cp cmd/fluxrig-mixer/main.go cmd/fluxrig-mixer/main_gen.go
	@sed -i '' 's/@version 0.0.0-dev/@version $(VERSION)/' cmd/fluxrig-mixer/main_gen.go
	@mkdir -p pkg/mixer/api/docs
	@if command -v swag >/dev/null; then \
		swag init -g main_gen.go -d cmd/fluxrig-mixer,pkg/mixer/api --parseDependency --parseInternal --packagePrefix $(MODULE_NAME) -o pkg/mixer/api/docs --outputTypes yaml,go; \
	else \
		$(shell go env GOPATH)/bin/swag init -g main_gen.go -d cmd/fluxrig-mixer,pkg/mixer/api --parseDependency --parseInternal --packagePrefix $(MODULE_NAME) -o pkg/mixer/api/docs --outputTypes yaml,go; \
	fi; \
	EXIT_CODE=$$?; \
	rm cmd/fluxrig-mixer/main_gen.go; \
	if [ $$EXIT_CODE -ne 0 ]; then exit $$EXIT_CODE; fi
	@touch pkg/mixer/api/docs/.generated
	@if [ -z "$(OPENAPI_DEST)" ]; then \
		echo "Spec generated at pkg/mixer/api/docs/ (skipping sync — OPENAPI_DEST not set in .env.local)"; \
	else \
		mkdir -p $$(dirname $(OPENAPI_DEST)); \
		cp pkg/mixer/api/docs/swagger.yaml $(OPENAPI_DEST); \
		if [ -n "$(STATIC_SYNC_DIR)" ]; then \
			mkdir -p $(STATIC_SYNC_DIR); \
			cp pkg/mixer/api/docs/swagger.yaml $(STATIC_SYNC_DIR)/openapi.yaml; \
			echo "Synced static spec to $(STATIC_SYNC_DIR)/openapi.yaml"; \
		fi; \
		echo "Spec generated at pkg/mixer/api/docs/ and synced to $(OPENAPI_DEST)"; \
	fi

.PHONY: iso8583-tool
iso8583-tool: ## Build iso8583-tool (Load Gen & Echo Server)
	@echo "Building iso8583-tool..."
	@go build -o bin/iso8583-tool ./cmd/iso8583-tool

test-robot-perf: build-bin iso8583-tool ## Run Robot Performance Suite
	@echo "Running Robot Performance Suite (Staged Load)..."
	@./test/robot/run.sh test/robot/suites/iso8583/server_staged_load.robot

test-robot-iso: build-bin iso8583-tool ## Run Robot ISO8583 Suite
	@echo "Running Robot ISO8583 Suite (Validation)..."
	@./test/robot/run.sh test/robot/suites/iso8583/server_validation.robot

test-robot-coatcheck: build-bin ## Run Robot Coatcheck Suite
	@echo "Running Robot Coatcheck Suite..."
	@./test/robot/run.sh test/robot/suites/iso8583/coatcheck_loop.robot

test-robot-topology: robot-prep build-bin ## Run Robot Topology Suite
	@echo "Running Topology Test..."
	@cd test/robot && ./run.sh suites/topology/cross_rack.robot

test-robot-telemetry: robot-prep build-bin ## Run Robot Telemetry Suite
	@echo "Running Telemetry Coverage Test..."
	@cd test/robot && ./run.sh suites/telemetry/force_log_coverage.robot

test-robot-conductor: robot-prep build-bin iso8583-tool ## Run Robot Conductor Suite (payment switch: stress + chaos)
	@echo "Running Conductor Stress + Chaos Suite (chaos needs toxiproxy-server on PATH)..."
	@cd test/robot && ./run.sh suites/conductor

test-robot: test-robot-iso test-robot-coatcheck test-robot-topology test-robot-telemetry ## Run all Robot Framework suites

test-robot-staged: robot-prep build-bin ## Run Robot Staged Load Suite (QoS Validation)
	@echo "Running Staged Load Test..."
	@cd test/robot && ./run.sh suites/iso8583/server_staged_load.robot

test-robot-validation: robot-prep build-bin ## Run Robot Server Validation
	@echo "Running Server Validation..."
	@cd test/robot && ./run.sh suites/iso8583/server_validation.robot
