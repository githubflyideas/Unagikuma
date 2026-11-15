#!/bin/sh
# Starts real Prometheus (19090), etcd (12379), Caddy (admin 12019) and
# RabbitMQ with its Prometheus plugin (15692), then Grafana, Traefik and
# MinIO, for TestLiveMetricsApps.
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

# Grafana, Traefik and MinIO are not in apt; fetch the official builds.
# Grafana 11.3.0 (13000), Traefik 3.2.0 (metrics 18087, backend Caddy 18085),
# MinIO from source via the Go module proxy (19100).
mkdir -p dl && cd dl
[ -d grafana-v11.3.0 ] || { curl -sfL -o g.tgz https://dl.grafana.com/oss/release/grafana-11.3.0.linux-amd64.tar.gz && tar xzf g.tgz && rm g.tgz; }
[ -x traefik ] || { curl -sfL -o t.tgz https://github.com/traefik/traefik/releases/download/v3.2.0/traefik_v3.2.0_linux_amd64.tar.gz && tar xzf t.tgz traefik && rm t.tgz; }
[ -x minio ] || GOBIN=/tmp/live/dl go install github.com/minio/minio@latest
cat > traefik.yml <<YML
entryPoints:
  web: {address: "127.0.0.1:18086"}
  metrics: {address: "127.0.0.1:18087"}
metrics:
  prometheus: {entryPoint: metrics}
providers:
  file: {filename: /tmp/live/dl/dyn.yml}
YML
cat > dyn.yml <<'YML'
http:
  routers: {r1: {rule: "PathPrefix(`/`)", service: s1, entryPoints: [web]}}
  services: {s1: {loadBalancer: {servers: [{url: "http://127.0.0.1:18085"}], healthCheck: {path: "/", interval: "2s"}}}}
YML
mkdir -p /tmp/live/gf /tmp/live/minio
(cd grafana-v11.3.0 && nohup ./bin/grafana server --homepath . cfg:default.paths.data=/tmp/live/gf cfg:default.paths.logs=/tmp/live/gf cfg:default.server.http_addr=127.0.0.1 cfg:default.server.http_port=13000 >/tmp/live/grafana.log 2>&1 &)
(nohup ./traefik --configFile=traefik.yml >/tmp/live/traefik.log 2>&1 &)
(MINIO_PROMETHEUS_AUTH_TYPE=public MINIO_ROOT_USER=minioadmin MINIO_ROOT_PASSWORD=minioadmin123 nohup ./minio server /tmp/live/minio --address 127.0.0.1:19100 --console-address 127.0.0.1:19101 >/tmp/live/minio.log 2>&1 &)
cd ..
sleep 25
curl -s -o /dev/null http://127.0.0.1:18085/
echo started
