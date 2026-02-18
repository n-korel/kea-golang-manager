package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"kea-golang-manager/internal/kea"
	"kea-golang-manager/internal/service"
)

const requestTimeout = 15 * time.Second

// HandlerOpts — опции для создания HTTP-обработчика.
type HandlerOpts struct {
	DHCPService *service.DHCPService
}

// NewHandler создаёт HTTP-обработчик с REST API.
func NewHandler(opts HandlerOpts) http.Handler {
	h := &handler{dhcpService: opts.DHCPService}

	r := chi.NewRouter()
	r.Get("/health", h.getHealth)
	r.Get("/config", h.getConfig)
	r.Post("/reload", h.reload)
	r.Post("/kea/reload", h.reload)
	r.Get("/stats", h.getStats)
	r.Route("/subnets", func(r chi.Router) {
		r.Get("/", h.listSubnets)
		r.Post("/", h.addSubnet)
		r.Delete("/{id}", h.deleteSubnet)
	})

	return r
}

type handler struct {
	dhcpService *service.DHCPService
}

type healthResponse struct {
	Status string     `json:"status"`
	Kea    *keaHealth `json:"kea,omitempty"`
}

type keaHealth struct {
	Reachable bool `json:"reachable"`
}

func (h *handler) getHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	_, err := h.dhcpService.GetConfig(ctx)
	reachable := err == nil

	if !reachable {
		writeJSON(w, http.StatusServiceUnavailable, healthResponse{
			Status: "error",
			Kea:    &keaHealth{Reachable: false},
		})
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{
		Status: "ok",
		Kea:    &keaHealth{Reachable: true},
	})
}

func (h *handler) getConfig(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	cfg, err := h.dhcpService.GetConfig(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (h *handler) reload(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if err := h.dhcpService.WriteConfigAndReload(ctx); err != nil {
		slog.Error("kea_reload_failed", "error", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	slog.Info("kea_reload_completed")
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) getStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	stats, err := h.dhcpService.Lease4Stats(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if stats == nil {
		stats = map[string]interface{}{}
	}
	writeJSON(w, http.StatusOK, stats)
}

func (h *handler) listSubnets(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	subnets, err := h.dhcpService.ListSubnets(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, subnets)
}

func (h *handler) addSubnet(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var req addSubnetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Subnet == "" {
		writeError(w, http.StatusBadRequest, errors.New("subnet is required"))
		return
	}
	if err := h.dhcpService.AddSubnet(ctx, req.Subnet, req.Pools, req.Reservations); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *handler) deleteSubnet(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	idParam := chi.URLParam(r, "id")
	if idParam == "" {
		writeError(w, http.StatusBadRequest, errors.New("id is required"))
		return
	}
	id, err := strconv.Atoi(idParam)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("id must be a positive integer"))
		return
	}
	if err := h.dhcpService.DeleteSubnet(ctx, id); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type errorResponse struct {
	Error string `json:"error"`
}

type addSubnetRequest struct {
	Subnet       string            `json:"subnet"`
	Pools        []string          `json:"pools"`
	Reservations []kea.Reservation `json:"reservations,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, errorResponse{Error: err.Error()})
}
