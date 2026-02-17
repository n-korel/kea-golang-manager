package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"kea-golang-manager/internal/kea"
	"kea-golang-manager/internal/service"
)

// NewHandler создаёт HTTP-обработчик с REST API поверх DHCPService.
func NewHandler(dhcpService *service.DHCPService) http.Handler {
	h := &handler{
		dhcpService: dhcpService,
	}

	r := chi.NewRouter()

	r.Get("/config", h.getConfig)
	r.Post("/reload", h.reload)

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

type errorResponse struct {
	Error string `json:"error"`
}

type addSubnetRequest struct {
	Subnet       string             `json:"subnet"`
	Pools        []string           `json:"pools"`
	Reservations []kea.Reservation  `json:"reservations,omitempty"`
}

func (h *handler) listSubnets(w http.ResponseWriter, r *http.Request) {
	subnets, err := h.dhcpService.ListSubnets(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, subnets)
}

func (h *handler) addSubnet(w http.ResponseWriter, r *http.Request) {
	var req addSubnetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if req.Subnet == "" {
		writeError(w, http.StatusBadRequest, errors.New("subnet is required"))
		return
	}

	if err := h.dhcpService.AddSubnet(r.Context(), req.Subnet, req.Pools, req.Reservations); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

func (h *handler) deleteSubnet(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	if idParam == "" {
		writeError(w, http.StatusBadRequest, errors.New("id is required"))
		return
	}

	id, err := strconv.Atoi(idParam)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if err := h.dhcpService.DeleteSubnet(r.Context(), id); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) getConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.dhcpService.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, cfg)
}

func (h *handler) reload(w http.ResponseWriter, r *http.Request) {
	if err := h.dhcpService.Reload(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, errorResponse{Error: err.Error()})
}

