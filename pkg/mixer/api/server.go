// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"
	httpSwagger "github.com/swaggo/http-swagger"
	"gopkg.in/yaml.v3"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/controller"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/gears"
	_ "github.com/jaab-tech/fluxrig/pkg/mixer/api/docs" // Swagger docs
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/registry"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
	"github.com/jaab-tech/fluxrig/pkg/version"
)

type Server struct {
	reg           registry.Registry
	pub           message.Publisher
	signer        *pki.ClusterKey
	scenarioCtrl  controller.ScenarioManager
	metricsCache  *telemetry.MetricsCache
	mixerID       uuid.UUID
	mixerEntityID uuid.UUID
	cfg           *config.MixerConfig
	wasmCatalog   WasmCatalog
	gearFactory   *gears.Factory
}

// WasmCatalog defines the interface for the Wasm Catalog to avoid circular imports if needed.
type WasmCatalog interface {
	Import(ctx context.Context, payload []byte, allowUnsigned bool) (any, error)
	List() (any, error)
}

func NewServer(reg registry.Registry, pub message.Publisher, signer *pki.ClusterKey,
	sc controller.ScenarioManager, cache *telemetry.MetricsCache,
	mixerID uuid.UUID, mixerEntityID uuid.UUID, cfg *config.MixerConfig, wasmCat WasmCatalog) *Server {
	return &Server{
		reg: reg, pub: pub, signer: signer, scenarioCtrl: sc,
		metricsCache: cache, mixerID: mixerID, mixerEntityID: mixerEntityID, cfg: cfg,
		wasmCatalog: wasmCat,
		// The gear manifest catalog is static (built into the binary), so a
		// single factory serves it. The Mixer knows the same gears a Rack of
		// the same build does.
		gearFactory: gears.NewFactory(),
	}
}

func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()

	// Middleware: CORS/Recovery/Logging logic can be added here

	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/config", s.handleConfig)
	mux.HandleFunc("GET /api/v1/racks", s.handleRacks)
	mux.HandleFunc("DELETE /api/v1/racks/{id}", s.handleRackAction)
	mux.HandleFunc("POST /api/v1/racks/{id}/{action}", s.handleRackAction)
	mux.HandleFunc("GET /api/v1/telemetry/{type}", s.handleTelemetry)
	mux.HandleFunc("GET /api/v1/entities/stats", s.handleEntityStats)
	mux.HandleFunc("POST /api/v1/scenario/import", s.handleScenarioImport)
	mux.HandleFunc("GET /api/v1/scenario/active", s.handleScenarioActive)
	mux.HandleFunc("GET /api/v1/topology/status", s.handleTopologyStatus)
	mux.HandleFunc("GET /api/v1/topology/list", s.handleTopologyList)
	mux.HandleFunc("POST /api/v1/wasm/import", s.handleWasmImport)
	mux.HandleFunc("GET /api/v1/wasm/catalog", s.handleWasmCatalog)
	mux.HandleFunc("GET /api/v1/gears", s.handleGears)
	mux.HandleFunc("GET /api/v1/gears/{type}", s.handleGearManifest)

	// Swagger UI
	mux.Handle("/swagger/", httpSwagger.WrapHandler)

	// Log using global/standard logger which is slog at this point
	slog.Info("Mixer Control Plane listening", "addr", addr)

	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var opErr error
			if err := c.Control(func(fd uintptr) {
				opErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			}); err != nil {
				return err
			}
			return opErr
		},
	}
	// Resilient Bind-Retry Loop
	// Handles transient port conflicts on macOS during rapid CI cycles.
	var l net.Listener
	var err error
	maxAttempts := 3
	for i := 1; i <= maxAttempts; i++ {
		l, err = lc.Listen(context.Background(), "tcp", addr)
		if err == nil {
			break
		}
		if i < maxAttempts {
			slog.Info("Mixer API bind failed, retrying...", "attempt", i, "addr", addr, "error", err)
			time.Sleep(250 * time.Millisecond)
		}
	}

	if err != nil {
		return fmt.Errorf("mixer API bind failed after %d attempts: %w", maxAttempts, err)
	}

	readTimeout, _ := time.ParseDuration(s.cfg.API.ReadHeaderTimeout)
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: readTimeout,
	}

	if s.cfg.API.TLSCertFile != "" && s.cfg.API.TLSKeyFile != "" {
		slog.Info("Enabling HTTPS for API", "cert", s.cfg.API.TLSCertFile)
		// Clean close of the TCP listener we just opened, as ListenAndServeTLS creates its own
		// OR we can use ServeTLS with the existing listener. Let's use ServeTLS for consistency with 'l'
		return server.ServeTLS(l, s.cfg.API.TLSCertFile, s.cfg.API.TLSKeyFile)
	}

	return server.Serve(l)
}

