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

	"kea-golang-manager/internal/ha"
	"kea-golang-manager/internal/kea"
)

const requestTimeout = 15 * time.Second

// haStatusProvider — интерфейс для получения HA-статуса (для тестов с моками).
type haStatusProvider interface {
	Status(ctx context.Context) (*ha.ClusterStatus, error)
}

// dhcpServiceForHandler — методы DHCPService, используемые обработчиком (для тестов с моками).
type dhcpServiceForHandler interface {
	GetConfig(ctx context.Context) (map[string]interface{}, error)
	ListSubnets(ctx context.Context) ([]kea.Subnet4, error)
	AddSubnet(ctx context.Context, subnet string, pools []string, reservations []kea.Reservation) (ha.ApplyResult, error)
	DeleteSubnet(ctx context.Context, id int) (ha.ApplyResult, error)
	WriteConfigAndReload(ctx context.Context) (ha.ApplyResult, error)
	Lease4Stats(ctx context.Context) (map[string]interface{}, error)
}

// HandlerOpts — опции для создания HTTP-обработчика.
type HandlerOpts struct {
	DHCPService dhcpServiceForHandler
	HAManager   haStatusProvider
}

// NewHandler создаёт HTTP-обработчик с REST API.
func NewHandler(opts HandlerOpts) http.Handler {
	h := &handler{
		dhcpService: opts.DHCPService,
		haManager:   opts.HAManager,
	}

	r := chi.NewRouter()
	r.Get("/health", h.getHealth)
	r.Get("/config", h.getConfig)
	r.Get("/ha/status", h.getHAStatus)
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
	dhcpService dhcpServiceForHandler
	haManager   haStatusProvider
}

type healthResponse struct {
	Status string     `json:"status"`
	Kea    *keaHealth `json:"kea,omitempty"`
}

type keaHealth struct {
	Primary  *nodeReachable `json:"primary,omitempty"`
	Standby  *nodeReachable `json:"standby,omitempty"`
}

type nodeReachable struct {
	Reachable bool `json:"reachable"`
}

func (h *handler) getHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	status, err := h.haManager.Status(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, healthResponse{
			Status: "error",
			Kea:    &keaHealth{Primary: &nodeReachable{false}, Standby: &nodeReachable{false}},
		})
		return
	}
	keaHealthVal := &keaHealth{
		Primary: &nodeReachable{Reachable: status.Primary.Reachable},
		Standby: &nodeReachable{Reachable: status.Standby.Reachable},
	}
	respStatus := "ok"
	httpStatus := http.StatusOK
	if !status.Primary.Reachable {
		respStatus = "error"
		httpStatus = http.StatusServiceUnavailable
	}
	writeJSON(w, httpStatus, healthResponse{Status: respStatus, Kea: keaHealthVal})
}

type haStatusResponse struct {
	Primary  haNodeStatus `json:"primary"`
	Standby  haNodeStatus `json:"standby"`
}

type haNodeStatus struct {
	State     string `json:"state"`
	Role      string `json:"role"`
	Reachable bool   `json:"reachable"`
}

func (h *handler) getHAStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	status, err := h.haManager.Status(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, haStatusResponse{
		Primary:  haNodeStatus{State: status.Primary.State, Role: status.Primary.Role, Reachable: status.Primary.Reachable},
		Standby:  haNodeStatus{State: status.Standby.State, Role: status.Standby.Role, Reachable: status.Standby.Reachable},
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

	result, err := h.dhcpService.WriteConfigAndReload(ctx)
	if err != nil {
		slog.Error("kea_reload_failed", "error", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	slog.Info("kea_reload_completed")
	if result.Warning != "" {
		writeApplyResult207(w, result)
		return
	}
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
	result, err := h.dhcpService.AddSubnet(ctx, req.Subnet, req.Pools, req.Reservations)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if result.Warning != "" {
		writeApplyResult207(w, result)
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
	result, err := h.dhcpService.DeleteSubnet(ctx, id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if result.Warning != "" {
		writeApplyResult207(w, result)
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

// writeApplyResult207 возвращает 207 с телом {"warning": "...", "ha_state": "..."} при пропуске standby.
func writeApplyResult207(w http.ResponseWriter, result ha.ApplyResult) {
	writeJSON(w, http.StatusMultiStatus, struct {
		Warning string `json:"warning"`
		HAState string `json:"ha_state"`
	}{result.Warning, result.HAState})
}
