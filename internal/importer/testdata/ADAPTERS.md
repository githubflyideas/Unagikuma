# Checkmate and Gatus fixture provenance

These are independently authored, synthetic **format fixtures**, not captured exports or upstream implementation code. Targets use `example.invalid`; monitors are disabled so integration tests never probe external services. No AGPL implementation code is incorporated.

## Checkmate

Project: `bluewave-labs/Checkmate`, **develop commit `df3f2fbabd95ea09509468c4990dd92fc0886e95`**, inspected November 1, 2026. There is no export schema version field; compatibility is pinned to these fields, not all Checkmate releases.

Primary references (replace paths after this immutable prefix):
`https://github.com/bluewave-labs/Checkmate/blob/df3f2fbabd95ea09509468c4990dd92fc0886e95/`

- `client/src/Pages/Settings/index.tsx`, `handleExportMonitors`: downloads **monitors.json**, serializing the response's `data` array, not the API response envelope.
- `server/src/api/controllers/monitorController.ts`, `exportMonitorsToJSON`: API envelope is `{success,msg,data}`. The adapter intentionally accepts the downloaded array only.
- `server/src/domain/monitors/monitor.service.ts`, `exportMonitorsToJSON`: obtains team monitors with recent checks excluded.
- `server/src/domain/monitors/monitor.schema.ts`, `monitorSchema`: names, URL, method, interval in milliseconds, group, secret, isActive, proxyMode, status-window and matching fields.
- `server/src/domain/monitors/monitor.model.ts`: defaults include 60000 ms, statusWindowSize 5, threshold 60, proxyMode inherit.
- `server/src/domain/monitors/monitor.type.ts`: monitor types and proxyMode enum.
- `server/src/service/networkProviders/HttpProvider.ts`: bearer secret, TLS verification option, 30-second timeout, method; advanced matching, cookie handling and transport retry behavior.
- `server/src/service/networkProviders/utils.ts`, `isStatusUp`: **2xx OR customUpCodes**, not replacement of 2xx with custom codes.

`checkmate.json` deliberately uses `proxyMode: none`, status window size 1 and threshold 100. Typical default exports are imported too: `proxyMode: inherit` is imported without a proxy (the export does not carry the inherited settings, and the report says so), and a status window (size, threshold) maps to attempts = ceil(size × threshold / 100), the smallest run of failures that fills the window's failing share — approximate, and reported whenever the window holds more than one check. Port monitors become TCP checks on host:port from the url and the port field; ping monitors become ping checks. Foreign IDs and observations exist in the fixture to verify they cannot become accounts, authority or live status.

## Gatus

Project: `TwiN/gatus`, **master commit `d255d0cd4d69ce818574cfdb3a7e82407e6559d7`**, inspected November 1, 2026. No blanket release-version guarantee.

Primary reference:
`https://github.com/TwiN/gatus/blob/d255d0cd4d69ce818574cfdb3a7e82407e6559d7/README.md`

Sections: Endpoints, Conditions, Client configuration, External Endpoints, Suites. Endpoint fields are `name`, `group`, `url`, `enabled`, `interval`, `method`, `body`, `headers`, `conditions`, `client`. Defaults: interval 60s, method GET, enabled true, client timeout 10s, follow redirects, verify TLS. Conditions are conjunctive. The adapter maps them: `[STATUS]` comparisons combine into the accepted status set; `[BODY]` text and `[BODY].path` comparisons become keyword / json-path checks; `[CERTIFICATE_EXPIRATION] > <whole days>` becomes cert-days; `[RESPONSE_TIME]` and further conditions of a kind a record can only hold once are named in the report as not carried over. `icmp://` endpoints become ping checks, `tcp://` TCP checks, and endpoints with a `dns` block DNS checks ([DNS_RCODE] == NOERROR is the check itself; a [BODY] answer becomes expect=). An endpoint is left out only for what truly cannot be represented (an inverted check, contradictory status conditions, an unresolvable placeholder URL, an unknown condition), with every reason listed; what was left out never blocks the endpoints that did import. Endpoint fields with no equivalent (alerts, ui, …) are ignored with a note. The YAML reader resolves anchors, aliases and merge keys.

`gatus.yaml` uses that documented endpoint shape. Tests add headers/body, unsupported conditions, anchors, aliases, merges, duplicate keys, placeholders, client file references and collisions. Gatus configuration loading/env interpolation (placeholders stay unresolved and are refused per endpoint), file-based credentials, remote includes, global settings and external endpoints are intentionally not implemented.
