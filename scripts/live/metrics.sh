#!/bin/sh
# Starts real Prometheus (19090), etcd (12379), Caddy (admin 12019) and
# RabbitMQ with its Prometheus plugin (15692) for TestLiveMetricsApps.
# Needs: apt-get install prometheus etcd-server caddy rabbitmq-server.
set -e
chmod 1777 /tmp/live 2>/dev/null || { mkdir -p /tmp/live; chmod 1777 /tmp/live; }
cd /tmp/live
mkdir -p prom etcd rmq rmqlog
cat > prom.yml <<'YML'
global: {scrape_interval: 2s}
scrape_configs:
  - job_name: self
    static_configs: [{targets: ['127.0.0.1:19090']}]
YML
cat > Caddyfile <<'CADDY'
{
	admin 127.0.0.1:12019
	servers {
		metrics
	}
}
:18085 {
	respond "ok"
}
CADDY
(prometheus --config.file=/tmp/live/prom.yml --web.listen-address=127.0.0.1:19090 --storage.tsdb.path=/tmp/live/prom >/tmp/live/prom.log 2>&1 &)
(etcd --name default --data-dir /tmp/live/etcd --listen-client-urls http://127.0.0.1:12379 --advertise-client-urls http://127.0.0.1:12379 --listen-peer-urls http://127.0.0.1:12380 --initial-advertise-peer-urls http://127.0.0.1:12380 --initial-cluster default=http://127.0.0.1:12380 >/tmp/live/etcd.log 2>&1 &)
(caddy run --config /tmp/live/Caddyfile --adapter caddyfile >/tmp/live/caddy.log 2>&1 &)
chown -R rabbitmq:rabbitmq rmq rmqlog
printf 'listeners.tcp.default = 127.0.0.1:15672\nprometheus.tcp.port = 15692\nprometheus.tcp.ip = 127.0.0.1\n' > rmq.conf; chmod 644 rmq.conf
echo "[rabbitmq_prometheus]." > rmq-plugins; chmod 644 rmq-plugins
PLUGINS=$(ls -d /usr/lib/rabbitmq/lib/*/plugins | head -1)
(runuser -u rabbitmq -- env RABBITMQ_NODENAME=live@localhost RABBITMQ_MNESIA_BASE=/tmp/live/rmq RABBITMQ_LOG_BASE=/tmp/live/rmqlog RABBITMQ_CONFIG_FILE=/tmp/live/rmq.conf RABBITMQ_ENABLED_PLUGINS_FILE=/tmp/live/rmq-plugins RABBITMQ_PLUGINS_DIR="$PLUGINS" HOME=/tmp/live/rmq rabbitmq-server >/tmp/live/rmq.log 2>&1 &)
sleep 25
curl -s -o /dev/null http://127.0.0.1:18085/
echo started
