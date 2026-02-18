package ha

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockFailoverClient — mock для TryFailover (идемпотентность и quorum).
type mockFailoverClient struct {
	mu                sync.Mutex
	fetchStatusCalls  int
	maintenanceCalls  int
	standbyReachable  bool
	standbyHAState    string
	standbyRole       string
	maintenanceErr    error
}

func (m *mockFailoverClient) FetchStatus(ctx context.Context, node string) Observation {
	m.mu.Lock()
	m.fetchStatusCalls++
	m.mu.Unlock()
	if node != NodeStandby {
		return Observation{Node: node, Reachable: false}
	}
	return Observation{
		Node:      NodeStandby,
		Reachable: m.standbyReachable,
		HAState:   m.standbyHAState,
		Role:      m.standbyRole,
		ObservedAt: time.Now().UTC(),
	}
}

func (m *mockFailoverClient) MaintenanceStart(ctx context.Context, node string) error {
	m.mu.Lock()
	m.maintenanceCalls++
	err := m.maintenanceErr
	m.mu.Unlock()
	return err
}

func (m *mockFailoverClient) setStandbyOK(haState, role string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.standbyReachable = true
	m.standbyHAState = haState
	m.standbyRole = role
}

func (m *mockFailoverClient) maintenanceCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maintenanceCalls
}

func TestTryFailover_idempotent_secondCallNoMaintenance(t *testing.T) {
	ctx := context.Background()
	store := NewStateStore()
	minFailures := 3

	// Primary недоступен 3 раза, standby доступен и в partner-down
	for i := 0; i < minFailures; i++ {
		store.RecordObservation(Observation{Node: NodePrimary, Reachable: false, Err: errTest})
	}
	store.RecordObservation(Observation{Node: NodeStandby, Reachable: true, HAState: HAStatePartnerDown, Role: RoleStandby})

	mock := &mockFailoverClient{}
	mock.setStandbyOK(HAStatePartnerDown, RoleStandby)

	log := slog.Default()
	err := TryFailover(ctx, store, mock, minFailures, log)
	if err != nil {
		t.Fatalf("first TryFailover: %v", err)
	}
	if mock.maintenanceCallCount() != 1 {
		t.Errorf("MaintenanceStart must be called once, got %d", mock.maintenanceCallCount())
	}
	st := store.GetStatus()
	if st.CurrentRole != RoleStandby {
		t.Errorf("CurrentRole = %q, want standby", st.CurrentRole)
	}

	// Второй вызов — идемпотентно: роль уже standby, MaintenanceStart не вызывать
	err2 := TryFailover(ctx, store, mock, minFailures, log)
	if err2 != nil {
		t.Fatalf("second TryFailover: %v", err2)
	}
	if mock.maintenanceCallCount() != 1 {
		t.Errorf("idempotent: MaintenanceStart must still be 1, got %d", mock.maintenanceCallCount())
	}
}

var errTest = errors.New("test error")

func TestTryFailover_quorumFail_noMaintenance(t *testing.T) {
	ctx := context.Background()
	store := NewStateStore()
	minFailures := 3
	for i := 0; i < minFailures; i++ {
		store.RecordObservation(Observation{Node: NodePrimary, Reachable: false})
	}
	store.RecordObservation(Observation{Node: NodeStandby, Reachable: true, HAState: HAStateHotStandby, Role: RoleStandby})

	mock := &mockFailoverClient{}
	mock.setStandbyOK(HAStateHotStandby, RoleStandby) // peer ещё видит hot-standby, не partner-down

	err := TryFailover(ctx, store, mock, minFailures, slog.Default())
	if err != nil {
		t.Fatalf("TryFailover should not error on quorum fail: %v", err)
	}
	if mock.maintenanceCallCount() != 0 {
		t.Errorf("quorum failed: MaintenanceStart must not be called, got %d", mock.maintenanceCallCount())
	}
}

func TestTryFailover_primaryNotDownYet_noMaintenance(t *testing.T) {
	ctx := context.Background()
	store := NewStateStore()
	store.RecordObservation(Observation{Node: NodePrimary, Reachable: true, Role: RolePrimary})
	store.RecordObservation(Observation{Node: NodeStandby, Reachable: true, Role: RoleStandby})

	mock := &mockFailoverClient{}
	mock.setStandbyOK(HAStateHotStandby, RoleStandby)

	err := TryFailover(ctx, store, mock, 3, slog.Default())
	if err != nil {
		t.Fatalf("TryFailover: %v", err)
	}
	if mock.maintenanceCallCount() != 0 {
		t.Errorf("primary up: MaintenanceStart must not be called, got %d", mock.maintenanceCallCount())
	}
}

func TestTryFailover_standbyUnreachable_noMaintenance(t *testing.T) {
	ctx := context.Background()
	store := NewStateStore()
	for i := 0; i < 3; i++ {
		store.RecordObservation(Observation{Node: NodePrimary, Reachable: false})
	}
	store.RecordObservation(Observation{Node: NodeStandby, Reachable: false})

	mock := &mockFailoverClient{}
	mock.standbyReachable = false

	err := TryFailover(ctx, store, mock, 3, slog.Default())
	if err != nil {
		t.Fatalf("TryFailover: %v", err)
	}
	if mock.maintenanceCallCount() != 0 {
		t.Errorf("standby unreachable: MaintenanceStart must not be called, got %d", mock.maintenanceCallCount())
	}
}

// TestTryFailover_logsStateTransition проверяет, что переход (failover) залогирован (observability_rules).
func TestTryFailover_logsStateTransition(t *testing.T) {
	ctx := context.Background()
	store := NewStateStore()
	for i := 0; i < 3; i++ {
		store.RecordObservation(Observation{Node: NodePrimary, Reachable: false})
	}
	store.RecordObservation(Observation{Node: NodeStandby, Reachable: true, HAState: HAStatePartnerDown, Role: RoleStandby})

	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mock := &mockFailoverClient{}
	mock.setStandbyOK(HAStatePartnerDown, RoleStandby)

	_ = TryFailover(ctx, store, mock, 3, log)
	logStr := logBuf.String()
	if !strings.Contains(logStr, "failover_triggered") {
		t.Errorf("log must contain failover_triggered (state transition). Got: %s", logStr)
	}
	if !strings.Contains(logStr, "new_role=standby") {
		t.Errorf("log must contain new_role=standby. Got: %s", logStr)
	}
}
