package ha

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kea-golang-manager/internal/kea"
)

// keaMockHandler — HTTP handler, имитирующий Kea Control Agent.
// По полю "command" в теле запроса отдаёт ha-heartbeat или config-get ответ.
type keaMockHandler struct {
	heartbeatState   string // для ha-heartbeat (пусто = 500)
	configGetStatus  int    // 200 или 500 для config-get
	heartbeatStatus  int    // 200 или 500 для ha-heartbeat
}

func (h *keaMockHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Command string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch body.Command {
	case "ha-heartbeat":
		status := h.heartbeatStatus
		if status == 0 {
			status = http.StatusOK
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		state := h.heartbeatState
		if state == "" {
			state = kea.HAStateHotStandby
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result":    0,
			"arguments": map[string]any{"state": state},
		})
	case "config-get":
		status := h.configGetStatus
		if status == 0 {
			status = http.StatusOK
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result":    0,
			"arguments": map[string]any{"Dhcp4": map[string]any{}},
		})
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func TestHAManager_Status(t *testing.T) {
	t.Run("both reachable, hot-standby", func(t *testing.T) {
		primary := httptest.NewServer(&keaMockHandler{heartbeatState: kea.HAStateHotStandby})
		defer primary.Close()
		standby := httptest.NewServer(&keaMockHandler{heartbeatState: kea.HAStateHotStandby})
		defer standby.Close()

		m := NewHAManager(
			kea.NewClient(primary.URL, 2*time.Second),
			kea.NewClient(standby.URL, 2*time.Second),
		)
		ctx := context.Background()

		status, err := m.Status(ctx)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if !status.Primary.Reachable || status.Primary.State != kea.HAStateHotStandby || status.Primary.Role != "primary" {
			t.Errorf("Primary: want reachable, state=hot-standby, role=primary; got reachable=%v state=%q role=%q",
				status.Primary.Reachable, status.Primary.State, status.Primary.Role)
		}
		if !status.Standby.Reachable || status.Standby.State != kea.HAStateHotStandby || status.Standby.Role != "standby" {
			t.Errorf("Standby: want reachable, state=hot-standby, role=standby; got reachable=%v state=%q role=%q",
				status.Standby.Reachable, status.Standby.State, status.Standby.Role)
		}
	})

	t.Run("primary unreachable", func(t *testing.T) {
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
		defer primary.Close()
		standby := httptest.NewServer(&keaMockHandler{heartbeatState: kea.HAStateHotStandby})
		defer standby.Close()

		m := NewHAManager(
			kea.NewClient(primary.URL, 2*time.Second),
			kea.NewClient(standby.URL, 2*time.Second),
		)
		ctx := context.Background()

		status, err := m.Status(ctx)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status.Primary.Reachable {
			t.Error("Primary should be unreachable")
		}
		if status.Primary.State != "unknown" {
			t.Errorf("Primary.State want unknown, got %q", status.Primary.State)
		}
		if !status.Standby.Reachable {
			t.Error("Standby should be reachable")
		}
	})

	t.Run("standby unreachable", func(t *testing.T) {
		primary := httptest.NewServer(&keaMockHandler{heartbeatState: kea.HAStateHotStandby})
		defer primary.Close()
		standby := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
		defer standby.Close()

		m := NewHAManager(
			kea.NewClient(primary.URL, 2*time.Second),
			kea.NewClient(standby.URL, 2*time.Second),
		)
		ctx := context.Background()

		status, err := m.Status(ctx)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if !status.Primary.Reachable {
			t.Error("Primary should be reachable")
		}
		if status.Standby.Reachable {
			t.Error("Standby should be unreachable")
		}
		if status.Standby.State != "unknown" {
			t.Errorf("Standby.State want unknown, got %q", status.Standby.State)
		}
	})
}

func TestHAManager_ActiveClient(t *testing.T) {
	t.Run("primary hot-standby returns primary", func(t *testing.T) {
		primary := httptest.NewServer(&keaMockHandler{heartbeatState: kea.HAStateHotStandby})
		defer primary.Close()
		standby := httptest.NewServer(&keaMockHandler{heartbeatState: kea.HAStateHotStandby})
		defer standby.Close()

		m := NewHAManager(
			kea.NewClient(primary.URL, 2*time.Second),
			kea.NewClient(standby.URL, 2*time.Second),
		)
		ctx := context.Background()

		client, err := m.ActiveClient(ctx)
		if err != nil {
			t.Fatalf("ActiveClient: %v", err)
		}
		// Проверяем, что это primary: запрос к primary должен пройти
		state, err := client.HAHeartbeat(ctx)
		if err != nil {
			t.Fatalf("HAHeartbeat: %v", err)
		}
		if state != kea.HAStateHotStandby {
			t.Errorf("expected hot-standby, got %q", state)
		}
		// Клиент должен быть именно primary (тот же baseURL по сути — сравниваем по поведению: primary отдаёт hot-standby)
		if client != m.primaryClient {
			t.Error("ActiveClient should return primary client when primary is reachable")
		}
	})

	t.Run("primary down standby partner-down returns standby", func(t *testing.T) {
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
		defer primary.Close()
		standby := httptest.NewServer(&keaMockHandler{heartbeatState: kea.HAStatePartnerDown})
		defer standby.Close()

		m := NewHAManager(
			kea.NewClient(primary.URL, 2*time.Second),
			kea.NewClient(standby.URL, 2*time.Second),
		)
		ctx := context.Background()

		client, err := m.ActiveClient(ctx)
		if err != nil {
			t.Fatalf("ActiveClient: %v", err)
		}
		if client != m.standbyClient {
			t.Error("ActiveClient should return standby when primary down and standby is partner-down")
		}
	})

	t.Run("both unreachable returns error", func(t *testing.T) {
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
		defer primary.Close()
		standby := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
		defer standby.Close()

		m := NewHAManager(
			kea.NewClient(primary.URL, 2*time.Second),
			kea.NewClient(standby.URL, 2*time.Second),
		)
		ctx := context.Background()

		_, err := m.ActiveClient(ctx)
		if err == nil {
			t.Fatal("expected error when both unreachable")
		}
		if err.Error() != "no active kea node available" {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestHAManager_GuardedApply(t *testing.T) {
	t.Run("hot-standby fn called twice PrimaryOK StandbyOK no warning", func(t *testing.T) {
		primary := httptest.NewServer(&keaMockHandler{heartbeatState: kea.HAStateHotStandby})
		defer primary.Close()
		standby := httptest.NewServer(&keaMockHandler{heartbeatState: kea.HAStateHotStandby})
		defer standby.Close()

		m := NewHAManager(
			kea.NewClient(primary.URL, 2*time.Second),
			kea.NewClient(standby.URL, 2*time.Second),
		)
		ctx := context.Background()

		var callCount int
		fn := func(ctx context.Context, c *kea.Client) error {
			callCount++
			_, _ = c.HAHeartbeat(ctx)
			return nil
		}

		result, err := m.GuardedApply(ctx, fn)
		if err != nil {
			t.Fatalf("GuardedApply: %v", err)
		}
		if callCount != 2 {
			t.Errorf("fn should be called twice, got %d", callCount)
		}
		if !result.PrimaryOK || !result.StandbyOK {
			t.Errorf("PrimaryOK=%v StandbyOK=%v", result.PrimaryOK, result.StandbyOK)
		}
		if result.Warning != "" {
			t.Errorf("unexpected Warning: %q", result.Warning)
		}
		if result.HAState != kea.HAStateHotStandby {
			t.Errorf("HAState want hot-standby, got %q", result.HAState)
		}
	})

	t.Run("communication-recovery fn called once StandbyOK false warning", func(t *testing.T) {
		callCount := 0
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Command string `json:"command"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Command == "ha-heartbeat" {
				// первый вызов (в ActiveClient/Status) и после fn — отдаём hot-standby чтобы active=primary, потом communication-recovery
				callCount++
				state := kea.HAStateHotStandby
				if callCount > 1 {
					state = kea.HAStateCommunicationRecovery
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"result":    0,
					"arguments": map[string]any{"state": state},
				})
				return
			}
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer primary.Close()
		standby := httptest.NewServer(&keaMockHandler{heartbeatState: kea.HAStateHotStandby})
		defer standby.Close()

		m := NewHAManager(
			kea.NewClient(primary.URL, 2*time.Second),
			kea.NewClient(standby.URL, 2*time.Second),
		)
		ctx := context.Background()

		var fnCallCount int
		fn := func(ctx context.Context, c *kea.Client) error {
			fnCallCount++
			return nil
		}

		result, err := m.GuardedApply(ctx, fn)
		if err != nil {
			t.Fatalf("GuardedApply: %v", err)
		}
		if fnCallCount != 1 {
			t.Errorf("fn should be called once, got %d", fnCallCount)
		}
		if result.StandbyOK {
			t.Error("StandbyOK should be false")
		}
		if result.Warning == "" || !strings.Contains(result.Warning, "communication-recovery") {
			t.Errorf("Warning should contain communication-recovery, got %q", result.Warning)
		}
	})

	t.Run("standby fn fails StandbyOK false warning", func(t *testing.T) {
		primary := httptest.NewServer(&keaMockHandler{heartbeatState: kea.HAStateHotStandby})
		defer primary.Close()
		standby := httptest.NewServer(&keaMockHandler{
			heartbeatState:  kea.HAStateHotStandby,
			configGetStatus: http.StatusInternalServerError,
		})
		defer standby.Close()

		m := NewHAManager(
			kea.NewClient(primary.URL, 2*time.Second),
			kea.NewClient(standby.URL, 2*time.Second),
		)
		ctx := context.Background()

		fn := func(ctx context.Context, c *kea.Client) error {
			_, err := c.GetConfig(ctx)
			return err
		}

		result, err := m.GuardedApply(ctx, fn)
		if err != nil {
			t.Fatalf("GuardedApply: %v", err)
		}
		if result.StandbyOK {
			t.Error("StandbyOK should be false")
		}
		if !strings.Contains(result.Warning, "standby apply failed") {
			t.Errorf("Warning should contain 'standby apply failed', got %q", result.Warning)
		}
	})

	t.Run("primary fn fails returns error PrimaryOK false", func(t *testing.T) {
		primary := httptest.NewServer(&keaMockHandler{
			heartbeatState:  kea.HAStateHotStandby,
			configGetStatus: http.StatusInternalServerError,
		})
		defer primary.Close()
		standby := httptest.NewServer(&keaMockHandler{heartbeatState: kea.HAStateHotStandby})
		defer standby.Close()

		m := NewHAManager(
			kea.NewClient(primary.URL, 2*time.Second),
			kea.NewClient(standby.URL, 2*time.Second),
		)
		ctx := context.Background()

		fn := func(ctx context.Context, c *kea.Client) error {
			_, err := c.GetConfig(ctx)
			return err
		}

		result, err := m.GuardedApply(ctx, fn)
		if err == nil {
			t.Fatal("expected error when primary fn fails")
		}
		if result.PrimaryOK {
			t.Error("PrimaryOK should be false")
		}
	})
}

