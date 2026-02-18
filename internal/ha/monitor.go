package ha

import (
	"context"
	"log/slog"
	"time"
)

// MonitorConfig — параметры монитора (интервал опроса, порог неудач).
type MonitorConfig struct {
	PollInterval   time.Duration
	MinFailures    int
}

// DefaultMonitorConfig возвращает конфиг по умолчанию (min_failures = 3 из rules).
func DefaultMonitorConfig() MonitorConfig {
	return MonitorConfig{
		PollInterval: 5 * time.Second,
		MinFailures:  3,
	}
}

// Run запускает горутину мониторинга обоих узлов через ha-status.
// Детектирует: control_agent_crash (недоступность узла), ha_link_failure (по ha_state из ответа).
// Перед объявлением сбоя требуется минимум MinFailures последовательных неудач.
// Завершается при отмене ctx (graceful shutdown).
func Run(ctx context.Context, store *StateStore, client *HAClient, cfg MonitorConfig, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("ha_monitor_stopped", "reason", ctx.Err())
			return
		case <-ticker.C:
			runOnePoll(ctx, store, client, cfg.MinFailures, log)
		}
	}
}

func runOnePoll(ctx context.Context, store *StateStore, client *HAClient, minFailures int, log *slog.Logger) {
	// Опрос обоих узлов (параллельно по контексту одного тика).
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	primaryObs := client.FetchStatus(pctx, NodePrimary)
	standbyObs := client.FetchStatus(pctx, NodeStandby)

	store.RecordObservation(primaryObs)
	store.RecordObservation(standbyObs)

	// Логируем только при переходе в «сбой» (достигнут порог) или при смене состояния.
	primaryFailures := store.PrimaryFailureCount()
	standbyFailures := store.StandbyFailureCount()

	if primaryFailures >= minFailures && primaryFailures > 0 {
		log.Warn("ha_primary_unreachable",
			"consecutive_failures", primaryFailures,
			"last_error", errString(primaryObs.Err),
		)
	}
	if standbyFailures >= minFailures && standbyFailures > 0 {
		log.Warn("ha_standby_unreachable",
			"consecutive_failures", standbyFailures,
			"last_error", errString(standbyObs.Err),
		)
	}

	// Явная детекция ha_link_failure: оба узла доступны, но ha_state = partner-down или communication-recovery.
	if primaryObs.Reachable && standbyObs.Reachable {
		if primaryObs.HAState == HAStatePartnerDown || standbyObs.HAState == HAStatePartnerDown {
			log.Warn("ha_link_state", "ha_state", HAStatePartnerDown, "source", "monitor")
		}
		if primaryObs.HAState == HAStateCommunicationRecovery || standbyObs.HAState == HAStateCommunicationRecovery {
			log.Info("ha_link_state", "ha_state", HAStateCommunicationRecovery, "source", "monitor")
		}
	}

	// После наблюдений — попытка failover с quorum (идемпотентно).
	_ = TryFailover(ctx, store, client, minFailures, log)
}

func errString(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}
