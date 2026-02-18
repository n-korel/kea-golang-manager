package lldp

import (
	"bufio"
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// TopologyEntry — маппинг MAC → port → VLAN (lldp_rules: topology enrichment).
type TopologyEntry struct {
	MAC  string `json:"mac"`
	Port string `json:"port"`
	VLAN string `json:"vlan,omitempty"`
}

// Snapshot — снимок LLDP-топологии; при недоступности LLDP возвращаем последние известные (stale-tolerant).
type Snapshot struct {
	CollectedAt time.Time       `json:"collected_at"`
	Entries     []TopologyEntry `json:"entries"`
	Stale       bool            `json:"stale,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// Collector — собирает LLDP (lldpctl или /sys), строит MAC→port→VLAN, не блокирует DHCP.
type Collector struct {
	mu       sync.RWMutex
	snap     Snapshot
	lldpctl  string
	interval time.Duration
	log      *slog.Logger
}

// NewCollector создаёт коллектор. lldpctlPath — путь к lldpctl (например "lldpctl"), interval — интервал обновления.
func NewCollector(lldpctlPath string, interval time.Duration, log *slog.Logger) *Collector {
	if log == nil {
		log = slog.Default()
	}
	if lldpctlPath == "" {
		lldpctlPath = "lldpctl"
	}
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	return &Collector{lldpctl: lldpctlPath, interval: interval, log: log}
}

// Run запускает горутину сбора. Tolerate stale: при ошибке оставляем последний снимок.
func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.log.Info("lldp_collector_stopped", "reason", ctx.Err())
			return
		case <-ticker.C:
			c.collectOnce(ctx)
		}
	}
}

func (c *Collector) collectOnce(ctx context.Context) {
	snap := c.fetch(ctx)
	c.mu.Lock()
	if snap.Error != "" {
		// Stale-tolerant: помечаем снимок как устаревший, но не затираем данные
		prev := c.snap
		if len(prev.Entries) > 0 {
			prev.Stale = true
			prev.Error = snap.Error
			prev.CollectedAt = time.Now().UTC()
			c.snap = prev
		} else {
			c.snap = snap
		}
	} else {
		c.snap = snap
	}
	c.mu.Unlock()
}

// fetch вызывает lldpctl -f keyvalue и парсит MAC, port, VLAN.
func (c *Collector) fetch(ctx context.Context) Snapshot {
	snap := Snapshot{CollectedAt: time.Now().UTC()}
	cmd := exec.CommandContext(ctx, c.lldpctl, "-f", "keyvalue")
	out, err := cmd.Output()
	if err != nil {
		snap.Error = err.Error()
		c.log.Debug("lldpctl_failed", "error", err)
		return snap
	}
	entries := parseKeyValue(out)
	snap.Entries = entries
	return snap
}

// parseKeyValue парсит вывод lldpctl -f keyvalue: lldp.eth0.chassis.mac=..., lldp.eth0.port.id=..., vlan и т.д.
func parseKeyValue(data []byte) []TopologyEntry {
	type iface struct {
		chassisMAC string
		portID     string
		vlanID     string
	}
	byIface := make(map[string]*iface)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		key, val := strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:])
		key = strings.TrimPrefix(key, "lldp.")
		parts := strings.SplitN(key, ".", 2)
		if len(parts) != 2 {
			continue
		}
		ifName, rest := parts[0], parts[1]
		if byIface[ifName] == nil {
			byIface[ifName] = &iface{}
		}
		switch rest {
		case "chassis.mac":
			byIface[ifName].chassisMAC = val
		case "port.id", "port.ifname":
			if byIface[ifName].portID == "" {
				byIface[ifName].portID = val
			}
		case "vlan.vlan-id", "vlan.pvid":
			if byIface[ifName].vlanID == "" {
				byIface[ifName].vlanID = val
			}
		}
	}
	var entries []TopologyEntry
	for _, i := range byIface {
		if i.chassisMAC != "" {
			entries = append(entries, TopologyEntry{
				MAC:  i.chassisMAC,
				Port: i.portID,
				VLAN: i.vlanID,
			})
		}
	}
	return entries
}

// Snapshot возвращает последний снимок (без вызова lldpctl).
func (c *Collector) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snap
}
