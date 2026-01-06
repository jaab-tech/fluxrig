# Versioning
VERSION ?= $(shell cat VERSION 2>/dev/null || echo "0.0.0-dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
# Check if dirty
ifneq ($(shell git status --porcelain 2>/dev/null),)
	DIRTY := -dirty
endif

MODULE_NAME := github.com/jaab-tech/fluxrig

# Go settings
GO_FLAGS := -v
LDFLAGS := -w -s \
	-X '$(MODULE_NAME)/pkg/version.Version=$(VERSION)' \
	-X '$(MODULE_NAME)/pkg/version.Commit=$(COMMIT)' \
	-X '$(MODULE_NAME)/pkg/version.BuildDate=$(DATE)' \
	-X '$(MODULE_NAME)/pkg/version.Dirty=$(DIRTY)'

.PHONY: all build test lint clean help

all: lint test build

build: lint ## Build fluxrig binary
	@echo "--------------------------------------------------"
	@echo "Building fluxrig..."
	@echo "  Version:  $(VERSION)"
	@echo "  Commit:   $(COMMIT)"
	@echo "  Date:     $(DATE)"
	@echo "  Dirty:    $(DIRTY)"
	@echo "--------------------------------------------------"
	mkdir -p bin
	go build $(GO_FLAGS) -ldflags "$(LDFLAGS)" -o bin/fluxrig ./cmd/fluxrig

# Mixer (Requires CGO)
	go build $(GO_FLAGS) -ldflags "$(LDFLAGS)" -o bin/fluxrig-mixer ./cmd/fluxrig-mixer

test: ## Run unit tests with race detection and coverage
	@echo "Running tests..."
	@mkdir -p test/test_logs
	go test -race -coverprofile=test/test_logs/coverage.out $$(go list ./... | grep -v '/test/utils' | grep -v 'cmd/fluxrig$$' | grep -v 'cmd/fluxrig-mixer$$')
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

regression: build ## Run full E2E regression suite (Simple, Conflict, Offline, CLI)
	@echo "--------------------------------------------------"
	@echo "Running Regression Suite..."
	@echo "--------------------------------------------------"
	@echo "--------------------------------------------------"
	@echo ">>> [1/8] Running Simple E2E..."
	@./test/e2e_simple/run.sh || { echo "❌ Simple E2E Failed"; exit 1; }
	@echo ">>> [2/8] Running Registry E2E..."
	@./test/e2e_registry/run.sh || { echo "❌ Registry E2E Failed"; exit 1; }
	@echo ">>> [3/8] Running Conflict E2E..."
	@./test/e2e_conflict/run.sh || { echo "❌ Conflict E2E Failed"; exit 1; }
	@echo ">>> [4/8] Running Offline E2E..."
	@./test/e2e_offline/run.sh || { echo "❌ Offline E2E Failed"; exit 1; }
	@echo ">>> [5/8] Running CLI E2E..."
	@./test/e2e_cli/run.sh || { echo "❌ CLI E2E Failed"; exit 1; }
	@echo "--------------------------------------------------"
	@echo ">>> [6/8] Running Telemetry E2E..."
	@./test/e2e_telemetry/run.sh || { echo "❌ Telemetry E2E Failed"; exit 1; }
	@echo "--------------------------------------------------"
	@echo ">>> [7/8] Running Simple TCP E2E..."
	@./test/e2e_simple_tcp/run.sh || { echo "❌ Simple TCP E2E Failed"; exit 1; }
	@echo "--------------------------------------------------"
	@echo ">>> [8/8] Running Bento Load E2E..."
	@./test/e2e_load/run.sh || { echo "❌ Bento Load E2E Failed"; exit 1; }
	@echo "--------------------------------------------------"
	@echo "✅ REGRESSION SUITE PASSED"
	@echo "--------------------------------------------------"

clean: ## Remove build artifacts and temporary files
	@echo "Cleaning..."
	rm -rf bin/
	rm -rf test/test_logs/
	# Clean E2E test artifacts (recursive data/logs)
	find test/e2e_* -name "data" -type d -exec rm -rf {} +
	find test/e2e_* -name "logs" -type d -exec rm -rf {} +
	find test/e2e_* -name "*.log" -delete
	rm -rf test/work/
	rm -rf test/e2e_*/work
	rm -rf test/e2e_*/work_*
	rm -rf test/test_logs/
	rm -rf test/test_logs/
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
	go clean -cache

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

help: ## Display this help screen
	@grep -h -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
