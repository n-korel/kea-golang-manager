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

// Reload перезагружает конфигурацию
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

// AddSubnet добавляет подсеть
func (c *Client) AddSubnet(ctx context.Context, subnet Subnet4) error {
	// Не все сборки Kea поддерживают online-команды вида "subnet4-add".
	// Универсальный путь через Control Agent:
	//   config-get -> правка -> config-set -> config-write.
	cfg, err := c.GetConfig(ctx)
	if err != nil {
		return err
	}

	var dhcp4 map[string]interface{}
	if v, ok := cfg["Dhcp4"].(map[string]interface{}); ok {
		dhcp4 = v
	} else if v, ok := any(cfg).(map[string]interface{}); ok {
		// если GetConfig вернул "чистый" dhcp4 конфиг
		dhcp4 = v
	} else {
		return fmt.Errorf("unexpected config shape")
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
				switch n := v.(type) {
				case float64:
					if int(n) > maxID {
						maxID = int(n)
					}
				case int:
					if n > maxID {
						maxID = n
					}
				}
			}
		}
	}

	// Добавляем новый subnet4 как map (через marshal/unmarshal для простоты)
	subnetBytes, err := json.Marshal(subnet)
	if err != nil {
		return fmt.Errorf("failed to marshal subnet: %w", err)
	}
	var subnetMap map[string]any
	if err := json.Unmarshal(subnetBytes, &subnetMap); err != nil {
		return fmt.Errorf("failed to unmarshal subnet: %w", err)
	}
	// Kea требует обязательный уникальный id для каждой подсети.
	if _, ok := subnetMap["id"]; !ok || subnetMap["id"] == 0 {
		subnetMap["id"] = maxID + 1
	}
	subnet4List = append(subnet4List, subnetMap)
	dhcp4["subnet4"] = subnet4List

	// 1) применяем новую конфигурацию к работающему серверу
	if err := c.SetConfig(ctx, map[string]any{"Dhcp4": dhcp4}); err != nil {
		return err
	}

	// 2) сохраняем конфигурацию в JSON-файл на диске
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
