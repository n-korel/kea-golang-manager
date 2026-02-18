package kea

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client представляет HTTP клиент для Kea Control Agent
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient создает новый клиент для Kea Control Agent
func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// ExecuteCommand выполняет произвольную команду через REST API Control Agent.
func (c *Client) ExecuteCommand(ctx context.Context, cmd Command) (*Response, error) {
	return c.executeCommand(ctx, cmd)
}

// executeCommand выполняет команду через REST API.
// В разных версиях Kea/Control Agent встречаются оба формата:
// - запрос как объект (map)
// - запрос как массив объектов
//
// Практика: некоторые сборки ctrl-agent ожидают именно объект, поэтому
// отправляем объект, а ответ умеем распарсить и как объект, и как массив.
func (c *Client) executeCommand(ctx context.Context, cmd Command) (*Response, error) {
	url := fmt.Sprintf("%s", c.baseURL)

	body, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal command: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(bodyBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Ответ может быть либо объектом, либо массивом объектов.
	var single Response
	if err := json.Unmarshal(respBytes, &single); err == nil && (single.Result != 0 || single.Text != "" || single.Arguments != nil) {
		return &single, nil
	}

	var list []Response
	if err := json.Unmarshal(respBytes, &list); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("empty response from kea")
	}
	return &list[0], nil
}

// GetConfig получает текущую конфигурацию
func (c *Client) GetConfig(ctx context.Context) (map[string]interface{}, error) {
	cmd := Command{
		Command: "config-get",
		Service: []string{"dhcp4"},
	}

	resp, err := c.executeCommand(ctx, cmd)
	if err != nil {
		return nil, err
	}

	if resp.Result != 0 {
		return nil, fmt.Errorf("kea error: %s", resp.Text)
	}

	// Чаще всего forwarded config-get возвращает Dhcp4 как вложенный объект.
	if dhcp4Cfg, ok := resp.Arguments["Dhcp4"].(map[string]interface{}); ok {
		return map[string]interface{}{"Dhcp4": dhcp4Cfg}, nil
	}
	// В некоторых сборках встречается ключ "config".
	if cfg, ok := resp.Arguments["config"].(map[string]interface{}); ok {
		return cfg, nil
	}

	return nil, fmt.Errorf("invalid config format in response")
}

// SetConfig устанавливает новую конфигурацию
func (c *Client) SetConfig(ctx context.Context, config map[string]interface{}) error {
	// Kea ожидает аргументы конфигурации по ключу сервиса, например "Dhcp4".
	// Если caller передал уже {"Dhcp4": {...}}, используем как есть.
	args := any(config)
	if _, ok := config["Dhcp4"]; !ok {
		args = map[string]interface{}{"Dhcp4": config}
	}

	cmd := Command{
		Command: "config-set",
		Service: []string{"dhcp4"},
		Arguments: args,
	}

	resp, err := c.executeCommand(ctx, cmd)
	if err != nil {
		return err
	}

	if resp.Result != 0 {
		return fmt.Errorf("kea error: %s", resp.Text)
	}

	return nil
}

// Reload перезагружает конфигурацию (только config-reload).
func (c *Client) Reload(ctx context.Context) error {
	cmd := Command{
		Command: "config-reload",
		Service: []string{"dhcp4"},
	}

	resp, err := c.executeCommand(ctx, cmd)
	if err != nil {
		return err
	}

	if resp.Result != 0 {
		return fmt.Errorf("kea error: %s", resp.Text)
	}

	return nil
}

// WriteConfigAndReload выполняет config-write, затем config-reload на этом узле (reload_policy).
func (c *Client) WriteConfigAndReload(ctx context.Context) error {
	if err := c.writeConfig(ctx); err != nil {
		return err
	}
	return c.Reload(ctx)
}

// Lease4Stats возвращает статистику лизов DHCPv4 (команда statistic-get-all для dhcp4).
// При отсутствии хука или ошибке возвращает nil map и ошибку.
func (c *Client) Lease4Stats(ctx context.Context) (map[string]interface{}, error) {
	cmd := Command{
		Command:   "statistic-get-all",
		Service:   []string{"dhcp4"},
		Arguments: map[string]interface{}{},
	}
	resp, err := c.executeCommand(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if resp.Result != 0 {
		return nil, fmt.Errorf("kea error: %s", resp.Text)
	}
	if resp.Arguments == nil {
		return map[string]interface{}{}, nil
	}
	return resp.Arguments, nil
}

// ListSubnets возвращает список подсетей из текущей конфигурации.
func (c *Client) ListSubnets(ctx context.Context) ([]Subnet4, error) {
	cfg, err := c.GetConfig(ctx)
	if err != nil {
		return nil, err
	}

	dhcp4, err := extractDhcp4Config(cfg)
	if err != nil {
		return nil, err
	}

	rawSubnets, ok := dhcp4["subnet4"].([]any)
	if !ok || len(rawSubnets) == 0 {
		return []Subnet4{}, nil
	}

	subnets := make([]Subnet4, 0, len(rawSubnets))
	for _, s := range rawSubnets {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}

		b, err := json.Marshal(m)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal subnet: %w", err)
		}

		var subnet Subnet4
		if err := json.Unmarshal(b, &subnet); err != nil {
			return nil, fmt.Errorf("failed to unmarshal subnet: %w", err)
		}

		subnets = append(subnets, subnet)
	}

	return subnets, nil
}

