#!/bin/sh
# Starts real Nginx, Apache, Lighttpd, HAProxy and PHP-FPM on 127.0.0.1 for
# the opt-in live checks (go test -tags live ./internal/probe). Needs the
# packages installed (apt-get install nginx apache2 lighttpd haproxy php-fpm)
# and root; the configs write under /tmp/live.
set -e
mkdir -p /tmp/live/arun
for f in nginx.conf apache.conf lighttpd.conf haproxy.cfg fpm.conf; do cp "$(dirname "$0")/$f" /tmp/live/$f; done
php-fpm8.3 -y /tmp/live/fpm.conf
nginx -c /tmp/live/nginx.conf
apache2 -f /tmp/live/apache.conf
lighttpd -f /tmp/live/lighttpd.conf
haproxy -f /tmp/live/haproxy.cfg -D
sleep 2
echo started
