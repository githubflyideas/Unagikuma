# Adding a page style

A style changes how the console is laid out (cards, tables, spacing,
type); colours stay with the theme, so a style must work with every
theme, light and dark.

1. Write `web/ui/styles/<id>.css`. The id is lowercase letters, digits
   and `-`. Scope every rule under `:root[data-style="<id>"]` and use the
   theme's colour variables (`--panel`, `--border`, `--text`, `--accent`,
   the status colours …) rather than fixed colours.
2. List it in `web/ui/styles/styles.json`. Add `"registered": true` to
   offer it to registered installations only: unregistered, it is shown
   as unavailable and its file is not served.
3. Name it in every locale under `internal/i18n/locales/`, key
   `ui.style.<id>`.
4. Check it: every theme, a phone width, right-to-left (Arabic, Urdu),
   and colour contrast (WCAG AA) on the console and the public status
   page.

"classic" is the base stylesheet itself and has no file of its own.
