package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/registry"

	"github.com/jaab-tech/fluxrig/pkg/pki"
	"github.com/jaab-tech/fluxrig/pkg/version"
	"github.com/vmihailenco/msgpack/v5"
)

type Server struct {
	reg    registry.Registry
	pub    message.Publisher
	signer *pki.ClusterKey
}

func NewServer(reg registry.Registry, pub message.Publisher, signer *pki.ClusterKey) *Server {
	return &Server{reg: reg, pub: pub, signer: signer}
}

func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()

	// Middleware: CORS/Recovery/Logging logic can be added here

	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/racks", s.handleRacks)
	mux.HandleFunc("/api/v1/racks/", s.handleRackAction) // Trailing slash for sub-paths
	mux.HandleFunc("/api/v1/telemetry/", s.handleTelemetry)

	// Log using global/standard logger which is slog at this point
	slog.Info("Mixer Control Plane listening", "addr", addr)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
	}
	return server.ListenAndServe()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"version": version.String(),
	})
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
					MachineID:     rack.MachineID,
					Name:          rack.Name,
					Status:        rack.Status, // "active"
					Secret:        rack.Secret,
					ClusterPublic: s.signer.Public,
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
					MachineID:     rack.MachineID,
					Name:          rack.Name,
					Status:        rack.Status, // "inactive"
					Secret:        rack.Secret,
					ClusterPublic: s.signer.Public,
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
					MachineID:     rack.MachineID,
					Name:          rack.Name,
					Status:        rack.Status, // "active"
					Secret:        rack.Secret,
					ClusterPublic: s.signer.Public,
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