// DeleteSubnet удаляет подсеть по её ID.
func (c *Client) DeleteSubnet(ctx context.Context, id int) error {
	cfg, err := c.GetConfig(ctx)
	if err != nil {
		return err
	}

	dhcp4, err := extractDhcp4Config(cfg)
	if err != nil {
		return err
	}

	subnets, ok := dhcp4["subnet4"].([]any)
	if !ok || len(subnets) == 0 {
		return fmt.Errorf("subnet with id %d not found", id)
	}

	newSubnets := make([]any, 0, len(subnets))
	found := false

	for _, s := range subnets {
		m, ok := s.(map[string]any)
		if !ok {
			newSubnets = append(newSubnets, s)
			continue
		}

		rawID, ok := m["id"]
		if !ok {
			newSubnets = append(newSubnets, s)
			continue
		}

		curID, ok := toInt(rawID)
		if !ok {
			newSubnets = append(newSubnets, s)
			continue
		}

		if curID == id {
			found = true
			continue
		}

		newSubnets = append(newSubnets, s)
	}

	if !found {
		return fmt.Errorf("subnet with id %d not found", id)
	}

	dhcp4["subnet4"] = newSubnets

	if err := c.SetConfig(ctx, map[string]any{"Dhcp4": dhcp4}); err != nil {
		return err
	}
	if err := c.writeConfig(ctx); err != nil {
		return err
	}

	return nil
}

const (
	addSubnetMaxRetries     = 5
	addSubnetInitialBackoff = 100 * time.Millisecond
	addSubnetMaxBackoff     = 2 * time.Second
)

// AddSubnet добавляет подсеть с оптимистичным ретраем при гонках конфигурации.
// Алгоритм:
//   1) config-get
//   2) добавление подсети и config-set
//   3) повторный config-get и проверка, что подсеть действительно присутствует
//   4) config-write после успешной валидации
//
// Если между нашими config-get и config-set другой клиент перезапишет конфиг,
// мы обнаружим, что "наша" подсеть отсутствует в свежем config-get и повторим
// операцию с экспоненциальной задержкой.
func (c *Client) AddSubnet(ctx context.Context, subnet Subnet4) error {
	backoff := addSubnetInitialBackoff

	for attempt := 0; attempt < addSubnetMaxRetries; attempt++ {
		assignedID, err := c.addSubnetOnce(ctx, subnet)
		if err != nil {
			// В случае ошибки сразу решаем — либо ретрай, либо выходим.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt == addSubnetMaxRetries-1 {
				return fmt.Errorf("failed to add subnet after %d attempts: %w", addSubnetMaxRetries, err)
			}
			if err := sleepWithBackoff(ctx, backoff); err != nil {
				return err
			}
			backoff = nextBackoff(backoff)
			continue
		}

		ok, verifyErr := c.verifySubnetPresent(ctx, assignedID, subnet.Subnet)
		if verifyErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt == addSubnetMaxRetries-1 {
				return fmt.Errorf("failed to verify subnet after %d attempts: %w", addSubnetMaxRetries, verifyErr)
			}
			if err := sleepWithBackoff(ctx, backoff); err != nil {
				return err
			}
			backoff = nextBackoff(backoff)
			continue
		}

		if !ok {
			// Конфигурация была перезаписана другим клиентом, пробуем ещё раз.
			if attempt == addSubnetMaxRetries-1 {
				return fmt.Errorf("concurrent config update detected while adding subnet %s", subnet.Subnet)
			}
			if err := sleepWithBackoff(ctx, backoff); err != nil {
				return err
			}
			backoff = nextBackoff(backoff)
			continue
		}

		// На этом этапе подсеть гарантированно присутствует в последнем config-get.
		if err := c.writeConfig(ctx); err != nil {
			return err
		}
		return nil
	}

	// Логически недостижимо, но необходимо для компиляции.
	return fmt.Errorf("unreachable AddSubnet state")
}

