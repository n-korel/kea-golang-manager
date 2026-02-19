package service

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"

	"kea-golang-manager/internal/ha"
	"kea-golang-manager/internal/kea"
)

// DHCPService предоставляет бизнес-логику для работы с DHCP
type DHCPService struct {
	haManager *ha.HAManager
}

// NewDHCPService создает новый сервис DHCP
func NewDHCPService(haManager *ha.HAManager) *DHCPService {
	return &DHCPService{
		haManager: haManager,
	}
}

// ValidateSubnet проверяет корректность подсети
func (s *DHCPService) ValidateSubnet(subnet string) error {
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return fmt.Errorf("invalid subnet format: %w", err)
	}

	if ipNet.IP.To4() == nil {
		return fmt.Errorf("subnet must be IPv4")
	}

	return nil
}

func parseIPv4Range(r string) (start net.IP, end net.IP, err error) {
	parts := strings.Split(r, "-")
	if len(parts) != 2 {
		return nil, nil, fmt.Errorf("expected range ip1-ip2")
	}

	start = net.ParseIP(strings.TrimSpace(parts[0])).To4()
	end = net.ParseIP(strings.TrimSpace(parts[1])).To4()
	if start == nil || end == nil {
		return nil, nil, fmt.Errorf("range must contain valid IPv4 addresses")
	}
	if bytes.Compare(start, end) > 0 {
		return nil, nil, fmt.Errorf("range start must be <= range end")
	}
	return start, end, nil
}

// ValidatePool проверяет корректность пула адресов.
// Формат Kea: "ip1-ip2". Дополнительно проверяем, что пул лежит внутри подсети.
func (s *DHCPService) ValidatePool(subnet *net.IPNet, pool string) error {
	start, end, err := parseIPv4Range(pool)
	if err != nil {
		return fmt.Errorf("invalid pool format: %w", err)
	}
	if subnet != nil {
		if !subnet.Contains(start) || !subnet.Contains(end) {
			return fmt.Errorf("pool range must be within subnet %s", subnet.String())
		}
	}
	return nil
}

// AddSubnet добавляет подсеть с валидацией через GuardedApply.
func (s *DHCPService) AddSubnet(ctx context.Context, subnet string, pools []string, reservations []kea.Reservation) (ha.ApplyResult, error) {
	if err := s.ValidateSubnet(subnet); err != nil {
		return ha.ApplyResult{}, err
	}

	_, ipNet, _ := net.ParseCIDR(subnet)

	keaPools := make([]kea.Pool, 0, len(pools))
	for _, p := range pools {
		if err := s.ValidatePool(ipNet, p); err != nil {
			return ha.ApplyResult{}, err
		}
		keaPools = append(keaPools, kea.Pool{Pool: p})
	}

	subnet4 := kea.Subnet4{
		Subnet:       subnet,
		Pools:        keaPools,
		Reservations: reservations,
	}

	return s.haManager.GuardedApply(ctx, func(ctx context.Context, client *kea.Client) error {
		return client.AddSubnet(ctx, subnet4)
	})
}

// ListSubnets возвращает список подсетей с активного узла.
func (s *DHCPService) ListSubnets(ctx context.Context) ([]kea.Subnet4, error) {
	activeClient, err := s.haManager.ActiveClient(ctx)
	if err != nil {
		return nil, err
	}
	return activeClient.ListSubnets(ctx)
}

// GetConfig получает текущую конфигурацию с активного узла.
func (s *DHCPService) GetConfig(ctx context.Context) (map[string]interface{}, error) {
	activeClient, err := s.haManager.ActiveClient(ctx)
	if err != nil {
		return nil, err
	}
	return activeClient.GetConfig(ctx)
}

// Reload перезагружает конфигурацию (config-reload) через GuardedApply.
func (s *DHCPService) Reload(ctx context.Context) (ha.ApplyResult, error) {
	return s.haManager.GuardedApply(ctx, func(ctx context.Context, client *kea.Client) error {
		return client.Reload(ctx)
	})
}

// WriteConfigAndReload выполняет config-write и config-reload через GuardedApply (reload_policy).
func (s *DHCPService) WriteConfigAndReload(ctx context.Context) (ha.ApplyResult, error) {
	return s.haManager.GuardedApply(ctx, func(ctx context.Context, client *kea.Client) error {
		return client.WriteConfigAndReload(ctx)
	})
}

// Lease4Stats возвращает статистику лизов DHCPv4 с активного узла (statistic-get-all).
func (s *DHCPService) Lease4Stats(ctx context.Context) (map[string]interface{}, error) {
	activeClient, err := s.haManager.ActiveClient(ctx)
	if err != nil {
		return nil, err
	}
	return activeClient.Lease4Stats(ctx)
}

// DeleteSubnet удаляет подсеть по ID через GuardedApply.
func (s *DHCPService) DeleteSubnet(ctx context.Context, id int) (ha.ApplyResult, error) {
	return s.haManager.GuardedApply(ctx, func(ctx context.Context, client *kea.Client) error {
		return client.DeleteSubnet(ctx, id)
	})
}
