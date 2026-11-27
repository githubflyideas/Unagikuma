"""Shapes a recording for the demo: python3 patch.py data.json

A cluster on one machine sees every target the same way from every site,
so the recording is given what the demo is there to show: a target down
for one site only (its way to it), and one down for some sites but up for
others. Also names the version "demo"."""
import json, sys

# monitor -> {site: (status, error, latency_ms)}
SITE_VIEW = {
    'vpn-gateway': {'b': ('up', 'ok', 14.2), 'c': ('up', 'ok', 31.7)},
    'edge-router-gz': {'c': ('down', 'timeout', 0)},
    'cdn-origin': {'b': ('down', 'timeout', 0)},
}

def walk(v):
    if isinstance(v, dict):
        if v.get('monitor_id') in SITE_VIEW and v.get('node_id') in SITE_VIEW[v['monitor_id']]:
            st, err, lat = SITE_VIEW[v['monitor_id']][v['node_id']]
            v.update(status=st, error=err, latency_ms=lat)
        for x in v.values():
            walk(x)
    elif isinstance(v, list):
        for x in v:
            walk(x)

# Site names: the recording's node names become the cities' short names.
NODES = {'b': 'muc', 'c': 'tyo', 'unagikuma': 'lax'}
NODE_KEYS = ('node_id', 'node', 'name', 'site', 'self', 'master', 'from', 'by')

def rename(v, key=None):
    if isinstance(v, dict):
        return {k: rename(x, k) for k, x in v.items()}
    if isinstance(v, list):
        return [rename(x, key) for x in v]
    if isinstance(v, str) and v in NODES and (key in NODE_KEYS or key in ('nodes', 'sites', 'probers', 'online')):
        return NODES[v]
    return v

# A day of history: a recording made minutes after the start has none, so
# the heartbeats, uptime and latency charts are filled in, steadily for a
# working monitor and with its failure at the end for a failing one.
import random, datetime
rnd = random.Random(7)
BASE_MS = {'official-site': 38, 'shop-frontend': 22, 'order-api': 15, 'payment-gw': 61, 'cdn-origin': 44, 'mail-relay': 27}

def fill(resp):
    for k, v in list(resp.items()):
        if k.startswith('/monitors/heartbeats'):
            for name, cells in v['items'].items():
                failing = bool(cells) and cells[-1]['s'] == 'critical'
                for i, c in enumerate(cells):
                    if c['s'] == '':
                        c['n'] = c['n'] or 80
                        c['s'], c['fail'] = 'ok', 0
                        if rnd.random() < 0.03:
                            c['s'], c['fail'] = 'degraded', 2
                    if failing and i >= len(cells) - 2:
                        c['s'], c['fail'] = 'critical', c['n']
        if k.endswith('/latency?range=24h') and v.get('points'):
            name = k.split('/')[2]
            base = BASE_MS.get(name, rnd.uniform(0.4, 6))
            end = datetime.datetime.fromisoformat(v['points'][-1]['t'].replace('Z', '+00:00'))
            pts = []
            for i in range(144):
                t = end - datetime.timedelta(minutes=10 * (143 - i))
                avg = base * (1 + 0.25 * rnd.random() + (0.8 if 18 <= (t.hour + 8) % 24 <= 22 else 0) * rnd.random())
                pts.append({'t': t.isoformat().replace('+00:00', 'Z'), 'avg': round(avg, 2), 'max': round(avg * 1.6, 2)})
            v['points'] = pts

def uptime(v, failing):
    up = {'24h': 0.9912 if failing else round(rnd.uniform(0.9995, 1), 5), '7d': 0.9987 if failing else round(rnd.uniform(0.9993, 1), 5),
          '30d': 0.9996 if failing else round(rnd.uniform(0.9996, 1), 5), '90d': 0.9998 if failing else round(rnd.uniform(0.9997, 1), 5)}
    return up

def fill_uptime(v):
    if isinstance(v, dict):
        if v.get('name') in TARGETS and 'latency_ms' in v and v.get('status') == 'ok':
            v['latency_ms'] = round(BASE_MS.get(v['name'], 2 + 10 * rnd.random()) * (0.9 + 0.2 * rnd.random()), 1)
        if 'uptime_24h' in v and 'status' in v:
            failing = v['status'] == 'critical'
            v['uptime_24h'], v['coverage_24h'] = uptime(v, failing)['24h'], 1
        if 'uptime' in v and 'coverage' in v and isinstance(v['uptime'], dict):
            failing = v.get('row', {}).get('status') == 'critical'
            v['uptime'] = uptime(v, failing)
            v['coverage'] = {k: 1 for k in v['coverage']}
            v['uptime_partial'] = {}
        for x in v.values():
            fill_uptime(x)
    elif isinstance(v, list):
        for x in v:
            fill_uptime(x)