// handleHealth godoc
// @Summary Health Check
// @Description Returns the operational status of the Mixer
// @Tags core
// @Produce json
// @Success 200 {object} HealthResponse
// @Router /health [get]
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(HealthResponse{
		Status:    "ok",
		Version:   version.String(),
		MachineID: s.mixerID,
		EntityID:  s.mixerEntityID,
	})
}

// handleConfig godoc
// @Summary Get Configuration
// @Description Returns the current Mixer configuration
// @Tags core
// @Produce json
// @Success 200 {object} config.MixerConfig
// @Router /config [get]
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if s.cfg == nil {
		http.Error(w, "Config not available", http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(s.cfg)
}

// handleRacks godoc
// @Summary List Racks
// @Description Returns a list of all known Racks
// @Tags registry
// @Produce json
// @Param status query string false "Filter by status (active, pending, offline)"
// @Success 200 {array} api.RackResponse
// @Router /racks [get]
// @externalDocs.description Node Architecture
// @externalDocs.url https://fluxrig.org/docs/architecture/nodes
func (s *Server) handleRacks(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		status := r.URL.Query().Get("status")
		list, err := s.reg.List(r.Context(), status)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(list)
		return
	}
	// POST: Manual Registration (if needed)
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

// handleRackAction godoc
// @Summary Rack Actions
// @Description Perform actions on Racks (Approve, Suspend, Activate, Remove, Log-Level). Note: This single endpoint handles multiple actions for simplicity in this Alpha version.
// @Tags registry
// @Accept json
// @Produce json
// @Param id path string true "Rack ID"
// @Param action path string false "Action (approve, suspend, activate)"
// @Param body body ApproveRequest false "Approve Request Body"
// @Success 200 {object} ActionResponse
// @Router /racks/{id}/{action} [post]
func (s *Server) handleRackAction(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	action := r.PathValue("action")

	id, err := uuid.Parse(idStr)
	// If not a valid UUID, we'll try to use it as a Name in some actions (like DELETE)
	isUUID := err == nil

	// Handle DELETE /racks/{id} (where id could be Name)
	if r.Method == http.MethodDelete && action == "" {
		if isUUID {
			err = s.reg.Remove(r.Context(), id)
		} else {
			err = s.reg.RemoveByName(r.Context(), idStr)
		}

		if err != nil {
			if err == registry.ErrNotFound {
				http.Error(w, "Rack not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"removed"}`))
		return
	}

	if !isUUID {
		http.Error(w, "Invalid UUID required for this action", http.StatusBadRequest)
		return
	}

	// Handle POST /racks/{id}/{action}
	if r.Method == http.MethodPost {
		// Definition of SetLogLevelRequest
		type SetLogLevelRequest struct {
			Level string `json:"level"`
		}

		switch action {
		case "approve":
			var req ApproveRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "Invalid JSON", http.StatusBadRequest)
				return
			}

			rack, err := s.reg.Approve(r.Context(), id, req.Name)
			if err != nil {
				if err == registry.ErrNotFound {
					http.Error(w, "Rack not found", http.StatusNotFound)
					return
				}
				slog.Error("Approve failed", "error", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			var passportBytes []byte
			if s.signer != nil {
				rackState := pki.RackState{
					MixerID:     s.mixerID,
					MachineID:   rack.MachineID,
					Name:        rack.Name,
					Status:      rack.Status, // "active"
					Secret:      rack.Secret,
					MixerPublic: s.signer.Public,
				}
				if env, err := s.signer.Sign(&rackState); err == nil {
					if b, err := cbor.Marshal(env); err == nil {
						passportBytes = b
					}
				}
			}

			// NOTIFY RACK
			s.publishStatus(id, "active", "approved", passportBytes)
			_ = json.NewEncoder(w).Encode(rack)
			return

		case "suspend":
			if err := s.reg.UpdateStatus(r.Context(), id, "inactive"); err != nil {
				if err == registry.ErrNotFound {
					http.Error(w, "Rack not found", http.StatusNotFound)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			var passportBytes []byte
			if rack, err := s.reg.Get(r.Context(), id); err == nil && s.signer != nil {
				rackState := pki.RackState{
					MixerID:     s.mixerID,
					MachineID:   rack.MachineID,
					Name:        rack.Name,
					Status:      rack.Status, // "inactive"
					Secret:      rack.Secret,
					MixerPublic: s.signer.Public,
				}
				if env, err := s.signer.Sign(&rackState); err == nil {
					if b, err := cbor.Marshal(env); err == nil {
						passportBytes = b
					}
				}
			}

			// NOTIFY RACK
			s.publishStatus(id, "inactive", "suspended", passportBytes)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"suspended"}`))
			return

		case "activate":
			if err := s.reg.UpdateStatus(r.Context(), id, "active"); err != nil {
				if err == registry.ErrNotFound {
					http.Error(w, "Rack not found", http.StatusNotFound)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			var passportBytes []byte
			if rack, err := s.reg.Get(r.Context(), id); err == nil && s.signer != nil {
				rackState := pki.RackState{
					MixerID:     s.mixerID,
					MachineID:   rack.MachineID,
					Name:        rack.Name,
					Status:      rack.Status, // "active"
					Secret:      rack.Secret,
					MixerPublic: s.signer.Public,
				}
				if env, err := s.signer.Sign(&rackState); err == nil {
					if b, err := cbor.Marshal(env); err == nil {
						passportBytes = b
					}
				}
			}

			// NOTIFY RACK
			s.publishStatus(id, "active", "activated", passportBytes)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"activated"}`))
			return

		case "log-level":
			var req SetLogLevelRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "Invalid JSON", http.StatusBadRequest)
				return
			}

			// Validate Level
			validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true, "trace": true}
			if !validLevels[strings.ToLower(req.Level)] {
				http.Error(w, "Invalid Log Level", http.StatusBadRequest)
				return
			}

			// Get current rack to find status
			rack, err := s.reg.Get(r.Context(), id)
			if err != nil {
				if err == registry.ErrNotFound {
					http.Error(w, "Rack not found", http.StatusNotFound)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			// NOTIFY RACK
			s.publishStatus(id, rack.Status, "set_log_level:"+req.Level, nil)

			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "level": req.Level})
			return

		case "shutdown":
			// Get current rack to find status
			rack, err := s.reg.Get(r.Context(), id)
			if err != nil {
				if err == registry.ErrNotFound {
					http.Error(w, "Rack not found", http.StatusNotFound)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			// NOTIFY RACK with "agent:shutdown" command
			s.publishStatus(id, rack.Status, "agent:shutdown", nil)

			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "shutdown_command_sent"})
		}
	}

	http.Error(w, "Not found", http.StatusNotFound)
}

