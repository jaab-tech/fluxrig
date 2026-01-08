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

.PHONY: all build test lint clean help catalog

all: lint test build

build: lint catalog ## Build fluxrig binary
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

catalog: ## Generate log message catalog
	@echo "Generating log catalog..."
	@go run scripts/catalog_logs.go > ops/docs/internal/log_catalog.csv

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

regression: build ## Run full E2E regression suite (Unified Runner)
	@echo "--------------------------------------------------"
	@echo "Running Regression Suite (Unified)..."
	@test/e2e/run_all.sh

clean: ## Remove build artifacts and temporary files
	@echo "Cleaning..."
	rm -rf bin/
	rm -rf test/test_logs/
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
	rm -f ops/docs/internal/log_catalog.csv
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
# Robot Framework (Validation)
.PHONY: robot
robot: build ## Run Robot Framework validation suite
	@echo "Running Robot Framework tests..."
	@cd test/robot && ./run.sh



.PHONY: clean-robot
clean-robot: ## Clean Robot Framework artifacts
	@rm -rf test/robot/.venv
	@rm -rf test/robot/results
	@rm -f test/robot/log.html test/robot/report.html test/robot/output.xml

.PHONY: openapi
openapi: ## Generate OpenAPI specification
	@echo "Generating OpenAPI spec..."
	@if command -v swag >/dev/null; then \
		swag init -g cmd/fluxrig-mixer/main.go -o ops/docs/public/5_reference --outputTypes yaml; \
	else \
		$(shell go env GOPATH)/bin/swag init -g cmd/fluxrig-mixer/main.go -o ops/docs/public/5_reference --outputTypes yaml; \
	fi
	@mv ops/docs/public/5_reference/swagger.yaml ops/docs/public/5_reference/openapi.yaml
	@echo "Spec generated at ops/docs/public/5_reference/openapi.yaml"
