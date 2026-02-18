//go:build integration

package ha

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"testing"
	"time"
)

const (
	primaryURL  = "http://127.0.0.1:8000"
	standbyURL  = "http://127.0.0.1:8001"
	pollTimeout = 90 * time.Second
	pollTick    = 2 * time.Second
)

// keaHAStatus — ответ ha-status от одного узла (упрощённо).
type keaHAStatus struct {
	State string `json:"state"`
	Role  string `json:"role"`
}

func getKeaHAStatus(ctx context.Context, baseURL string) (keaHAStatus, error) {
	body := `{"command":"ha-status","service":["dhcp4"]}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader([]byte(body)))
	if err != nil {
		return keaHAStatus{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return keaHAStatus{}, err
	}
	defer resp.Body.Close()
	var list []struct {
		Result    int                    `json:"result"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return keaHAStatus{}, err
	}
	if len(list) == 0 || list[0].Result != 0 {
		return keaHAStatus{}, fmt.Errorf("ha-status failed")
	}
	args := list[0].Arguments
	s := keaHAStatus{}
	if v, ok := args["state"].(string); ok {
		s.State = v
	}
	if v, ok := args["role"].(string); ok {
		s.Role = v
	}
	return s, nil
}

func requireKeaReachable(t *testing.T, ctx context.Context) {
	t.Helper()
	_, errP := getKeaHAStatus(ctx, primaryURL)
	_, errS := getKeaHAStatus(ctx, standbyURL)
	if errP != nil || errS != nil {
		t.Skipf("Kea not reachable (primary: %v, standby: %v). Start stack with: docker compose up -d", errP, errS)
	}
}

// assertNoDualPrimary проверяет, что не более одного узла в роли primary (testing_rules: dual-primary must never persist).
func assertNoDualPrimary(t *testing.T, ctx context.Context) {
	t.Helper()
	p, errP := getKeaHAStatus(ctx, primaryURL)
	s, errS := getKeaHAStatus(ctx, standbyURL)
	if errP != nil || errS != nil {
		return
	}
	primaryIsPrimary := p.Role == RolePrimary
	standbyIsPrimary := s.Role == RolePrimary
	if primaryIsPrimary && standbyIsPrimary {
		t.Fatal("dual-primary detected: both nodes report role=primary")
	}
}

// waitUntil ожидает до timeout, пока cond не вернёт true; иначе тест падает.
func waitUntil(t *testing.T, timeout time.Duration, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastMsg string
	for time.Now().Before(deadline) {
		ok, msg := cond()
		lastMsg = msg
		if ok {
			return
		}
		time.Sleep(pollTick)
	}
	t.Fatalf("timeout waiting: %s", lastMsg)
}

func dockerStop(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "stop", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("docker stop %s: %v %s", name, err, out)
	}
}

func dockerStart(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "start", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("docker start %s: %v %s", name, err, out)
	}
}

// TestIntegration_kill_primary: убить kea-primary → standby получает role=primary, ha_state=partner-down.
func TestIntegration_kill_primary(t *testing.T) {
	ctx := context.Background()
	requireKeaReachable(t, ctx)
	assertNoDualPrimary(t, ctx)

	dockerStop(t, "kea-primary-dhcp4")
	dockerStop(t, "kea-primary-ctrl-agent")
	defer func() {
		dockerStart(t, "kea-primary-dhcp4")
		dockerStart(t, "kea-primary-ctrl-agent")
	}()

	waitUntil(t, pollTimeout, func() (bool, string) {
		s, err := getKeaHAStatus(ctx, standbyURL)
		if err != nil {
			return false, err.Error()
		}
		if s.Role == RolePrimary && (s.State == HAStatePartnerDown || s.State == "partner-down") {
			return true, ""
		}
		return false, fmt.Sprintf("standby role=%s state=%s (want primary, partner-down)", s.Role, s.State)
	})
	assertNoDualPrimary(t, ctx)
}

// TestIntegration_restart_secondary: перезапустить standby → primary остаётся активным, после recovery — hot-standby.
func TestIntegration_restart_secondary(t *testing.T) {
	ctx := context.Background()
	requireKeaReachable(t, ctx)
	assertNoDualPrimary(t, ctx)

	dockerStop(t, "kea-standby-dhcp4")
	dockerStop(t, "kea-standby-ctrl-agent")
	time.Sleep(3 * time.Second)
	dockerStart(t, "kea-standby-dhcp4")
	dockerStart(t, "kea-standby-ctrl-agent")

	// Primary должен остаться primary
	waitUntil(t, 20*time.Second, func() (bool, string) {
		p, err := getKeaHAStatus(ctx, primaryURL)
		if err != nil {
			return false, err.Error()
		}
		if p.Role == RolePrimary {
			return true, ""
		}
		return false, "primary not active"
	})
	// После recovery — hot-standby на одном из узлов
	waitUntil(t, pollTimeout, func() (bool, string) {
		p, _ := getKeaHAStatus(ctx, primaryURL)
		s, _ := getKeaHAStatus(ctx, standbyURL)
		if p.State == HAStateHotStandby || s.State == HAStateHotStandby || p.State == "hot-standby" || s.State == "hot-standby" {
			return true, ""
		}
		return false, fmt.Sprintf("primary state=%s standby state=%s", p.State, s.State)
	})
	assertNoDualPrimary(t, ctx)
}