// handleTelemetry godoc
// @Summary Query Telemetry
// @Description Query Logs or Metrics from DuckDB
// @Tags telemetry
// @Produce json
// @Param type path string true "Type of telemetry (logs, metrics)"
// @Param entity query string false "Filter by Entity Name"
// @Param min_level query string false "Filter by Level (logs only)"
// @Param since query string false "Start time (e.g. 1h, 2023-01-01T00:00:00Z)"
// @Param limit query int false "Max records (default 100)"
// @Success 200 {array} map[string]interface{}
// @Router /telemetry/{type} [get]
func (s *Server) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	// /api/v1/telemetry/logs or /metrics
	path := r.PathValue("type")
	q := r.URL.Query()

	limitStr := q.Get("limit")
	limit := 100
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil {
			limit = v
		}
	}

	parseTime := func(k string) time.Time {
		val := q.Get(k)
		if val == "" {
			return time.Time{}
		}
		// Try RFC3339
		if t, err := time.Parse(time.RFC3339, val); err == nil {
			return t
		}
		// Try Duration (relative to now)
		if d, err := time.ParseDuration(val); err == nil {
			return time.Now().Add(-d)
		}
		return time.Time{}
	}

	if path == "logs" {
		query := registry.LogQuery{
			Limit:      limit,
			MinLevel:   q.Get("min_level"),
			EntityName: q.Get("entity"),
			Since:      parseTime("since"),
			Until:      parseTime("until"),
		}

		logs, err := s.reg.QueryLogs(r.Context(), query)
		if err != nil {
			slog.Error("QueryLogs failed", "error", err, "query", query)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := json.NewEncoder(w).Encode(logs); err != nil {
			slog.Error("API: Encode Logs failed", "error", err)
		}
		return
	}

	if path == "metrics" {
		query := registry.MetricQuery{
			Limit:      limit,
			Name:       q.Get("name"),
			EntityName: q.Get("entity"),
			Since:      parseTime("since"),
			Until:      parseTime("until"),
		}

		metrics, err := s.reg.QueryMetrics(r.Context(), query)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(metrics)
		return
	}

	http.Error(w, "Not found", http.StatusNotFound)
}

