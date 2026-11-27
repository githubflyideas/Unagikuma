# Adding a page style

A style changes how the console is laid out (cards, tables, spacing,
type, and whether the navigation is a sidebar, a top bar or a bottom
bar). Colour comes from five shared palettes (Calm, Perfume, Country, Industrial, Passion); click the logo to cycle them.

1. Write `web/ui/styles/<id>.css`. The id is lowercase letters, digits
   and `-`. Scope every rule under `:root[data-style="<id>"]` and use the
   theme's colour variables (`--panel`, `--border`, `--text`, `--accent`,
   the status colours …) rather than fixed colours.
2. List it in `web/ui/styles/styles.json`. A style is free unless its
   entry carries `"registered": true`; a registered style is listed for
   everyone and can be picked or set as the default anywhere, but its
   sheet is served only on a registered installation or while the
   installation's seven-day style trial (from the first time a
   registered style is switched on) is running.
3. Name it in every locale under `internal/i18n/locales/`, key
   `ui.style.<id>`.
4. Check it: every theme, a phone width, right-to-left (Arabic, Urdu),
   and colour contrast (WCAG AA) on the console and the public status
   page.


"classic" is the base stylesheet itself and has no file of its own.
The shipped styles are Classic (sidebar) and the four registered
scenario styles: retail (Chain Store, top menu), noc (Modern Dashboard,
top menu), starbridge (Starship, bottom menu) and atelier (Craftsman
Workshop, top menu). All five styles use the same five palettes. A stored
choice naming any other style falls back to "classic".