# Real-looking targets instead of the recording's local ports.
TARGETS = {
    'official-site': 'https://www.example.com/', 'shop-frontend': 'shop.example.com:443', 'order-api': '10.20.1.21:8080',
    'payment-gw': 'pay.example.com:443', 'user-center': '10.20.1.23:8080', 'search-svc': '10.20.1.31:9200',
    'mysql-primary': '10.20.2.11:3306', 'mysql-replica': '10.20.2.12:3306', 'redis-cache': '10.20.2.21:6379',
    'kafka-broker': '10.20.2.31:9092', 'core-sw-bj': '10.1.0.1:22', 'core-sw-sh': '10.2.0.1:22', 'edge-router-gz': '10.3.0.1:22',
    'vpn-gateway': 'vpn.example.com:443', 'dns-primary': '10.20.0.53:53', 'mail-relay': 'smtp.example.com:25',
    'cdn-origin': 'origin.example.com:443', 'backup-nas': '10.20.5.8:445', 'ntp-server': '10.20.0.123:123', 'monitoring-wall': '10.20.9.9:80',
}
import re
LOCAL = re.compile(r'(https?://)?127\.0\.0\.1:\d+/?')

def retarget(v, name):
    if isinstance(v, dict):
        n = v.get('name') if v.get('name') in TARGETS else (v.get('monitor') if v.get('monitor') in TARGETS else name)
        return {k: retarget(x, n) for k, x in v.items()}
    if isinstance(v, list):
        return [retarget(x, name) for x in v]
    if isinstance(v, str) and '\n' in v and LOCAL.search(v):
        # a configuration text: each record names its monitor first
        return '\n'.join(LOCAL.sub(TARGETS[l.split()[0]], l) if l.split() and l.split()[0] in TARGETS else l for l in v.split('\n'))
    if isinstance(v, str) and name in TARGETS and LOCAL.search(v):
        return LOCAL.sub(TARGETS[name], v)
    return v

path = sys.argv[1]
d = json.load(open(path))
walk(d['responses'])
d['responses'] = rename(d['responses'])
d['responses'] = {k: retarget(v, k.split('/')[2].split('?')[0] if k.startswith('/monitors/') else None) for k, v in d['responses'].items()}
fill(d['responses'])
fill_uptime(d['responses'])
d['responses']['/session']['version'] = 'demo'
# The demo shows the registered version, set up: no trial notices, no
# get-started list.
d['responses']['/session'].update(registered=True, site_view={'allowed': True})
d['responses']['/overview']['status_pages'] = max(1, d['responses']['/overview'].get('status_pages') or 0)
# Open on the ISP style; every style can be tried.
for k in ('/branding', '/brand'):
    d['responses'][k]['style'] = 'noc'
    d['responses'][k]['style_trial'] = {'state': 'active', 'days_left': 7}
d['responses']['/session'].pop('csrf', None)
# The recording machine's paths and listen address: a usual installation's.
for k, v in (('/system', None), ('/system/port', None)):
    r = d['responses'].get(k)
    if isinstance(r, dict):
        for f in ('config_path', 'data_dir'):
            if f in r:
                r[f] = '/var/lib/unagikuma' + ('/monitors.conf' if f == 'config_path' else '')
        if 'listen' in r:
            r['listen'] = ':8077'
        if 'current' in r:
            r['current'] = 8077

# An international setting: sites and names from Los Angeles, Munich and Tokyo.
text = json.dumps(d, ensure_ascii=False)
for a, b in (('北京', 'Munich'), ('广州', 'Tokyo'), ('上海', 'Los Angeles'),
             ('core-sw-bj', 'core-sw-muc'), ('core-sw-sh', 'core-sw-lax'), ('edge-router-gz', 'edge-router-tyo')):
    text = text.replace(a, b)
# Any other path of the recording machine (diagnostics, the editor, scripts).
import re as _re
text = _re.sub(r'/tmp/[^"]*?/cl/[abc](?=[/"])', '/var/lib/unagikuma', text)
open(path, 'w').write(text)
print('patched')
