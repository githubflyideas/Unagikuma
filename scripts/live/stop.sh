#!/bin/sh
pkill nginx haproxy lighttpd apache2 php-fpm8.3 2>/dev/null || true
