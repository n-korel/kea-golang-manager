package ha

import (
	"sync"
	"time"
)

// HAState — состояние HA state machine Kea.
const (
	HAStateHotStandby            = "hot-standby"
	HAStatePartnerDown            = "partner-down"
	HAStateCommunicationRecovery  = "communication-recovery"
	HAStateReady                  = "ready"
	HAStateTerminated             = "terminated"
)

// NodeRole — роль узла (по данным Kea HA).
const (
	RolePrimary  = "primary"
	RoleStandby  = "standby"
	RoleUnknown  = "unknown"
)

// PeerStatus — доступность пира с точки зрения оркестратора.
const (
	PeerOnline  = "online"
	PeerOffline = "offline"
	PeerUnknown = "unknown"
)

// HAStatus — агрегированный HA-статус для API и решений failover.
type HAStatus struct {
	CurrentRole            string     `json:"current_role"`
	PeerStatus             string     `json:"peer_status"`
	HAState                string     `json:"ha_state"`
	LastFailoverReason     *string    `json:"last_failover_reason,omitempty"`
	LastRoleChangeAt       *time.Time `json:"last_role_change_timestamp,omitempty"`
}

// Observation — результат одной проверки узла (для монитора).
type Observation struct {
	Node       string    // "primary" | "standby"
	Reachable  bool
	HAState    string
	Role       string
	ObservedAt time.Time
	Err        error
}

// FailoverReason — причина сбоя (failover_rules.required_failure_models).
const (
	FailoverReasonKeaProcessCrash   = "kea_process_crash"
	FailoverReasonControlAgentCrash = "control_agent_crash"
	FailoverReasonHALinkFailure     = "ha_link_failure"
	FailoverReasonNetworkPartition  = "network_partition"
	FailoverReasonContainerRestart  = "container_restart"
	FailoverReasonHostReboot        = "host_reboot"
)

// StateStore — потокобезопасное хранилище HA-состояния (без глобального мутабельного стейта).
type StateStore struct {
	mu sync.RWMutex

	// Агрегированный статус для API (выводится из наблюдений и ha-status).
	status HAStatus

	// Последовательные неудачи по узлам (для порога min_detection_interval).
	primaryConsecutiveFailures int
	standbyConsecutiveFailures int

	// Последнее наблюдение по каждому узлу (для quorum и классификации).
	lastPrimaryObservation *Observation
	lastStandbyObservation *Observation

	// Время последней смены роли и причина последнего failover (для логов и API).
	lastRoleChangeAt   time.Time
	lastFailoverReason *string
}

// NewStateStore создаёт новое хранилище состояния.
func NewStateStore() *StateStore {
	return &StateStore{
		status: HAStatus{
			CurrentRole: RoleUnknown,
			PeerStatus:  PeerUnknown,
			HAState:     HAStateReady,
		},
	}
}

// GetStatus возвращает копию текущего HA-статуса (для API).
func (s *StateStore) GetStatus() HAStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// GetStatusSnapshot возвращает полный снимок для failover (роль, пир, причина, наблюдения).
func (s *StateStore) GetStatusSnapshot() (HAStatus, *Observation, *Observation) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := s.status
	var primaryObs, standbyObs *Observation
	if s.lastPrimaryObservation != nil {
		o := *s.lastPrimaryObservation
		primaryObs = &o
	}
	if s.lastStandbyObservation != nil {
		o := *s.lastStandbyObservation
		standbyObs = &o
	}
	return st, primaryObs, standbyObs
}

// RecordObservation обновляет состояние по результату проверки узла.
// Вызывается монитором после каждой проверки; обновляет счётчики неудач и последнее наблюдение.
func (s *StateStore) RecordObservation(obs Observation) {
	s.mu.Lock()
	defer s.mu.Unlock()

	obsCopy := obs
	switch obs.Node {
	case "primary":
		if obs.Reachable && obs.Err == nil {
			s.primaryConsecutiveFailures = 0
		} else {
			s.primaryConsecutiveFailures++
		}
		s.lastPrimaryObservation = &obsCopy
	case "standby":
		if obs.Reachable && obs.Err == nil {
			s.standbyConsecutiveFailures = 0
		} else {
			s.standbyConsecutiveFailures++
		}
		s.lastStandbyObservation = &obsCopy
	}
	s.recomputeStatusLocked()
}

// PrimaryFailureCount возвращает число последовательных неудач primary (для порога).
func (s *StateStore) PrimaryFailureCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.primaryConsecutiveFailures
}

// StandbyFailureCount возвращает число последовательных неудач standby.
func (s *StateStore) StandbyFailureCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.standbyConsecutiveFailures
}

// RecordRoleChange вызывается при фиксации смены роли (после quorum и ha-maintenance).
func (s *StateStore) RecordRoleChange(newRole string, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.CurrentRole = newRole
	now := time.Now().UTC()
	s.status.LastRoleChangeAt = &now
	s.lastRoleChangeAt = now
	s.lastFailoverReason = &reason
	s.status.LastFailoverReason = &reason
}

// recomputeStatusLocked агрегирует статус из последних наблюдений (вызывать под mu).
// CurrentRole из наблюдений выставляется только когда primary доступен (чтобы TryFailover
// мог отличить «ещё не делали failover» от «уже зафиксировали standby» через RecordRoleChange).
func (s *StateStore) recomputeStatusLocked() {
	if s.lastPrimaryObservation != nil && s.lastPrimaryObservation.Reachable {
		s.status.CurrentRole = normalizeRole(s.lastPrimaryObservation.Role)
		s.status.HAState = normalizeHAState(s.lastPrimaryObservation.HAState)
		if s.lastStandbyObservation != nil {
			s.status.PeerStatus = peerStatusFromReachable(s.lastStandbyObservation.Reachable)
		} else {
			s.status.PeerStatus = PeerUnknown
		}
		return
	}
	if s.lastStandbyObservation != nil && s.lastStandbyObservation.Reachable {
		s.status.HAState = normalizeHAState(s.lastStandbyObservation.HAState)
		if s.lastPrimaryObservation != nil {
			s.status.PeerStatus = peerStatusFromReachable(s.lastPrimaryObservation.Reachable)
		} else {
			s.status.PeerStatus = PeerUnknown
		}
		// Primary недоступен: CurrentRole не перезаписываем из standby, чтобы TryFailover
		// мог вызвать ha-maintenance и RecordRoleChange(standby); после этого оставляем standby.
		if s.status.CurrentRole != RoleStandby {
			s.status.CurrentRole = RoleUnknown
		}
		return
	}
	s.status.CurrentRole = RoleUnknown
	s.status.PeerStatus = PeerUnknown
	s.status.HAState = HAStateReady
}

func normalizeRole(r string) string {
	switch r {
	case RolePrimary, RoleStandby:
		return r
	default:
		return RoleUnknown
	}
}

func normalizeHAState(st string) string {
	switch st {
	case HAStateHotStandby, HAStatePartnerDown, HAStateCommunicationRecovery, HAStateReady, HAStateTerminated:
		return st
	default:
		return HAStateReady
	}
}

func peerStatusFromReachable(reachable bool) string {
	if reachable {
		return PeerOnline
	}
	return PeerOffline
}
