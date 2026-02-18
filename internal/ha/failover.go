package ha

import (
	"context"
	"log/slog"
	"time"
)

// FailoverClient — интерфейс для вызова HA-команд (реальный HAClient или mock в тестах).
type FailoverClient interface {
	FetchStatus(ctx context.Context, node string) Observation
	MaintenanceStart(ctx context.Context, node string) error
}

// TryFailover проверяет необходимость failover, выполняет quorum и при подтверждении
// вызывает ha-maintenance-start на живом узле. Не вызывает промоцию без верификации пира.
// Идемпотентно: повторный вызов при уже переключённой роли не меняет состояние.
func TryFailover(ctx context.Context, store *StateStore, client FailoverClient, minFailures int, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	status, primaryObs, standbyObs := store.GetStatusSnapshot()
	primaryFailures := store.PrimaryFailureCount()

	// Сценарий: primary недоступен, standby доступен.
	if primaryFailures < minFailures {
		return nil
	}
	if primaryObs == nil || primaryObs.Reachable {
		return nil
	}
	if standbyObs == nil || !standbyObs.Reachable {
		// Нет живого узла для вызова ha-maintenance.
		return nil
	}

	// Quorum: убедиться, что standby тоже считает primary недоступным (не просто медленный ответ).
	// Запрашиваем свежий ha-status со standby.
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	standbyFresh := client.FetchStatus(qctx, NodeStandby)
	if !standbyFresh.Reachable {
		log.Warn("failover_quorum_failed", "reason", "standby_unreachable_after_check")
		return nil
	}
	// Пир подтверждает: он в partner-down или уже принял роль primary.
	quorumOK := standbyFresh.HAState == HAStatePartnerDown ||
		standbyFresh.Role == RolePrimary
	if !quorumOK {
		log.Warn("failover_quorum_failed",
			"reason", "peer_still_sees_primary",
			"standby_ha_state", standbyFresh.HAState,
			"standby_role", standbyFresh.Role,
		)
		return nil
	}

	// Идемпотентность: если уже зафиксировали переход на standby, не дергаем ha-maintenance повторно.
	if status.CurrentRole == RoleStandby {
		return nil
	}

	reason := classifyPrimaryDown(primaryObs, standbyObs)
	if err := client.MaintenanceStart(ctx, NodeStandby); err != nil {
		log.Error("failover_maintenance_start_failed",
			"node", NodeStandby,
			"error", err,
			"reason", reason,
		)
		return err
	}
	store.RecordRoleChange(RoleStandby, reason)
	log.Info("failover_triggered",
		"reason", reason,
		"new_role", RoleStandby,
		"triggered_at", time.Now().UTC().Format(time.RFC3339),
	)
	return nil
}

// classifyPrimaryDown выбирает причину сбоя по наблюдениям (для логов и API).
func classifyPrimaryDown(primaryObs, standbyObs *Observation) string {
	if primaryObs != nil && primaryObs.Err != nil {
		// Недоступность Control Agent или сеть.
		return FailoverReasonControlAgentCrash
	}
	if standbyObs != nil && standbyObs.HAState == HAStatePartnerDown {
		return FailoverReasonHALinkFailure
	}
	return FailoverReasonControlAgentCrash
}
