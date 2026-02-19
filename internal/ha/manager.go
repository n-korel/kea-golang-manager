package ha

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"kea-golang-manager/internal/kea"
)

// NodeStatus — статус одного узла (primary или standby).
type NodeStatus struct {
	Role      string // "primary" | "standby"
	State     string // одна из констант kea.HAState*
	Reachable bool
}

// ClusterStatus — статус HA-кластера (оба узла).
type ClusterStatus struct {
	Primary NodeStatus
	Standby NodeStatus
}

// ApplyResult описывает исход Guarded Apply.
type ApplyResult struct {
	PrimaryOK bool
	StandbyOK bool
	HAState   string // состояние после применения на primary
	Warning   string // не пустое, если standby был пропущен
}

// HAManager управляет HA-кластером Kea и guarded apply.
type HAManager struct {
	primaryClient *kea.Client
	standbyClient *kea.Client
	mu            sync.Mutex
	lastPrimaryState string
	lastStandbyState string
}

// NewHAManager создаёт HAManager с клиентами для primary и standby.
func NewHAManager(primaryClient, standbyClient *kea.Client) *HAManager {
	return &HAManager{
		primaryClient: primaryClient,
		standbyClient: standbyClient,
	}
}

// Status опрашивает ha-heartbeat на обоих узлах.
// Если узел недоступен: Reachable=false, State="unknown".
func (m *HAManager) Status(ctx context.Context) (*ClusterStatus, error) {
	out := &ClusterStatus{
		Primary: NodeStatus{Role: "primary", State: "unknown"},
		Standby: NodeStatus{Role: "standby", State: "unknown"},
	}

	primaryState, err := m.primaryClient.HAHeartbeat(ctx)
	if err != nil {
		out.Primary.Reachable = false
	} else {
		out.Primary.Reachable = true
		out.Primary.State = primaryState
		m.logStateTransition("primary", primaryState)
	}

	standbyState, err := m.standbyClient.HAHeartbeat(ctx)
	if err != nil {
		out.Standby.Reachable = false
	} else {
		out.Standby.Reachable = true
		out.Standby.State = standbyState
		m.logStateTransition("standby", standbyState)
	}

	return out, nil
}

func (m *HAManager) logStateTransition(role, state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var prev string
	if role == "primary" {
		prev = m.lastPrimaryState
		m.lastPrimaryState = state
	} else {
		prev = m.lastStandbyState
		m.lastStandbyState = state
	}
	if prev != state {
		slog.Info("ha state transition", "role", role, "previous", prev, "current", state)
	}
}

// ActiveClient возвращает клиент активного узла.
// Логика: 1) primary доступен → primary; 2) primary недоступен и standby в partner-down → standby; 3) иначе ошибка.
func (m *HAManager) ActiveClient(ctx context.Context) (*kea.Client, error) {
	status, err := m.Status(ctx)
	if err != nil {
		return nil, err
	}
	if status.Primary.Reachable {
		return m.primaryClient, nil
	}
	if status.Standby.Reachable && status.Standby.State == kea.HAStatePartnerDown {
		return m.standbyClient, nil
	}
	return nil, fmt.Errorf("no active kea node available")
}

// GuardedApply применяет fn к активному узлу и при состоянии hot-standby — к standby.
// Возвращает ошибку только если применение на активном узле (шаг 1) упало.
func (m *HAManager) GuardedApply(ctx context.Context, fn func(ctx context.Context, c *kea.Client) error) (ApplyResult, error) {
	active, err := m.ActiveClient(ctx)
	if err != nil {
		return ApplyResult{}, err
	}

	if err := fn(ctx, active); err != nil {
		return ApplyResult{PrimaryOK: false}, err
	}

	result := ApplyResult{PrimaryOK: true}
	state, err := active.HAHeartbeat(ctx)
	if err != nil {
		result.HAState = "unknown"
		result.StandbyOK = false
		result.Warning = fmt.Sprintf("ha-heartbeat after apply failed: %v", err)
		slog.Warn("standby apply skipped: could not get HA state", "error", err)
		return result, nil
	}
	result.HAState = state

	if state == kea.HAStateHotStandby {
		if err := fn(ctx, m.standbyClient); err != nil {
			result.StandbyOK = false
			result.Warning = fmt.Sprintf("standby apply failed: %v", err)
			slog.Error("standby apply failed", "error", err)
			return result, nil
		}
		result.StandbyOK = true
		return result, nil
	}

	result.StandbyOK = false
	result.Warning = fmt.Sprintf("standby apply skipped: HA state is %s", state)
	slog.Warn("standby apply skipped", "ha_state", state)
	return result, nil
}
