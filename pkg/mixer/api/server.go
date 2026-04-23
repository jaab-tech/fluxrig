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
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/registry"

	"github.com/fxamacker/cbor/v2"
	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/telemetry"
	"github.com/jaab-tech/fluxrig/pkg/version"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/controller"
	_ "github.com/jaab-tech/fluxrig/pkg/mixer/api/docs" // Swagger docs
	httpSwagger "github.com/swaggo/http-swagger"
)

type Server struct {
	reg          registry.Registry
	pub          message.Publisher
	signer       *pki.ClusterKey
	scenarioCtrl controller.ScenarioManager
	metricsCache *telemetry.MetricsCache
	mixerID      uint64
	cfg          *config.MixerConfig
}

func NewServer(reg registry.Registry, pub message.Publisher, signer *pki.ClusterKey, sc controller.ScenarioManager, cache *telemetry.MetricsCache, mixerID uint64, cfg *config.MixerConfig) *Server {
	return &Server{reg: reg, pub: pub, signer: signer, scenarioCtrl: sc, metricsCache: cache, mixerID: mixerID, cfg: cfg}
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
	mux.HandleFunc("GET /api/v1/topology/status", s.handleTopologyStatus)
	mux.HandleFunc("GET /api/v1/topology/list", s.handleTopologyList)

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
	// Resilient Bind-Retry Loop (ADR 0032)
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
		return fmt.Errorf("Mixer API bind failed after %d attempts: %w", maxAttempts, err)
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
		Status:  "ok",
		Version: version.String(),
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

	id, err := strconv.ParseUint(idStr, 10, 16)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	// Handle DELETE /racks/{id}
	if r.Method == http.MethodDelete && action == "" {
		if err := s.reg.Remove(r.Context(), uint16(id)); err != nil {
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

			rack, err := s.reg.Approve(r.Context(), uint16(id), req.Name)
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
			s.publishStatus(uint16(id), "active", "approved", passportBytes)
			_ = json.NewEncoder(w).Encode(rack)
			return

		case "suspend":
			if err := s.reg.UpdateStatus(r.Context(), uint16(id), "inactive"); err != nil {
				if err == registry.ErrNotFound {
					http.Error(w, "Rack not found", http.StatusNotFound)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			var passportBytes []byte
			if rack, err := s.reg.Get(r.Context(), uint16(id)); err == nil && s.signer != nil {
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
			s.publishStatus(uint16(id), "inactive", "suspended", passportBytes)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"suspended"}`))
			return

		case "activate":
			if err := s.reg.UpdateStatus(r.Context(), uint16(id), "active"); err != nil {
				if err == registry.ErrNotFound {
					http.Error(w, "Rack not found", http.StatusNotFound)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			var passportBytes []byte
			if rack, err := s.reg.Get(r.Context(), uint16(id)); err == nil && s.signer != nil {
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
			s.publishStatus(uint16(id), "active", "activated", passportBytes)
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
			rack, err := s.reg.Get(r.Context(), uint16(id))
			if err != nil {
				if err == registry.ErrNotFound {
					http.Error(w, "Rack not found", http.StatusNotFound)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			// NOTIFY RACK
			s.publishStatus(uint16(id), rack.Status, "set_log_level:"+req.Level, nil)

			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "level": req.Level})
			return

		case "shutdown":
			// Get current rack to find status
			rack, err := s.reg.Get(r.Context(), uint16(id))
			if err != nil {
				if err == registry.ErrNotFound {
					http.Error(w, "Rack not found", http.StatusNotFound)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			// NOTIFY RACK with "agent:shutdown" command
			s.publishStatus(uint16(id), rack.Status, "agent:shutdown", nil)

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
		_ = json.NewEncoder(w).Encode(logs)
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
// @Param id query int false "Entity ID"
// @Success 200 {object} map[string]interface{}
// @Router /entities/stats [get]
func (s *Server) handleEntityStats(w http.ResponseWriter, r *http.Request) {
	if s.metricsCache == nil {
		http.Error(w, "Metrics cache not initialized", http.StatusServiceUnavailable)
		return
	}

	idStr := r.URL.Query().Get("id")
	if idStr != "" {
		id, err := strconv.ParseUint(idStr, 10, 64)
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

// handleTopologyStatus returns the current synchronization status.
// Queries registry for actual rack count.
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

	// Get active scenario version
	activeVer := "unknown"
	if s.scenarioCtrl != nil {
		activeVer = s.scenarioCtrl.CurrentVersion()
	}

	status := TopologyStatusResponse{
		SyncStatus: "synchronized",
		ActiveVer:  activeVer,
		RacksTotal: racksTotal,
	}
	_ = json.NewEncoder(w).Encode(status)
}

// handleTopologyList returns the projected topology with rack and gear details.
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

func (s *Server) publishStatus(id uint16, status string, cmd string, passport []byte) {
	if s.pub == nil {
		return
	}

	topic := fmt.Sprintf("fluxrig.agent.notify.%d", id)

	payload := map[string]any{
		"status":   status,
		"command":  cmd,
		"passport": passport,
	}

	fm := fluxmsg.New()
	fm.FluxID = 0
	fm.SrcGearID = 0
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
		slog.Info("Notification Sent", "id", id, "status", status)
	}
}
