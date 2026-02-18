package snmp

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"
)

// Snapshot — снимок данных SNMP для мониторинга.
// snmp_rules: результат только мониторинг, НЕ источник решений о failover.
type Snapshot struct {
	CollectedAt time.Time              `json:"collected_at"`
	Interfaces  []InterfaceStats        `json:"interfaces,omitempty"`
	Raw         map[string]interface{} `json:"raw,omitempty"`
	Error       string                 `json:"error,omitempty"`
}

// InterfaceStats — упрощённая статистика по интерфейсу (ifDescr, ifInOctets, ifOutOctets и т.д.).
type InterfaceStats struct {
	Index     uint   `json:"index"`
	Descr     string `json:"descr,omitempty"`
	InOctets  uint64 `json:"in_octets,omitempty"`
	OutOctets uint64 `json:"out_octets,omitempty"`
}

// Poller — SNMP-поллер: горутина с таймаутом на запрос, credentials из env.
type Poller struct {
	cfg    Config
	mu     sync.RWMutex
	snap   Snapshot
	log    *slog.Logger
}

// NewPoller создаёт поллер по конфигу (обычно из ConfigFromEnv).
func NewPoller(cfg Config, log *slog.Logger) *Poller {
	if log == nil {
		log = slog.Default()
	}
	return &Poller{cfg: cfg, log: log}
}

// Run запускает горутину опроса. Таймаут на каждый запрос обязателен, не блокирует основной loop.
// Завершается при отмене ctx (graceful shutdown).
func (p *Poller) Run(ctx context.Context) {
	if p.cfg.Target == "" {
		p.log.Debug("snmp_poller_skipped", "reason", "SNMP_TARGET empty")
		return
	}
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.log.Info("snmp_poller_stopped", "reason", ctx.Err())
			return
		case <-ticker.C:
			p.pollOnce(ctx)
		}
	}
}

func (p *Poller) pollOnce(ctx context.Context) {
	// Отдельный контекст с таймаутом, чтобы не блокировать loop
	reqCtx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	snap := p.doPoll(reqCtx)
	p.mu.Lock()
	p.snap = snap
	p.mu.Unlock()
}

func (p *Poller) doPoll(ctx context.Context) Snapshot {
	snap := Snapshot{CollectedAt: time.Now().UTC()}
	conn, err := p.buildConn()
	if err != nil {
		snap.Error = err.Error()
		p.log.Warn("snmp_connect_failed", "error", err)
		return snap
	}
	if err := conn.Connect(); err != nil {
		snap.Error = err.Error()
		p.log.Warn("snmp_connect_failed", "error", err)
		return snap
	}
	defer conn.Conn.Close()

	// Проверка отмены перед запросом
	if ctx.Err() != nil {
		return snap
	}

	// Скаляры MIB-2 — только мониторинг (не источник решений о failover)
	oids := []string{
		"1.3.6.1.2.1.1.1.0", // sysDescr
		"1.3.6.1.2.1.1.3.0", // sysUpTime
	}
	result, err := conn.Get(oids)
	if err != nil {
		snap.Error = err.Error()
		p.log.Debug("snmp_get_failed", "error", err)
		return snap
	}
	if result == nil {
		return snap
	}
	raw := make(map[string]interface{})
	for _, v := range result.Variables {
		raw[v.Name] = valueToInterface(v.Value)
	}
	snap.Raw = raw
	return snap
}

func (p *Poller) buildConn() (*gosnmp.GoSNMP, error) {
	params := &gosnmp.GoSNMP{
		Target:    p.cfg.Target,
		Port:      p.cfg.Port,
		Timeout:   p.cfg.Timeout,
		Retries:   0,
	}
	if p.cfg.Version == "3" && p.cfg.V3User != "" {
		params.Version = gosnmp.Version3
		params.SecurityModel = gosnmp.UserSecurityModel
		params.MsgFlags = gosnmp.AuthNoPriv
		if p.cfg.V3PrivPass != "" {
			params.MsgFlags = gosnmp.AuthPriv
		}
		params.SecurityParameters = &gosnmp.UsmSecurityParameters{
			UserName:                 p.cfg.V3User,
			AuthenticationProtocol:   gosnmp.MD5,
			AuthenticationPassphrase: p.cfg.V3AuthPass,
			PrivacyProtocol:          gosnmp.DES,
			PrivacyPassphrase:        p.cfg.V3PrivPass,
		}
		return params, nil
	}
	// Fallback SNMPv2c
	params.Version = gosnmp.Version2c
	community := p.cfg.Community
	if community == "" {
		community = "public"
	}
	params.Community = community
	return params, nil
}

func valueToInterface(v interface{}) interface{} {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case int, int32, int64, uint, uint32, uint64:
		return x
	default:
		return v
	}
}

// Snapshot возвращает последний снимок (без запроса к SNMP).
func (p *Poller) Snapshot() Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.snap
}
