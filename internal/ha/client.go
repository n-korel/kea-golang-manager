package ha

import (
	"context"
	"fmt"
	"time"

	"kea-golang-manager/internal/kea"
)

// NodeName — идентификатор узла в паре.
const (
	NodePrimary = "primary"
	NodeStandby = "standby"
)

// HAClient — клиент к HA-эндпоинтам Kea (ha-status, ha-maintenance) для обоих узлов.
// Не содержит глобального состояния; создаётся с двумя URL и таймаутом.
type HAClient struct {
	primary *kea.Client
	standby *kea.Client
}

// NewHAClient создаёт клиент для primary и standby Control Agent.
func NewHAClient(primaryURL, standbyURL string, timeout time.Duration) *HAClient {
	return &HAClient{
		primary: kea.NewClient(primaryURL, timeout),
		standby: kea.NewClient(standbyURL, timeout),
	}
}

// client возвращает клиент Kea для узла node.
func (c *HAClient) client(node string) *kea.Client {
	if node == NodeStandby {
		return c.standby
	}
	return c.primary
}

// HAStatusRequest — команда ha-status для dhcp4.
func haStatusCommand() kea.Command {
	return kea.Command{
		Command: "ha-status",
		Service: []string{"dhcp4"},
	}
}

// FetchStatus вызывает ha-status на указанном узле и возвращает наблюдение (доступность, роль, состояние).
func (c *HAClient) FetchStatus(ctx context.Context, node string) Observation {
	obs := Observation{
		Node:       node,
		ObservedAt: time.Now().UTC(),
	}
	cli := c.client(node)
	resp, err := cli.ExecuteCommand(ctx, haStatusCommand())
	if err != nil {
		obs.Reachable = false
		obs.Err = err
		obs.HAState = HAStateReady
		obs.Role = RoleUnknown
		return obs
	}
	if resp.Result != 0 {
		obs.Reachable = true
		obs.HAState = HAStateReady
		obs.Role = RoleUnknown
		return obs
	}
	obs.Reachable = true
	obs.HAState = parseHAState(resp.Arguments)
	obs.Role = parseRole(resp.Arguments)
	return obs
}

// Heartbeat выполняет проверку живости узла (через ha-status; при успешном ответе узел жив).
func (c *HAClient) Heartbeat(ctx context.Context, node string) (reachable bool, err error) {
	obs := c.FetchStatus(ctx, node)
	return obs.Reachable, obs.Err
}

// parseHAState извлекает состояние HA из arguments ответа ha-status.
// Kea может возвращать "state" или вложенные структуры; обрабатываем типичные ключи.
func parseHAState(args map[string]interface{}) string {
	if args == nil {
		return HAStateReady
	}
	// Прямое поле state (часто в верхнем уровне).
	if s, ok := args["state"].(string); ok && s != "" {
		return normalizeHAState(s)
	}
	// Вложенный high-availability или ha
	if ha, ok := args["high-availability"].(map[string]interface{}); ok {
		if s, ok := ha["state"].(string); ok && s != "" {
			return normalizeHAState(s)
		}
	}
	return HAStateReady
}

// MaintenanceStart запускает режим обслуживания на узле (ha-maintenance-start).
// Вызывается на живом узле при подтверждённом (quorum) падении партнёра.
func (c *HAClient) MaintenanceStart(ctx context.Context, node string) error {
	cli := c.client(node)
	cmd := kea.Command{
		Command: "ha-maintenance-start",
		Service: []string{"dhcp4"},
	}
	resp, err := cli.ExecuteCommand(ctx, cmd)
	if err != nil {
		return err
	}
	if resp.Result != 0 {
		return fmt.Errorf("kea ha-maintenance-start result %d: %s", resp.Result, resp.Text)
	}
	return nil
}

// parseRole извлекает роль текущего сервера из arguments.
func parseRole(args map[string]interface{}) string {
	if args == nil {
		return RoleUnknown
	}
	if r, ok := args["role"].(string); ok && r != "" {
		return normalizeRole(r)
	}
	if ha, ok := args["high-availability"].(map[string]interface{}); ok {
		if r, ok := ha["role"].(string); ok && r != "" {
			return normalizeRole(r)
		}
		if name, ok := ha["this-server-name"].(string); ok && name != "" {
			return normalizeRole(name)
		}
	}
	return RoleUnknown
}
