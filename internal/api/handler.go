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
	"kea-golang-manager/internal/lldp"
	"kea-golang-manager/internal/service"
	"kea-golang-manager/internal/snmp"
)

const requestTimeout = 15 * time.Second

// HandlerOpts — опции для создания HTTP-обработчика (обязателен только DHCPService).
type HandlerOpts struct {
	DHCPService    *service.DHCPService
	HAStore        *ha.StateStore
	HAClient       *ha.HAClient
	PrimaryClient  *kea.Client
	StandbyClient  *kea.Client
	SNMPPoller     *snmp.Poller
	LLDPCollector  *lldp.Collector
}

// NewHandler создаёт HTTP-обработчик с REST API (backend_rules.api_endpoints_required).
func NewHandler(opts HandlerOpts) http.Handler {
	h := &handler{
		dhcpService:   opts.DHCPService,
		haStore:       opts.HAStore,
		haClient:      opts.HAClient,
		primaryClient: opts.PrimaryClient,
		standbyClient: opts.StandbyClient,
		snmpPoller:    opts.SNMPPoller,
		lldpCollector: opts.LLDPCollector,
	}

	r := chi.NewRouter()

	r.Get("/health", h.getHealth)
	r.Get("/ha/status", h.getHAStatus)
	r.Post("/ha/demote", h.postHADemote)

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
	dhcpService   *service.DHCPService
	haStore       *ha.StateStore
	haClient      *ha.HAClient
	primaryClient *kea.Client
	standbyClient *kea.Client
	snmpPoller    *snmp.Poller
	lldpCollector *lldp.Collector
}

func (h *handler) getHealth(w http.ResponseWriter, r *http.Request) {
	_, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if h.haStore == nil {
		writeJSON(w, http.StatusOK, healthResponse{
			Status:  "ok",
			Primary: nil,
			Standby: nil,
		})
		return
	}

	status, primaryObs, standbyObs := h.haStore.GetStatusSnapshot()
	primaryBlock := nodeHealthFromObs(primaryObs, "primary")
	standbyBlock := nodeHealthFromObs(standbyObs, "standby")

	aggStatus := "ok"
	if !primaryBlock.Reachable && !standbyBlock.Reachable {
		aggStatus = "critical"
	} else if !primaryBlock.Reachable || !standbyBlock.Reachable || status.HAState == ha.HAStatePartnerDown {
		aggStatus = "degraded"
	}

	resp := healthResponse{
		Status:  aggStatus,
		Primary: primaryBlock,
		Standby: standbyBlock,
	}

	code := http.StatusOK
	if aggStatus == "critical" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, resp)
}

type healthResponse struct {
	Status  string       `json:"status"`
	Primary *nodeHealth  `json:"primary,omitempty"`
	Standby *nodeHealth  `json:"standby,omitempty"`
}

type nodeHealth struct {
	Reachable bool   `json:"reachable"`
	HAState   string `json:"ha_state"`
}

func nodeHealthFromObs(obs *ha.Observation, node string) *nodeHealth {
	if obs == nil {
		return &nodeHealth{Reachable: false, HAState: ha.HAStateReady}
	}
	return &nodeHealth{
		Reachable: obs.Reachable,
		HAState:   obs.HAState,
	}
}

func (h *handler) getHAStatus(w http.ResponseWriter, r *http.Request) {
	_, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if h.haStore == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("HA not configured"))
		return
	}
	st := h.haStore.GetStatus()
	writeJSON(w, http.StatusOK, st)
}

type demoteRequest struct {
	Node    string `json:"node"`
	Confirm bool   `json:"confirm"`
}

func (h *handler) postHADemote(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if h.haClient == nil || h.haStore == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("HA not configured"))
		return
	}

	var req demoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !req.Confirm {
		writeError(w, http.StatusBadRequest, errors.New("confirm is required and must be true"))
		return
	}
	if req.Node != ha.NodePrimary && req.Node != ha.NodeStandby {
		writeError(w, http.StatusBadRequest, errors.New("node must be \"primary\" or \"standby\""))
		return
	}

	if err := h.haClient.MaintenanceStart(ctx, req.Node); err != nil {
		slog.Error("ha_demote_failed", "node", req.Node, "error", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	slog.Info("ha_demote_triggered", "node", req.Node, "reason", "api_request")
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) reload(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if h.primaryClient != nil && h.standbyClient != nil {
		if err := h.primaryClient.WriteConfigAndReload(ctx); err != nil {
			slog.Error("kea_reload_failed", "node", "primary", "error", err)
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if err := h.standbyClient.WriteConfigAndReload(ctx); err != nil {
			slog.Error("kea_reload_failed", "node", "standby", "error", err)
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		slog.Info("kea_reload_completed", "nodes", []string{"primary", "standby"})
	} else {
		if err := h.dhcpService.WriteConfigAndReload(ctx); err != nil {
			slog.Error("kea_reload_failed", "error", err)
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		slog.Info("kea_reload_completed", "nodes", []string{"primary"})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) getStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	haState := ""
	if h.haStore != nil {
		haState = h.haStore.GetStatus().HAState
	}

	leases, err := h.dhcpService.Lease4Stats(ctx)
	if err != nil {
		leases = nil
	}

	var snmpData *snmp.Snapshot
	if h.snmpPoller != nil {
		s := h.snmpPoller.Snapshot()
		snmpData = &s
	}
	var lldpData *lldp.Snapshot
	if h.lldpCollector != nil {
		s := h.lldpCollector.Snapshot()
		lldpData = &s
	}

	resp := statsResponse{
		HAState: haState,
		Leases:  leases,
		SNMP:    snmpData,
		LLDP:    lldpData,
	}
	writeJSON(w, http.StatusOK, resp)
}

type statsResponse struct {
	HAState string                 `json:"ha_state"`
	Leases  map[string]interface{} `json:"leases,omitempty"`
	SNMP    *snmp.Snapshot         `json:"snmp,omitempty"`
	LLDP    *lldp.Snapshot         `json:"lldp,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type addSubnetRequest struct {
	Subnet       string            `json:"subnet"`
	Pools        []string          `json:"pools"`
	Reservations []kea.Reservation `json:"reservations,omitempty"`
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

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, errorResponse{Error: err.Error()})
}
