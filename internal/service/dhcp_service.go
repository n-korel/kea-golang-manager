package service

import (
	"context"
	"bytes"
	"fmt"
	"kea-golang-manager/internal/kea"
	"net"
	"strings"
)

// DHCPService предоставляет бизнес-логику для работы с DHCP
type DHCPService struct {
	client *kea.Client
}

// NewDHCPService создает новый сервис DHCP
func NewDHCPService(client *kea.Client) *DHCPService {
	return &DHCPService{
		client: client,
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

// AddSubnet добавляет подсеть с валидацией
func (s *DHCPService) AddSubnet(ctx context.Context, subnet string, pools []string, reservations []kea.Reservation) error {
	if err := s.ValidateSubnet(subnet); err != nil {
		return err
	}

	_, ipNet, _ := net.ParseCIDR(subnet)

	keaPools := make([]kea.Pool, 0, len(pools))
	for _, p := range pools {
		if err := s.ValidatePool(ipNet, p); err != nil {
			return err
		}
		keaPools = append(keaPools, kea.Pool{Pool: p})
	}

	subnet4 := kea.Subnet4{
		Subnet:       subnet,
		Pools:        keaPools,
		Reservations: reservations,
	}

	return s.client.AddSubnet(ctx, subnet4)
}

// GetConfig получает текущую конфигурацию
func (s *DHCPService) GetConfig(ctx context.Context) (map[string]interface{}, error) {
	return s.client.GetConfig(ctx)
}

// Reload перезагружает конфигурацию
func (s *DHCPService) Reload(ctx context.Context) error {
	return s.client.Reload(ctx)
}
