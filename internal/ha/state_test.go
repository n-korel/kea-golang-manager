package ha

import (
	"errors"
	"testing"
)

func TestNewStateStore_InitialState(t *testing.T) {
	s := NewStateStore()
	st := s.GetStatus()
	if st.CurrentRole != RoleUnknown {
		t.Errorf("initial CurrentRole = %q, want %q", st.CurrentRole, RoleUnknown)
	}
	if st.PeerStatus != PeerUnknown {
		t.Errorf("initial PeerStatus = %q, want %q", st.PeerStatus, PeerUnknown)
	}
	if st.HAState != HAStateReady {
		t.Errorf("initial HAState = %q, want %q", st.HAState, HAStateReady)
	}
	if s.PrimaryFailureCount() != 0 || s.StandbyFailureCount() != 0 {
		t.Errorf("initial failure counts must be 0")
	}
}

func TestStateMachine_Transitions_primaryUp_standbyUp_hotStandby(t *testing.T) {
	s := NewStateStore()
	s.RecordObservation(Observation{
		Node:      NodePrimary,
		Reachable: true,
		HAState:   HAStateHotStandby,
		Role:      RolePrimary,
	})
	s.RecordObservation(Observation{
		Node:      NodeStandby,
		Reachable: true,
		HAState:   HAStateHotStandby,
		Role:      RoleStandby,
	})
	st := s.GetStatus()
	if st.CurrentRole != RolePrimary {
		t.Errorf("CurrentRole = %q, want primary", st.CurrentRole)
	}
	if st.PeerStatus != PeerOnline {
		t.Errorf("PeerStatus = %q, want online", st.PeerStatus)
	}
	if st.HAState != HAStateHotStandby {
		t.Errorf("HAState = %q, want hot-standby", st.HAState)
	}
}

func TestStateMachine_Transitions_primaryDown_standbyUp_partnerDown(t *testing.T) {
	s := NewStateStore()
	// Сначала оба работают
	s.RecordObservation(Observation{Node: NodePrimary, Reachable: true, HAState: HAStateHotStandby, Role: RolePrimary})
	s.RecordObservation(Observation{Node: NodeStandby, Reachable: true, HAState: HAStateHotStandby, Role: RoleStandby})
	// Primary падает, standby видит partner-down. Роль до RecordRoleChange остаётся unknown.
	s.RecordObservation(Observation{Node: NodePrimary, Reachable: false, Err: errors.New("unreachable")})
	s.RecordObservation(Observation{Node: NodeStandby, Reachable: true, HAState: HAStatePartnerDown, Role: RoleStandby})
	st := s.GetStatus()
	if st.CurrentRole != RoleUnknown {
		t.Errorf("CurrentRole = %q, want unknown until RecordRoleChange(standby)", st.CurrentRole)
	}
	if st.HAState != HAStatePartnerDown {
		t.Errorf("HAState = %q, want partner-down", st.HAState)
	}
	if st.PeerStatus != PeerOffline {
		t.Errorf("PeerStatus = %q, want offline", st.PeerStatus)
	}
	// После RecordRoleChange — роль standby
	s.RecordRoleChange(RoleStandby, FailoverReasonControlAgentCrash)
	st = s.GetStatus()
	if st.CurrentRole != RoleStandby {
		t.Errorf("after RecordRoleChange CurrentRole = %q, want standby", st.CurrentRole)
	}
}

func TestStateMachine_Transitions_communicationRecovery(t *testing.T) {
	s := NewStateStore()
	s.RecordObservation(Observation{
		Node:      NodePrimary,
		Reachable: true,
		HAState:   HAStateCommunicationRecovery,
		Role:      RolePrimary,
	})
	s.RecordObservation(Observation{
		Node:      NodeStandby,
		Reachable: true,
		HAState:   HAStateCommunicationRecovery,
		Role:      RoleStandby,
	})
	st := s.GetStatus()
	if st.HAState != HAStateCommunicationRecovery {
		t.Errorf("HAState = %q, want communication-recovery", st.HAState)
	}
}

func TestStateMachine_Transitions_ready_and_terminated(t *testing.T) {
	s := NewStateStore()
	s.RecordObservation(Observation{Node: NodePrimary, Reachable: true, HAState: HAStateReady, Role: RolePrimary})
	s.RecordObservation(Observation{Node: NodeStandby, Reachable: false})
	st := s.GetStatus()
	if st.HAState != HAStateReady {
		t.Errorf("HAState = %q, want ready", st.HAState)
	}

	s.RecordObservation(Observation{Node: NodePrimary, Reachable: true, HAState: HAStateTerminated, Role: RolePrimary})
	st = s.GetStatus()
	if st.HAState != HAStateTerminated {
		t.Errorf("HAState = %q, want terminated", st.HAState)
	}
}

