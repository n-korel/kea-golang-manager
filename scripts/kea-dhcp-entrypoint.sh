#!/bin/sh
# Подставляет IP ctrl-agent в конфиг (хук HA в образе jonasal принимает только IP).
# Контейнеры DHCP используют network_mode: service:ctrl-agent, поэтому у них тот же IP,
# что и у ctrl-agent — bind HA listener к этому IP корректен.
# Использование: kea-dhcp-entrypoint.sh <config-file> (например kea-primary.conf)
set -e
CONFIG_NAME="${1:-kea-primary.conf}"
CONFIG_SRC="/kea/config/$CONFIG_NAME"
CONFIG_DST="/tmp/kea.conf"

echo "Waiting for ctrl-agent hostnames..."
until getent hosts kea-primary-ctrl-agent >/dev/null 2>&1; do sleep 1; done
until getent hosts kea-standby-ctrl-agent >/dev/null 2>&1; do sleep 1; done

PRIMARY_IP=$(getent hosts kea-primary-ctrl-agent | awk '{print $1}')
STANDBY_IP=$(getent hosts kea-standby-ctrl-agent | awk '{print $1}')
# В network_mode: service:ctrl-agent «свой» хост может резолвиться в 127.0.0.1; хук биндится к этому адресу
[ -z "$PRIMARY_IP" ] && PRIMARY_IP=0.0.0.0
[ -z "$STANDBY_IP" ] && STANDBY_IP=0.0.0.0
echo "Resolved: primary=$PRIMARY_IP standby=$STANDBY_IP"

cp "$CONFIG_SRC" "$CONFIG_DST"
# HA hook dedicated listener (ports 8003/8004) — не конфликтуют с Control Agent (8001/8002)
sed -i "s|http://kea-primary-ctrl-agent:8003|http://${PRIMARY_IP}:8003|g" "$CONFIG_DST"
sed -i "s|http://kea-standby-ctrl-agent:8004|http://${STANDBY_IP}:8004|g" "$CONFIG_DST"

exec /entrypoint.sh -c "$CONFIG_DST"