// handleEntityStats godoc
// @Summary Get Entity Stats
// @Description Returns cached metrics for a specific entity or all entities.
// @Tags telemetry
// @Produce json
// @Param id query string false "Entity ID"
// @Success 200 {object} map[string]interface{}
// @Router /entities/stats [get]
func (s *Server) handleEntityStats(w http.ResponseWriter, r *http.Request) {
	if s.metricsCache == nil {
		http.Error(w, "Metrics cache not initialized", http.StatusServiceUnavailable)
		return
	}

	idStr := r.URL.Query().Get("id")
	if idStr != "" {
		id, err := uuid.Parse(idStr)
		if err != nil {
			http.Error(w, "Invalid ID", http.StatusBadRequest)
			return
		}

		stats := s.metricsCache.GetStats(id)
		if stats == nil {
			http.Error(w, "Entity stats not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(stats)
		return
	}

	// List all
	ids := s.metricsCache.GetAllStats()
	_ = json.NewEncoder(w).Encode(ids)
}

// handleScenarioImport godoc
// @Summary Import Scenario
// @Description Upload a YAML scenario definition to update the topology.
// @Tags scenario
// @Accept application/x-yaml
// @Produce json
// @Param dry_run query boolean false "Validate without applying"
// @Param activate query boolean false "Immediately activate after import"
// @Param body body registry.Scenario true "Scenario YAML"
// @Success 200 {object} ScenarioImportResponse
// @Router /scenario/import [post]
func (s *Server) handleScenarioImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dryRunVal := r.URL.Query().Get("dry_run")
	dryRun := dryRunVal == "true" || dryRunVal == "1"

	activateVal := r.URL.Query().Get("activate")
	shouldActivate := activateVal == "true" || activateVal == "1"

	// Read Body
	// Limit size to prevent DoS
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024) // 10MB limit
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Read error", http.StatusBadRequest)
		return
	}

	name, err := s.scenarioCtrl.Import(r.Context(), data, dryRun)
	if err != nil {
		slog.Error("Import failed", "error", err)
		http.Error(w, fmt.Sprintf("Import failed: %v", err), http.StatusBadRequest)
		return
	}

	// Activate if requested and not dry-run
	if shouldActivate && !dryRun {
		if err := s.scenarioCtrl.Activate(r.Context(), name); err != nil {
			slog.Error("Activation failed", "scenario", name, "error", err)
			http.Error(w, fmt.Sprintf("Imported but activation failed: %v", err), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	resp := ScenarioImportResponse{
		Status: "imported",
		Name:   name,
	}
	if dryRun {
		resp.Status = "validated"
	} else if shouldActivate {
		resp.Status = "imported_and_activated"
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// handleScenarioActive godoc
// @Summary Get Active Scenario
// @Description Returns the currently active scenario as YAML.
// @Tags scenario
// @Produce application/yaml
// @Success 200 {string} string "Scenario YAML"
// @Router /scenario/active [get]
func (s *Server) handleScenarioActive(w http.ResponseWriter, r *http.Request) {
	if s.scenarioCtrl == nil {
		http.Error(w, "Scenario controller not initialized", http.StatusServiceUnavailable)
		return
	}

	scenario := s.scenarioCtrl.GetActiveScenario()
	if scenario == nil {
		http.Error(w, "No active scenario", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(http.StatusOK)
	if err := yaml.NewEncoder(w).Encode(scenario); err != nil {
		slog.Error("Failed to encode active scenario", "error", err)
	}
}

// handleTopologyStatus godoc
// @Summary Topology Status
// @Description Returns synchronization status of the fleet
// @Tags topology
// @Produce json
// @Success 200 {object} TopologyStatusResponse
// @Router /topology/status [get]
func (s *Server) handleTopologyStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Query registry for all racks
	racks, err := s.reg.List(ctx, "")
	racksTotal := 0
	if err == nil {
		racksTotal = len(racks)
	}

	// Get active scenario details
	activeVer := "unknown"
	activeScenario := "none"
	if s.scenarioCtrl != nil {
		activeVer = s.scenarioCtrl.CurrentVersion()
		activeScenario = s.scenarioCtrl.CurrentName()
	}

	status := TopologyStatusResponse{
		SyncStatus:     "synchronized",
		ActiveScenario: activeScenario,
		ActiveVer:      activeVer,
		RacksTotal:     racksTotal,
	}
	_ = json.NewEncoder(w).Encode(status)
}

// handleTopologyList godoc
// @Summary List Topology
// @Description Returns the projected topology with rack and gear details.
// @Tags topology
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /topology/list [get]
func (s *Server) handleTopologyList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Query registry for all racks
	racks, err := s.reg.List(ctx, "")
	rackList := []map[string]any{}
	if err == nil {
		for _, rack := range racks {
			rackList = append(rackList, map[string]any{
				"machine_id": rack.MachineID,
				"name":       rack.Name,
				"status":     rack.Status,
				"last_seen":  rack.LastSeen,
			})
		}
	}

	// Get gears from scenario controller
	gearList := []string{}
	if s.scenarioCtrl != nil {
		scenario := s.scenarioCtrl.GetActiveScenario()
		if scenario != nil {
			for _, g := range scenario.Gears {
				gearList = append(gearList, g.Name)
			}
		}
	}

	topology := map[string]any{
		"racks": rackList,
		"gears": gearList,
	}
	_ = json.NewEncoder(w).Encode(topology)
}

func (s *Server) publishStatus(id uuid.UUID, status string, cmd string, passport []byte) {
	if s.pub == nil {
		return
	}

	topic := fmt.Sprintf("flux.agent.notify.%s", id.String())

	payload := map[string]any{
		"status":   status,
		"command":  cmd,
		"passport": passport,
	}

	fm := fluxmsg.New()
	fm.FluxID = uuid.Nil
	fm.SrcGearID = uuid.Nil
	fm.Data = payload

	b, err := cbor.Marshal(fm)
	if err != nil {
		slog.Error("failed to marshal fluxmsg", "error", err)
		return
	}

	msg := message.NewMessage(watermill.NewUUID(), b)
	if err := s.pub.Publish(topic, msg); err != nil {
		slog.Error("failed to publish notification", "topic", topic, "error", err)
	} else {
		slog.Info("Notification Sent", "id", id.String(), "status", status)
	}
}

// handleWasmImport godoc
// @Summary Import Wasm Module
// @Description Securely imports, validates, and distributes a Wasm payload
// @Tags wasm
// @Accept application/octet-stream
// @Produce json
// @Param allow_unsigned query boolean false "Allow unsigned modules"
// @Param body body []byte true "Wasm binary payload"
// @Success 200 {object} map[string]interface{}
// @Router /wasm/import [post]
func (s *Server) handleWasmImport(w http.ResponseWriter, r *http.Request) {
	if s.wasmCatalog == nil {
		http.Error(w, "Wasm Catalog is not initialized", http.StatusServiceUnavailable)
		return
	}

	allowUnsignedVal := r.URL.Query().Get("allow_unsigned")
	allowUnsigned := allowUnsignedVal == "true" || allowUnsignedVal == "1"

	r.Body = http.MaxBytesReader(w, r.Body, 50*1024*1024) // 50MB limit for Wasm modules
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read Wasm payload", http.StatusBadRequest)
		return
	}

	meta, err := s.wasmCatalog.Import(r.Context(), data, allowUnsigned)
	if err != nil {
		http.Error(w, fmt.Sprintf("Wasm validation failed: %v", err), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(meta)
}

// handleWasmCatalog godoc
// @Summary List Wasm Catalog
// @Description Returns the catalog of trusted and available Wasm modules
// @Tags wasm
// @Produce json
// @Success 200 {array} map[string]interface{}
// @Router /wasm/catalog [get]
func (s *Server) handleWasmCatalog(w http.ResponseWriter, r *http.Request) {
	if s.wasmCatalog == nil {
		http.Error(w, "Wasm Catalog is not initialized", http.StatusServiceUnavailable)
		return
	}

	list, err := s.wasmCatalog.List()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read Wasm catalog: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(list)
}

// handleGears godoc
// @Summary List gear manifests
// @Description Returns the manifest catalog: every gear type this build can run, with identity, ports, and config schema
// @Tags gears
// @Produce json
// @Success 200 {array} sdk.Manifest
// @Router /gears [get]
func (s *Server) handleGears(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(s.gearFactory.Manifests())
}

// handleGearManifest godoc
// @Summary Get one gear manifest
// @Description Returns the manifest for a single gear type
// @Tags gears
// @Produce json
// @Param type path string true "Gear type (e.g. io_iso8583)"
// @Success 200 {object} sdk.Manifest
// @Failure 404 {object} map[string]string
// @Router /gears/{type} [get]
func (s *Server) handleGearManifest(w http.ResponseWriter, r *http.Request) {
	typ := r.PathValue("type")
	m, ok := s.gearFactory.Manifest(typ)
	if !ok {
		http.Error(w, fmt.Sprintf("unknown gear type %q", typ), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(m)
}
