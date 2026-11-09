# Upgrading a cluster to 2.2

Back up every server before upgrading. Keep the backups until you have checked that monitoring and notifications work at every site.

## What changes

Each site keeps monitoring and sending its own alerts at all times. Other sites' results are useful context, not votes. You may receive more than one alert about the same problem. Acknowledging or silencing an alert affects only the site that sent it.

The main server manages shared settings. If it is unavailable, sites keep using their last received settings. Choosing a new main server remains a manual action.

## Upgrade steps

1. Upgrade the main server, then the other full servers while they can connect to it. Do not keep a mixture of old and new versions in service.
2. Wait for every site to receive the shared settings. Check each site's monitoring, notifications and connection status before testing an outage.
3. Older probe-only nodes need manual migration: they must be replaced with full servers with their own data directories. They cannot send independent alerts simply by installing the new executable.
4. For a probe-only node, stop the old process first. You can join the replacement under a new site name, move its assigned monitors and groups, then remove the old node. To reuse a name, first save its assignments and group settings, remove its references and old node entry during a maintenance window, join the replacement, then restore those settings. Do not run two servers under the same site identity.

Earlier cluster-wide incidents are kept as history without sending a false recovery. Fresh incidents belong to the site that observes them. Imported incident history is a snapshot, not a live copy of another site's current acknowledgements.

Large history databases need extra disk space and time for the first upgrade. Allow at least one additional history database's worth of free space and schedule a maintenance window. Do not start a restored backup alongside the original server under the same site name.
