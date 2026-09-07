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
# Every binary links most of pkg/, so a change there must rebuild it. Depending
# only on cmd/<x> (as these rules once did) left a binary stale after a pkg edit,
# and a stale binary makes a test exercise the old code silently. The generated
# OpenAPI docs are excluded: they are a build output, not an input.
GO_LIB := $(shell find pkg -name "*.go" -not -path "pkg/mixer/api/docs/*")

bin/fluxrig: catalog $(shell find cmd/fluxrig -name "*.go") $(GO_LIB)
	@echo "Building fluxrig..."
	@mkdir -p bin
	go build $(GO_FLAGS) -ldflags "$(LDFLAGS)" -o bin/fluxrig ./cmd/fluxrig

bin/fluxrig-mixer: catalog openapi $(shell find cmd/fluxrig-mixer -name "*.go") $(GO_LIB)
	@echo "Building fluxrig-mixer..."
	@mkdir -p bin
	go build $(GO_FLAGS) -ldflags "$(LDFLAGS)" -o bin/fluxrig-mixer ./cmd/fluxrig-mixer

bin/iso8583-tool: $(shell find cmd/iso8583-tool -name "*.go") $(GO_LIB)
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
	@echo "Build Complete ($(VERSION))"
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

check-no-binaries: ## Fail if a compiled executable is tracked in git
	@./scripts/check_no_binaries.sh

check-no-internal-leaks: ## Fail if a mirrored file names internal-only infrastructure
	@./scripts/check_no_internal_leaks.sh

lint: check-no-binaries check-no-internal-leaks ## Run golangci-lint
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
# The suites shell out to three tools. Without them they fail on their own terms:
# a missing duckdb reports "Rack not found in DB snapshot", which reads as a Rack
# that never registered rather than as a missing tool. Say what is absent first.
	@missing=""; \
	command -v duckdb  >/dev/null || missing="$$missing\n  duckdb   reads the Mixer store        (01_simple, 02_telemetry, 03_registry, 10_iso8583)  https://duckdb.org/docs/installation/"; \
	command -v zig     >/dev/null || missing="$$missing\n  zig      builds the Wasm payload      (12_wasm_polyglot)                                https://ziglang.org/download/"; \
	command -v tshark  >/dev/null || missing="$$missing\n  tshark   replays the PCAP samples     (10_iso8583)                                      apt install tshark"; \
	if [ -n "$$missing" ]; then \
		echo "Missing prerequisites for the regression suite:"; \
		printf "$$missing\n"; \
		exit 1; \
	fi
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
openapi: pkg/mixer/api/docs/.generated ## Generate the OpenAPI specification

# The spec is generated straight from main.go, whose @version annotation stays
# at the 0.0.0-dev placeholder. Stamping the real version here would make the
# checked-in swagger.yaml and docs.go differ from HEAD on every build between
# releases, and the release refuses to run against a dirty tree. The served
# spec reports the real version instead: pkg/mixer/api/server.go assigns it to
# docs.SwaggerInfo.Version from the same ldflag the rest of the binary uses.
pkg/mixer/api/docs/.generated: cmd/fluxrig-mixer/main.go pkg/mixer/api/server.go pkg/mixer/api/types.go
	@echo "Generating OpenAPI spec..."
	@mkdir -p pkg/mixer/api/docs
	@if command -v swag >/dev/null; then \
		swag init -g main.go -d cmd/fluxrig-mixer,pkg/mixer/api --parseDependency --parseInternal --packagePrefix $(MODULE_NAME) -o pkg/mixer/api/docs --outputTypes yaml,go; \
	else \
		$(shell go env GOPATH)/bin/swag init -g main.go -d cmd/fluxrig-mixer,pkg/mixer/api --parseDependency --parseInternal --packagePrefix $(MODULE_NAME) -o pkg/mixer/api/docs --outputTypes yaml,go; \
	fi
	@touch pkg/mixer/api/docs/.generated
	@echo "Spec generated at pkg/mixer/api/docs/"

# Copying the spec into the sibling docs repositories is deliberate, not a
# side effect of building: the copy there is a release artifact carrying a real
# version, and a developer build overwriting it with the placeholder leaves
# those repositories dirty. The release publishes its own stamped copy.
.PHONY: openapi-sync
openapi-sync: openapi ## Copy the generated spec into the docs repos (needs OPENAPI_DEST in .env.local)
	@if [ -z "$(OPENAPI_DEST)" ]; then \
		echo "Nothing to sync — OPENAPI_DEST not set in .env.local"; \
	else \
		mkdir -p $$(dirname $(OPENAPI_DEST)); \
		cp pkg/mixer/api/docs/swagger.yaml $(OPENAPI_DEST); \
		if [ -n "$(STATIC_SYNC_DIR)" ]; then \
			mkdir -p $(STATIC_SYNC_DIR); \
			cp pkg/mixer/api/docs/swagger.yaml $(STATIC_SYNC_DIR)/openapi.yaml; \
			echo "Synced static spec to $(STATIC_SYNC_DIR)/openapi.yaml"; \
		fi; \
		echo "Synced spec to $(OPENAPI_DEST)"; \
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

