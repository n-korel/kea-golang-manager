package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"kea-golang-manager/internal/api"
	"kea-golang-manager/internal/ha"
	"kea-golang-manager/internal/kea"
	"kea-golang-manager/internal/service"
	"kea-golang-manager/pkg/config"
)

func main() {
	globalFS := flag.NewFlagSet("global", flag.ExitOnError)
	keaPrimaryURL := globalFS.String("kea-primary-url", "http://localhost:8001", "Kea Control Agent URL (primary)")
	keaStandbyURL := globalFS.String("kea-standby-url", "http://localhost:8002", "Kea Control Agent URL (standby)")
	timeout := globalFS.Duration("timeout", 10*time.Second, "HTTP request timeout")
	httpAddr := globalFS.String("http-addr", ":8080", "HTTP server listen address")

	if err := globalFS.Parse(os.Args[1:]); err != nil {
		log.Fatalf("failed to parse global flags: %v", err)
	}

	args := globalFS.Args()
	if len(args) < 1 {
		printUsage()
		os.Exit(1)
	}

	command := args[0]
	cfg := config.New(*keaPrimaryURL, *keaStandbyURL, *timeout)
	primaryClient := kea.NewClient(cfg.PrimaryURL, cfg.Timeout)
	standbyClient := kea.NewClient(cfg.StandbyURL, cfg.Timeout)
	haManager := ha.NewHAManager(primaryClient, standbyClient)
	dhcpService := service.NewDHCPService(haManager)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	switch command {
	case "serve-http":
		handler := api.NewHandler(api.HandlerOpts{DHCPService: dhcpService, HAManager: haManager})
		server := &http.Server{
			Addr:         *httpAddr,
			Handler:      handler,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  60 * time.Second,
		}

		quit := make(chan os.Signal, 1)
		signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

		go func() {
			log.Printf("Starting HTTP server on %s", *httpAddr)
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("HTTP server failed: %v", err)
			}
		}()

		<-quit
		log.Println("Shutting down HTTP server...")

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server shutdown: %v", err)
		} else {
			log.Println("HTTP server stopped gracefully")
		}
	case "add-subnet":
		if err := handleAddSubnet(ctx, dhcpService, args[1:]); err != nil {
			log.Fatalf("Error: %v", err)
		}
	case "reload":
		result, err := dhcpService.WriteConfigAndReload(ctx)
		if err != nil {
			log.Fatalf("Error reloading config: %v", err)
		}
		if result.Warning != "" {
			log.Printf("Warning: %s (ha_state=%s)", result.Warning, result.HAState)
		}
		fmt.Println("Configuration reloaded successfully")
	case "show-config":
		if err := handleShowConfig(ctx, dhcpService); err != nil {
			log.Fatalf("Error: %v", err)
		}
	case "ha-status":
		status, err := haManager.Status(ctx)
		if err != nil {
			log.Fatalf("Error getting HA status: %v", err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(status); err != nil {
			log.Fatalf("Error encoding HA status: %v", err)
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

	result, err := svc.AddSubnet(ctx, *subnet, poolList, reservations)
	if err != nil {
		return fmt.Errorf("failed to add subnet: %w", err)
	}
	if result.Warning != "" {
		fmt.Printf("Warning: %s (ha_state=%s)\n", result.Warning, result.HAState)
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
	fmt.Println("  kea-manager ha-status")
	fmt.Println("  kea-manager serve-http [-http-addr=:8080]")
	fmt.Println("\nFlags:")
	fmt.Println("  -kea-primary-url string  Kea Control Agent URL primary (default: http://localhost:8001)")
	fmt.Println("  -kea-standby-url string  Kea Control Agent URL standby (default: http://localhost:8002)")
	fmt.Println("  -timeout duration        HTTP request timeout (default: 10s)")
	fmt.Println("  -http-addr string        HTTP server listen address (default: :8080)")
}