// addSubnetOnce выполняет одну попытку добавления подсети:
//   config-get -> правка -> config-set (без config-write).
// Возвращает присвоенный подсети id.
func (c *Client) addSubnetOnce(ctx context.Context, subnet Subnet4) (int, error) {
	cfg, err := c.GetConfig(ctx)
	if err != nil {
		return 0, err
	}

	dhcp4, err := extractDhcp4Config(cfg)
	if err != nil {
		return 0, err
	}

	// Берём текущий subnet4 (если есть)
	var subnet4List []any
	maxID := 0
	if existing, ok := dhcp4["subnet4"].([]any); ok {
		subnet4List = existing
		// ищем максимальный id среди уже существующих подсетей
		for _, s := range subnet4List {
			m, ok := s.(map[string]any)
			if !ok {
				continue
			}
			if v, ok := m["id"]; ok {
				if id, ok := toInt(v); ok && id > maxID {
					maxID = id
				}
			}
		}
	}

	// Добавляем новый subnet4 как map (через marshal/unmarshal для простоты)
	subnetBytes, err := json.Marshal(subnet)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal subnet: %w", err)
	}
	var subnetMap map[string]any
	if err := json.Unmarshal(subnetBytes, &subnetMap); err != nil {
		return 0, fmt.Errorf("failed to unmarshal subnet: %w", err)
	}

	// Kea требует обязательный уникальный id для каждой подсети.
	assignedID := 0
	if v, ok := subnetMap["id"]; ok {
		if id, ok := toInt(v); ok && id > 0 {
			assignedID = id
		}
	}
	if assignedID == 0 {
		assignedID = maxID + 1
		subnetMap["id"] = assignedID
	}

	subnet4List = append(subnet4List, subnetMap)
	dhcp4["subnet4"] = subnet4List

	// Применяем новую конфигурацию к работающему серверу
	if err := c.SetConfig(ctx, map[string]any{"Dhcp4": dhcp4}); err != nil {
		return 0, err
	}

	return assignedID, nil
}

// verifySubnetPresent проверяет по свежему config-get, что подсеть с заданным id
// и CIDR действительно присутствует. Это даёт оптимистичную гарантию, что
// наш config-set не был "перетёрт" другим клиентом.
func (c *Client) verifySubnetPresent(ctx context.Context, id int, cidr string) (bool, error) {
	cfg, err := c.GetConfig(ctx)
	if err != nil {
		return false, err
	}

	dhcp4, err := extractDhcp4Config(cfg)
	if err != nil {
		return false, err
	}

	subnets, ok := dhcp4["subnet4"].([]any)
	if !ok {
		return false, nil
	}

	for _, s := range subnets {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		rawID, ok := m["id"]
		if !ok {
			continue
		}
		curID, ok := toInt(rawID)
		if !ok || curID != id {
			continue
		}

		rawSubnet, ok := m["subnet"].(string)
		if !ok {
			// id совпал, но структура неожиданная — считаем, что конфиг изменён
			return false, nil
		}
		if rawSubnet == cidr {
			return true, nil
		}
		// id совпал, но другая подсеть — конфиг изменён конкурирующим клиентом
		return false, nil
	}

	return false, nil
}

// writeConfig выполняет config-write для текущей конфигурации.
func (c *Client) writeConfig(ctx context.Context) error {
	writeCmd := Command{
		Command: "config-write",
		Service: []string{"dhcp4"},
	}
	resp, err := c.executeCommand(ctx, writeCmd)
	if err != nil {
		return fmt.Errorf("config-write failed: %w", err)
	}
	if resp.Result != 0 {
		return fmt.Errorf("kea error during config-write: %s", resp.Text)
	}
	return nil
}

// extractDhcp4Config извлекает объект Dhcp4 из ответа config-get.
func extractDhcp4Config(cfg map[string]interface{}) (map[string]interface{}, error) {
	if v, ok := cfg["Dhcp4"].(map[string]interface{}); ok {
		return v, nil
	}
	if v, ok := any(cfg).(map[string]interface{}); ok {
		// если GetConfig вернул "чистый" dhcp4 конфиг
		return v, nil
	}
	return nil, fmt.Errorf("unexpected config shape")
}

// toInt аккуратно приводит значение id (float64/int/строка) к int.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float32:
		return int(n), true
	case float64:
		return int(n), true
	case string:
		var parsed int
		if _, err := fmt.Sscanf(n, "%d", &parsed); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

// sleepWithBackoff делает паузу с учётом контекста.
func sleepWithBackoff(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// nextBackoff считает следующий шаг экспоненциального backoff.
func nextBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return addSubnetInitialBackoff
	}
	next := current * 2
	if next > addSubnetMaxBackoff {
		return addSubnetMaxBackoff
	}
	return next
}