test-robot-validation-rules: robot-prep build-bin ## Run Robot Spec Validation Suite (what a spec's rules do to real traffic)
	@echo "Running Spec Validation Rules Suite..."
	@cd test/robot && ./run.sh suites/iso8583/validation_rules.robot

test-robot-tlv: build-bin iso8583-tool ## Run Robot ISO8583 TLV Fidelity Suite
	@echo "Running Robot ISO8583 TLV Fidelity Suite..."
	@./test/robot/run.sh test/robot/suites/iso8583/tlv_fidelity.robot

test-robot-coatcheck: build-bin ## Run Robot Coatcheck Suite
	@echo "Running Robot Coatcheck Suite..."
	@./test/robot/run.sh test/robot/suites/iso8583/coatcheck_loop.robot

test-robot-topology: robot-prep build-bin ## Run Robot Topology Suite
	@echo "Running Topology Test..."
	@cd test/robot && ./run.sh suites/topology/cross_rack.robot

test-robot-specs: robot-prep build-bin ## Run Robot Spec Suite (the contract a spec states, and the documents derived from it)
	@echo "Running Spec Suite..."
	@cd test/robot && ./run.sh suites/specs

test-robot-telemetry: robot-prep build-bin ## Run Robot Telemetry Suite
	@echo "Running Telemetry Coverage Test..."
	@cd test/robot && ./run.sh suites/telemetry/force_log_coverage.robot

test-robot-conductor: robot-prep build-bin iso8583-tool ## Run Robot Conductor Suite (payment switch: stress + chaos)
	@echo "Running Conductor Stress + Chaos Suite (chaos needs toxiproxy-server on PATH)..."
	@cd test/robot && ./run.sh suites/conductor

# Pins the most recent Robot run into the documentation site: the report and log
# verbatim under static/, plus a summary the page renders as text.
#
# What lands is a snapshot, not a live status, and an existing one is left alone
# unless REPLACE=1. Re-publishing on every local run would rewrite half a
# megabyte of HTML in the repository each time without telling a reader anything
# new, so replacing a pinned run is a decision rather than a habit.
#
# Failing runs are published too, deliberately. A report that only appears when
# it is green tells a reader nothing about whether the suite is green.
robot-publish: ## Pin the latest Robot run into the docs site (usage: make robot-publish SUITE=roaming [REPLACE=1])
	@if [ -z "$(SUITE)" ]; then echo "SUITE is required, e.g. make robot-publish SUITE=roaming"; exit 1; fi
	@if [ -z "$(ROBOT_REPORTS_STATIC)" ] || [ -z "$(ROBOT_REPORTS_GENERATED)" ] || [ -z "$(ROBOT_PUBLISH_PYTHON)" ] || [ -z "$(ROBOT_PUBLISH_SCRIPT)" ]; then \
		echo "Skipping (ROBOT_REPORTS_* / ROBOT_PUBLISH_* not set - configure .env.local)"; \
	else \
		latest=$$(ls -dt /tmp/fluxrig/robot_run_*/results 2>/dev/null | head -1); \
		if [ -z "$$latest" ]; then echo "No Robot run found under /tmp/fluxrig"; exit 1; fi; \
		echo "Publishing $$latest as '$(SUITE)'..."; \
		flag=""; [ -n "$(REPLACE)" ] && flag="--replace"; \
		$(ROBOT_PUBLISH_PYTHON) $(ROBOT_PUBLISH_SCRIPT) \
			$$flag "$(SUITE)" "$$latest" "$(ROBOT_REPORTS_STATIC)" "$(ROBOT_REPORTS_GENERATED)"; \
	fi

test-robot-roaming: robot-prep build-bin ## Run Robot Roaming Enrichment Suite (network signal, with a CAMARA simulator)
	@echo "Running Roaming Enrichment Suite..."
	@cd test/robot && ./run.sh suites/roaming/roaming.robot

test-robot-roaming-stress: robot-prep build-bin ## Run Robot Roaming Stress Suite (correlation under load; runs on its own, not in test-robot)
	@echo "Running Roaming Stress Suite..."
	@cd test/robot && ./run.sh suites/roaming/stress_load.robot

test-robot: test-robot-iso test-robot-tlv test-robot-coatcheck test-robot-topology test-robot-telemetry test-robot-roaming test-robot-specs test-robot-validation-rules ## Run the functional Robot suites (the performance and chaos suites run on their own)

test-robot-staged: robot-prep build-bin ## Run Robot Staged Load Suite (QoS Validation)
	@echo "Running Staged Load Test..."
	@cd test/robot && ./run.sh suites/iso8583/server_staged_load.robot

test-robot-validation: robot-prep build-bin ## Run Robot Server Validation
	@echo "Running Server Validation..."
	@cd test/robot && ./run.sh suites/iso8583/server_validation.robot
