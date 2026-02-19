package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kea-golang-manager/internal/ha"
	"kea-golang-manager/internal/kea"
	"kea-golang-manager/internal/service"
)

type mockHAStatus struct {
	status *ha.ClusterStatus
	err    error
}

func (m *mockHAStatus) Status(ctx context.Context) (*ha.ClusterStatus, error) {
	return m.status, m.err
}

type mockDHCPService struct {
	addSubnetResult    ha.ApplyResult
	addSubnetErr       error
	deleteSubnetResult ha.ApplyResult
	deleteSubnetErr    error
	reloadResult       ha.ApplyResult
	reloadErr          error
}

func (m *mockDHCPService) GetConfig(ctx context.Context) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}

func (m *mockDHCPService) ListSubnets(ctx context.Context) ([]kea.Subnet4, error) {
	return nil, nil
}

func (m *mockDHCPService) AddSubnet(ctx context.Context, subnet string, pools []string, reservations []kea.Reservation) (ha.ApplyResult, error) {
	return m.addSubnetResult, m.addSubnetErr
}

func (m *mockDHCPService) DeleteSubnet(ctx context.Context, id int) (ha.ApplyResult, error) {
	return m.deleteSubnetResult, m.deleteSubnetErr
}

func (m *mockDHCPService) WriteConfigAndReload(ctx context.Context) (ha.ApplyResult, error) {
	return m.reloadResult, m.reloadErr
}

func (m *mockDHCPService) Lease4Stats(ctx context.Context) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}

func TestHandler_GetHAStatus(t *testing.T) {
	t.Run("200 when hot-standby on both", func(t *testing.T) {
		haMock := &mockHAStatus{
			status: &ha.ClusterStatus{
				Primary: ha.NodeStatus{Role: "primary", State: kea.HAStateHotStandby, Reachable: true},
				Standby: ha.NodeStatus{Role: "standby", State: kea.HAStateHotStandby, Reachable: true},
			},
		}
		h := NewHandler(HandlerOpts{HAManager: haMock, DHCPService: &mockDHCPService{}})
		req := httptest.NewRequest(http.MethodGet, "/ha/status", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("GET /ha/status: want 200, got %d", rec.Code)
		}
		var body haStatusResponse
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.Primary.State != kea.HAStateHotStandby || !body.Primary.Reachable || body.Primary.Role != "primary" {
			t.Errorf("primary: got state=%q role=%q reachable=%v", body.Primary.State, body.Primary.Role, body.Primary.Reachable)
		}
		if body.Standby.State != kea.HAStateHotStandby || !body.Standby.Reachable || body.Standby.Role != "standby" {
			t.Errorf("standby: got state=%q role=%q reachable=%v", body.Standby.State, body.Standby.Role, body.Standby.Reachable)
		}
	})

	t.Run("503 when haManager.Status error", func(t *testing.T) {
		haMock := &mockHAStatus{err: errors.New("status failed")}
		h := NewHandler(HandlerOpts{HAManager: haMock, DHCPService: &mockDHCPService{}})
		req := httptest.NewRequest(http.MethodGet, "/ha/status", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET /ha/status on error: want 503, got %d", rec.Code)
		}
		var body errorResponse
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.Error != "status failed" {
			t.Errorf("error body: got %q", body.Error)
		}
	})
}

func TestHandler_PostSubnets(t *testing.T) {
	t.Run("201 when StandbyOK true", func(t *testing.T) {
		haMock := &mockHAStatus{status: &ha.ClusterStatus{}}
		svc := &mockDHCPService{
			addSubnetResult: ha.ApplyResult{PrimaryOK: true, StandbyOK: true},
		}
		h := NewHandler(HandlerOpts{HAManager: haMock, DHCPService: svc})
		req := httptest.NewRequest(http.MethodPost, "/subnets/", strings.NewReader(`{"subnet":"192.168.1.0/24"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Errorf("POST /subnets: want 201, got %d body=%s", rec.Code, rec.Body.Bytes())
		}
	})

	t.Run("207 when Warning != \"\"", func(t *testing.T) {
		haMock := &mockHAStatus{status: &ha.ClusterStatus{}}
		svc := &mockDHCPService{
			addSubnetResult: ha.ApplyResult{PrimaryOK: true, StandbyOK: false, Warning: "standby skipped", HAState: "communication-recovery"},
		}
		h := NewHandler(HandlerOpts{HAManager: haMock, DHCPService: svc})
		req := httptest.NewRequest(http.MethodPost, "/subnets/", strings.NewReader(`{"subnet":"192.168.1.0/24"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusMultiStatus {
			t.Errorf("POST /subnets with warning: want 207, got %d", rec.Code)
		}
		var body struct {
			Warning string `json:"warning"`
			HAState string `json:"ha_state"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Warning != "standby skipped" || body.HAState != "communication-recovery" {
			t.Errorf("want warning=standby skipped ha_state=communication-recovery, got %q %q", body.Warning, body.HAState)
		}
	})

	t.Run("400 when subnet field missing", func(t *testing.T) {
		h := NewHandler(HandlerOpts{HAManager: &mockHAStatus{}, DHCPService: &mockDHCPService{}})
		req := httptest.NewRequest(http.MethodPost, "/subnets/", strings.NewReader(`{"pools":[]}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST /subnets without subnet: want 400, got %d", rec.Code)
		}
	})
}

func TestHandler_DeleteSubnet(t *testing.T) {
	t.Run("204 when StandbyOK true", func(t *testing.T) {
		svc := &mockDHCPService{
			deleteSubnetResult: ha.ApplyResult{PrimaryOK: true, StandbyOK: true},
		}
		h := NewHandler(HandlerOpts{HAManager: &mockHAStatus{}, DHCPService: svc})
		req := httptest.NewRequest(http.MethodDelete, "/subnets/1", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Errorf("DELETE /subnets/1: want 204, got %d", rec.Code)
		}
	})

	t.Run("207 when Warning != \"\"", func(t *testing.T) {
		svc := &mockDHCPService{
			deleteSubnetResult: ha.ApplyResult{PrimaryOK: true, StandbyOK: false, Warning: "standby skipped", HAState: "partner-down"},
		}
		h := NewHandler(HandlerOpts{HAManager: &mockHAStatus{}, DHCPService: svc})
		req := httptest.NewRequest(http.MethodDelete, "/subnets/1", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusMultiStatus {
			t.Errorf("DELETE /subnets/1 with warning: want 207, got %d", rec.Code)
		}
		var body struct {
			Warning string `json:"warning"`
			HAState string `json:"ha_state"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Warning != "standby skipped" || body.HAState != "partner-down" {
			t.Errorf("want warning=standby skipped ha_state=partner-down, got %q %q", body.Warning, body.HAState)
		}
	})
}

// Проверка, что *service.DHCPService реализует dhcpServiceForHandler (компиляция).
var _ dhcpServiceForHandler = (*service.DHCPService)(nil)
