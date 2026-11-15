#!/bin/sh
# Starts a real Redis (16379), PostgreSQL 16 (15432) and MariaDB (13306)
# with a read-only account "monitor" (password monpass1234) and a table
# "orders" (42 rows in PostgreSQL, 3 in MariaDB), for TestLiveRedis and
# TestLiveSQL. Needs: apt-get install redis-server postgresql mariadb-server.
set -e
chmod 1777 /tmp/live 2>/dev/null || { mkdir -p /tmp/live; chmod 1777 /tmp/live; }
cd /tmp/live
PGBIN=$(ls -d /usr/lib/postgresql/*/bin | head -1)
redis-server --port 16379 --bind 127.0.0.1 --daemonize yes --save "" --dir /tmp/live >/dev/null
rm -rf pg mdb; mkdir pg mdb; chown postgres:postgres pg; chown mysql:mysql mdb
echo supersecret > pgpw; chmod 644 pgpw
runuser -u postgres -- "$PGBIN/initdb" -D /tmp/live/pg -A scram-sha-256 --pwfile=/tmp/live/pgpw -U postgres >/dev/null
runuser -u postgres -- "$PGBIN/pg_ctl" -D /tmp/live/pg -o "-p 15432 -c listen_addresses=127.0.0.1 -c unix_socket_directories=/tmp/live" -l /tmp/live/pg.log -w start >/dev/null
mariadb-install-db --user=mysql --datadir=/tmp/live/mdb --auth-root-authentication-method=normal >/dev/null 2>&1
(mariadbd --user=mysql --datadir=/tmp/live/mdb --port=13306 --bind-address=127.0.0.1 --socket=/tmp/live/mdb.sock --pid-file=/tmp/live/mdb.pid >/tmp/live/mdb.log 2>&1 &)
sleep 6
PGPASSWORD=supersecret "$PGBIN/psql" -h 127.0.0.1 -p 15432 -U postgres -v ON_ERROR_STOP=1 -q <<'SQL'
CREATE TABLE orders(id serial primary key, amount numeric);
INSERT INTO orders(amount) SELECT g FROM generate_series(1,42) g;
CREATE ROLE monitor LOGIN PASSWORD 'monpass1234';
GRANT SELECT ON orders TO monitor;
SQL
mariadb -S /tmp/live/mdb.sock -e "CREATE DATABASE app; CREATE TABLE app.orders(id int primary key auto_increment, amount decimal(10,2)); INSERT INTO app.orders(amount) VALUES (1),(2),(3.5); CREATE USER 'monitor'@'127.0.0.1' IDENTIFIED BY 'monpass1234'; GRANT SELECT ON app.* TO 'monitor'@'127.0.0.1'; FLUSH PRIVILEGES;"
echo started