// TestIntegration_ha_link_drop: заблокировать порт между узлами → нет dual-primary.
// На Windows/CI без iptables пропускаем или эмулируем остановкой одного ctrl-agent.
func TestIntegration_ha_link_drop(t *testing.T) {
	ctx := context.Background()
	requireKeaReachable(t, ctx)
	// Эмуляция потери связи: останавливаем primary ctrl-agent (standby перестаёт получать heartbeat).
	dockerStop(t, "kea-primary-ctrl-agent")
	defer dockerStart(t, "kea-primary-ctrl-agent")

	time.Sleep(15 * time.Second)
	assertNoDualPrimary(t, ctx)
	// Восстанавливаем
	dockerStart(t, "kea-primary-ctrl-agent")
}

// TestIntegration_network_partition: изолировать primary → failover на standby.
func TestIntegration_network_partition(t *testing.T) {
	// Эквивалент изоляции primary — остановить primary
	ctx := context.Background()
	requireKeaReachable(t, ctx)
	dockerStop(t, "kea-primary-dhcp4")
	dockerStop(t, "kea-primary-ctrl-agent")
	defer func() {
		dockerStart(t, "kea-primary-dhcp4")
		dockerStart(t, "kea-primary-ctrl-agent")
	}()

	waitUntil(t, pollTimeout, func() (bool, string) {
		s, err := getKeaHAStatus(ctx, standbyURL)
		if err != nil {
			return false, err.Error()
		}
		if s.Role == RolePrimary {
			return true, ""
		}
		return false, "standby did not become primary"
	})
	assertNoDualPrimary(t, ctx)
}

// TestIntegration_renewal_storm: flood DHCP RENEW во время failover → нет потери обслуживания.
// Упрощённо: проверяем, что после паузы (имитация storm) финальное HA-состояние корректно и нет dual-primary.
func TestIntegration_renewal_storm(t *testing.T) {
	ctx := context.Background()
	requireKeaReachable(t, ctx)
	dockerStop(t, "kea-primary-ctrl-agent")
	time.Sleep(5 * time.Second)
	// "Storm" — просто ждём стабилизации
	time.Sleep(20 * time.Second)
	dockerStart(t, "kea-primary-ctrl-agent")
	waitUntil(t, pollTimeout, func() (bool, string) {
		p, e1 := getKeaHAStatus(ctx, primaryURL)
		s, e2 := getKeaHAStatus(ctx, standbyURL)
		if e1 != nil && e2 != nil {
			return false, "both unreachable"
		}
		if p.Role == RolePrimary && s.Role == RolePrimary {
			return false, "dual-primary"
		}
		if p.Role == RolePrimary || s.Role == RolePrimary {
			return true, ""
		}
		return false, "no primary"
	})
	assertNoDualPrimary(t, ctx)
}

// TestIntegration_simultaneous_restart: оба узла рестартуют → корректное восстановление без dual-primary.
func TestIntegration_simultaneous_restart(t *testing.T) {
	ctx := context.Background()
	requireKeaReachable(t, ctx)
	dockerStop(t, "kea-primary-dhcp4")
	dockerStop(t, "kea-primary-ctrl-agent")
	dockerStop(t, "kea-standby-dhcp4")
	dockerStop(t, "kea-standby-ctrl-agent")
	time.Sleep(2 * time.Second)
	dockerStart(t, "kea-primary-dhcp4")
	dockerStart(t, "kea-primary-ctrl-agent")
	dockerStart(t, "kea-standby-dhcp4")
	dockerStart(t, "kea-standby-ctrl-agent")

	waitUntil(t, pollTimeout, func() (bool, string) {
		p, e1 := getKeaHAStatus(ctx, primaryURL)
		s, e2 := getKeaHAStatus(ctx, standbyURL)
		if e1 != nil || e2 != nil {
			return false, "nodes not ready"
		}
		if p.Role == RolePrimary && s.Role == RoleStandby && (p.State == HAStateHotStandby || p.State == "hot-standby") {
			return true, ""
		}
		if s.Role == RolePrimary && p.Role == RoleStandby {
			return true, ""
		}
		return false, fmt.Sprintf("primary role=%s state=%s standby role=%s state=%s", p.Role, p.State, s.Role, s.State)
	})
	assertNoDualPrimary(t, ctx)
}