func TestStateMachine_MinFailures_primary(t *testing.T) {
	s := NewStateStore()
	for i := 0; i < 3; i++ {
		s.RecordObservation(Observation{Node: NodePrimary, Reachable: false, Err: errors.New("fail")})
	}
	if s.PrimaryFailureCount() != 3 {
		t.Errorf("PrimaryFailureCount() = %d, want 3", s.PrimaryFailureCount())
	}
	// Один успех — сброс счётчика
	s.RecordObservation(Observation{Node: NodePrimary, Reachable: true, HAState: HAStateHotStandby, Role: RolePrimary})
	if s.PrimaryFailureCount() != 0 {
		t.Errorf("after success PrimaryFailureCount() = %d, want 0", s.PrimaryFailureCount())
	}
}

func TestStateMachine_MinFailures_standby(t *testing.T) {
	s := NewStateStore()
	for i := 0; i < 2; i++ {
		s.RecordObservation(Observation{Node: NodeStandby, Reachable: false})
	}
	if s.StandbyFailureCount() != 2 {
		t.Errorf("StandbyFailureCount() = %d, want 2", s.StandbyFailureCount())
	}
}

func TestSplitBrainDetection_singleCurrentRole(t *testing.T) {
	// Оба узла доступны и оба сообщают role=primary (split-brain).
	// Агрегат выдаёт одну роль: приоритет у primary (primary reachable -> его роль).
	s := NewStateStore()
	s.RecordObservation(Observation{
		Node:      NodePrimary,
		Reachable: true,
		HAState:   HAStatePartnerDown,
		Role:      RolePrimary,
	})
	s.RecordObservation(Observation{
		Node:      NodeStandby,
		Reachable: true,
		HAState:   HAStatePartnerDown,
		Role:      RolePrimary, // dual-primary с точки зрения пиров
	})
	st := s.GetStatus()
	if st.CurrentRole != RolePrimary && st.CurrentRole != RoleStandby && st.CurrentRole != RoleUnknown {
		t.Errorf("CurrentRole must be one of primary/standby/unknown, got %q", st.CurrentRole)
	}
	if st.CurrentRole != RolePrimary {
		t.Errorf("when primary reachable, primary is preferred: CurrentRole = %q", st.CurrentRole)
	}
	if st.PeerStatus != PeerOnline {
		t.Errorf("PeerStatus = %q", st.PeerStatus)
	}
}

func TestRecordRoleChange_updatesStatus(t *testing.T) {
	s := NewStateStore()
	reason := FailoverReasonControlAgentCrash
	s.RecordRoleChange(RoleStandby, reason)
	st := s.GetStatus()
	if st.CurrentRole != RoleStandby {
		t.Errorf("CurrentRole = %q, want standby", st.CurrentRole)
	}
	if st.LastFailoverReason == nil || *st.LastFailoverReason != reason {
		t.Errorf("LastFailoverReason = %v", st.LastFailoverReason)
	}
	if st.LastRoleChangeAt == nil {
		t.Errorf("LastRoleChangeAt must be set")
	}
}

func TestGetStatusSnapshot_returnsCopyOfObservations(t *testing.T) {
	s := NewStateStore()
	s.RecordObservation(Observation{Node: NodePrimary, Reachable: true, Role: RolePrimary, HAState: HAStateHotStandby})
	s.RecordObservation(Observation{Node: NodeStandby, Reachable: true, Role: RoleStandby, HAState: HAStateHotStandby})
	st, pObs, sObs := s.GetStatusSnapshot()
	if st.CurrentRole != RolePrimary {
		t.Errorf("CurrentRole = %q", st.CurrentRole)
	}
	if pObs == nil || !pObs.Reachable || pObs.Role != RolePrimary {
		t.Errorf("primary observation invalid: %+v", pObs)
	}
	if sObs == nil || !sObs.Reachable || sObs.Role != RoleStandby {
		t.Errorf("standby observation invalid: %+v", sObs)
	}
}

func TestNormalizeHAState_and_normalizeRole(t *testing.T) {
	if normalizeHAState("hot-standby") != HAStateHotStandby {
		t.Error("normalizeHAState hot-standby")
	}
	if normalizeHAState("unknown") != HAStateReady {
		t.Error("unknown -> ready")
	}
	if normalizeRole("primary") != RolePrimary || normalizeRole("standby") != RoleStandby {
		t.Error("normalizeRole")
	}
	if normalizeRole("") != RoleUnknown {
		t.Error("empty role -> unknown")
	}
}
