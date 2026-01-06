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

	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/version"
	"github.com/vmihailenco/msgpack/v5"

	"github.com/jaab-tech/fluxrig/pkg/config"
	"github.com/jaab-tech/fluxrig/pkg/controller"
)

type Server struct {
	reg          registry.Registry
	pub          message.Publisher
	signer       *pki.ClusterKey
	scenarioCtrl *controller.ScenarioController
	mixerID      uint64
	cfg          *config.MixerConfig
}

func NewServer(reg registry.Registry, pub message.Publisher, signer *pki.ClusterKey, sc *controller.ScenarioController, mixerID uint64, cfg *config.MixerConfig) *Server {
	return &Server{reg: reg, pub: pub, signer: signer, scenarioCtrl: sc, mixerID: mixerID, cfg: cfg}
}

func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()

	// Middleware: CORS/Recovery/Logging logic can be added here

	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/config", s.handleConfig)
	mux.HandleFunc("/api/v1/racks", s.handleRacks)
	mux.HandleFunc("/api/v1/racks/{id}", s.handleRackAction) // Use Go 1.22 path value syntax if valid, or just check content first.
	mux.HandleFunc("/api/v1/racks/", s.handleRackAction)     // Trailing slash for sub-paths
	mux.HandleFunc("/api/v1/telemetry/", s.handleTelemetry)
	mux.HandleFunc("/api/v1/scenario/import", s.handleScenarioImport)
	mux.HandleFunc("/api/v1/topology/status", s.handleTopologyStatus)
	mux.HandleFunc("/api/v1/topology/list", s.handleTopologyList)

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
	l, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
	}
	return server.Serve(l)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"version": version.String(),
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if s.cfg == nil {
		http.Error(w, "Config not available", http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(s.cfg)
}

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

func (s *Server) handleRackAction(w http.ResponseWriter, r *http.Request) {
	// Parse /api/v1/racks/{id}/approve
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/racks/")
	parts := strings.Split(path, "/")

	// Handle /racks/{id} (DELETE)
	if len(parts) == 1 {
		idStr := parts[0]
		id, err := strconv.ParseUint(idStr, 10, 16)
		if err != nil {
			http.Error(w, "Invalid ID", http.StatusBadRequest)
			return
		}

		if r.Method == http.MethodDelete {
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

		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Handle /racks/{id}/{action}
	if len(parts) >= 2 {
		idStr := parts[0]
		action := parts[1]

		id, err := strconv.ParseUint(idStr, 10, 16)
		if err != nil {
			http.Error(w, "Invalid ID", http.StatusBadRequest)
			return
		}

		if action == "approve" && r.Method == http.MethodPost {
			var req struct {
				Name string `json:"name"`
			}
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
					if b, err := msgpack.Marshal(env); err == nil {
						passportBytes = b
					}
				}
			}

			// NOTIFY RACK
			s.publishStatus(uint16(id), "active", "approved", passportBytes)
			_ = json.NewEncoder(w).Encode(rack)
			return
		}

		if action == "suspend" && r.Method == http.MethodPost {
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
					if b, err := msgpack.Marshal(env); err == nil {
						passportBytes = b
					}
				}
			}

			// NOTIFY RACK
			s.publishStatus(uint16(id), "inactive", "suspended", passportBytes)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"suspended"}`))
			return
		}

		if action == "activate" && r.Method == http.MethodPost {
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
					if b, err := msgpack.Marshal(env); err == nil {
						passportBytes = b
					}
				}
			}

			// NOTIFY RACK
			s.publishStatus(uint16(id), "active", "activated", passportBytes)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"activated"}`))
			return
		}
	}

	http.Error(w, "Not found", http.StatusNotFound)
}

func (s *Server) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	// /api/v1/telemetry/logs or /metrics
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/telemetry/")
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
	if dryRun {
		_, _ = w.Write([]byte(`{"status":"validated"}`))
	} else {
		if shouldActivate {
			_, _ = w.Write([]byte(`{"status":"imported_and_activated","name":"` + name + `"}`))
		} else {
			_, _ = w.Write([]byte(`{"status":"imported","name":"` + name + `"}`))
		}
	}
}

// handleTopologyStatus returns the current synchronization status.
// Queries registry for actual rack count.
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

	status := map[string]any{
		"sync_status": "synchronized",
		"active_ver":  activeVer,
		"racks_total": racksTotal,
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

	b, err := msgpack.Marshal(fm)
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
