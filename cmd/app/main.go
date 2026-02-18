package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"kea-golang-manager/internal/api"
	"kea-golang-manager/internal/ha"
	"kea-golang-manager/internal/kea"
	"kea-golang-manager/internal/lldp"
	"kea-golang-manager/internal/service"
	"kea-golang-manager/internal/snmp"
	"kea-golang-manager/pkg/config"
)

func main() {
	// Глобальные флаги (общие для всех команд)
	globalFS := flag.NewFlagSet("global", flag.ExitOnError)
	keaURL := globalFS.String("kea-url", "http://localhost:8000", "Kea Control Agent URL (primary)")
	keaStandbyURL := globalFS.String("kea-standby-url", "http://localhost:8001", "Kea Control Agent URL (standby); empty disables HA")
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

	cfg := config.New(*keaURL, *timeout)
	cfg.KeaPrimaryURL = *keaURL
	cfg.KeaStandbyURL = *keaStandbyURL
	client := kea.NewClient(cfg.KeaURL, cfg.Timeout)
	dhcpService := service.NewDHCPService(client)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	switch command {
	case "serve-http":
		var haStore *ha.StateStore
		var haClient *ha.HAClient
		var standbyClient *kea.Client
		var monitorCancel context.CancelFunc
		snmpCfg := snmp.ConfigFromEnv()
		snmpPoller := snmp.NewPoller(snmpCfg, slog.Default())
		lldpCollector := lldp.NewCollector("", 2*time.Minute, slog.Default())

		svcCtx, svcCancel := context.WithCancel(context.Background())
		go snmpPoller.Run(svcCtx)
		go lldpCollector.Run(svcCtx)

		if cfg.KeaStandbyURL != "" {
			haStore = ha.NewStateStore()
			haClient = ha.NewHAClient(cfg.KeaPrimaryURL, cfg.KeaStandbyURL, cfg.Timeout)
			standbyClient = kea.NewClient(cfg.KeaStandbyURL, cfg.Timeout)
			monitorCfg := ha.MonitorConfig{
				PollInterval: cfg.HAPollInterval,
				MinFailures:  cfg.HAMinFailures,
			}
			monitorCtx, cancelFn := context.WithCancel(context.Background())
			monitorCancel = cancelFn
			go ha.Run(monitorCtx, haStore, haClient, monitorCfg, slog.Default())
		}

		handler := api.NewHandler(api.HandlerOpts{
			DHCPService:   dhcpService,
			HAStore:       haStore,
			HAClient:      haClient,
			PrimaryClient: client,
			StandbyClient: standbyClient,
			SNMPPoller:     snmpPoller,
			LLDPCollector:  lldpCollector,
		})
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
		svcCancel()
		if monitorCancel != nil {
			monitorCancel()
		}
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
		if err := dhcpService.WriteConfigAndReload(ctx); err != nil {
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
	fmt.Println("  kea-manager serve-http [-http-addr=:8080] [-kea-standby-url=...]")
	fmt.Println("\nFlags:")
	fmt.Println("  -kea-url string         Kea Control Agent URL primary (default: http://localhost:8000)")
	fmt.Println("  -kea-standby-url string Kea Control Agent URL standby; empty disables HA (default: http://localhost:8001)")
	fmt.Println("  -timeout duration       HTTP request timeout (default: 10s)")
	fmt.Println("  -http-addr string       HTTP server listen address (default: :8080)")
}
