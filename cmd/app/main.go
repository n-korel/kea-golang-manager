package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"kea-golang-manager/internal/kea"
	"kea-golang-manager/internal/service"
	"kea-golang-manager/pkg/config"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cfg := config.Load()
	client := kea.NewClient(cfg.KeaURL, cfg.Timeout)
	dhcpService := service.NewDHCPService(client)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	command := os.Args[1]

	switch command {
	case "add-subnet":
		if err := handleAddSubnet(ctx, dhcpService, os.Args[2:]); err != nil {
			log.Fatalf("Error: %v", err)
		}
	case "reload":
		if err := dhcpService.Reload(ctx); err != nil {
			log.Fatalf("Error reloading config: %v", err)
		}
		fmt.Println("Configuration reloaded successfully")
	case "show-config":
		if err := handleShowConfig(ctx, dhcpService); err != nil {
			log.Fatalf("Error: %v", err)
		}
	default:
		fmt.Printf("Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

func handleAddSubnet(ctx context.Context, svc *service.DHCPService, args []string) error {
	fs := flag.NewFlagSet("add-subnet", flag.ExitOnError)
	subnet := fs.String("subnet", "", "Subnet in CIDR format (e.g., 192.168.1.0/24)")
	pools := fs.String("pools", "", "Comma-separated pools (e.g., 192.168.1.10-192.168.1.100)")
	hwAddr := fs.String("hw-address", "", "Hardware address for reservation (e.g., aa:bb:cc:dd:ee:ff)")
	ipAddr := fs.String("ip-address", "", "IP address for reservation")
	hostname := fs.String("hostname", "", "Hostname for reservation")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *subnet == "" {
		return fmt.Errorf("subnet is required")
	}

	var poolList []string
	if *pools != "" {
		poolList = strings.Split(*pools, ",")
		for i := range poolList {
			poolList[i] = strings.TrimSpace(poolList[i])
		}
	}

	var reservations []kea.Reservation
	if *hwAddr != "" && *ipAddr != "" {
		reservations = append(reservations, kea.Reservation{
			HWAddress: *hwAddr,
			IPAddress: *ipAddr,
			Hostname:  *hostname,
		})
	}

	if err := svc.AddSubnet(ctx, *subnet, poolList, reservations); err != nil {
		return fmt.Errorf("failed to add subnet: %w", err)
	}

	fmt.Printf("Subnet %s added successfully\n", *subnet)
	return nil
}

func handleShowConfig(ctx context.Context, svc *service.DHCPService) error {
	cfg, err := svc.GetConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	jsonData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	fmt.Println(string(jsonData))
	return nil
}

func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  kea-manager add-subnet -subnet=<CIDR> [-pools=<pool1,pool2>] [-hw-address=<mac>] [-ip-address=<ip>] [-hostname=<name>]")
	fmt.Println("  kea-manager reload")
	fmt.Println("  kea-manager show-config")
	fmt.Println("\nFlags:")
	fmt.Println("  -kea-url string    Kea Control Agent URL (default: http://localhost:8000)")
	fmt.Println("  -timeout duration  HTTP request timeout (default: 10s)")
}
