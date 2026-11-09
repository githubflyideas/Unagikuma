package server

import (
	"bytes"
	"encoding/json"
	"html"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/i18n"
)

// The public status page is rendered on the server first: the title, the
// overall state, open notices and every monitor with its uptime are in
// the HTML, so the page reads at once on a slow link, without
// JavaScript, and in search results. The same data is embedded for the
// script, which then keeps the page current without a second fetch.

func (s *Server) serveStatusHTML(w http.ResponseWriter, r *http.Request, name string) bool {
	shell, err := fs.ReadFile(s.opts.Web, "status.html")
	if err != nil {
		return false
	}
	var sp *config.StatusPage
	if name == "" || name == "-" {
		sp = s.domainPage(r)
	} else {
		sp = s.statusPage(name)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if sp == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write(shell) // the script says it does not exist
		return true
	}
	body := s.pageCache.get(sp.Name, s.now(), func() []byte { return s.statusPageBody(sp) })
	lang := i18n.Normalize(firstLang(r.Header.Get("Accept-Language")))
	page := string(shell)
	page = strings.Replace(page, `<html lang="en">`, `<html lang="`+lang+`"`+dirAttr(lang)+`>`, 1)
	page = strings.Replace(page, "<title>Status</title>", "<title>"+html.EscapeString(sp.Title)+"</title>", 1)
	// JSON in a non-executed script element; "</" cannot end it early.
	data := `<script type="application/json" id="page-data">` + strings.ReplaceAll(string(body), "</", `<\/`) + `</script>`
	page = strings.Replace(page, `<div id="app"><div class="boot"></div></div>`, `<div id="app">`+statusFirstScreen(body, lang)+`</div>`+data, 1)
	_, _ = w.Write([]byte(page))
	return true
}

func firstLang(h string) string {
	tag, _, _ := strings.Cut(h, ",")
	tag, _, _ = strings.Cut(tag, ";")
	return strings.TrimSpace(tag)
}

func dirAttr(lang string) string {
	if lang == "ar" || lang == "ur" {
		return ` dir="rtl"`
	}
	return ""
}

// statusFirstScreen renders the page's data as plain HTML in the classes
// the script uses, so swapping in the live page does not move anything.
func statusFirstScreen(body []byte, lang string) string {
	var d struct {
		Title    string
		Logo     string
		Status   string
		Windows  []string
		Updated  time.Time `json:"updated_at"`
		Monitors []struct {
			Title  string
			Group  string
			Status string
			Uptime map[string]*float64
		}
		Announcements []struct {
			Title, Body, Severity string
			Resolved              *time.Time `json:"resolved_at"`
		}
	}
	if json.Unmarshal(body, &d) != nil {
		return `<div class="boot"></div>`
	}
	e := html.EscapeString
	tr := func(key string, args ...any) string { return e(i18n.T(lang, key, args...)) }
	var b bytes.Buffer
	b.WriteString(`<div class="pub"><div class="head">`)
	if d.Logo != "" {
		b.WriteString(`<img src="` + e(d.Logo) + `" alt="">`)
	}
	b.WriteString(`<h1>` + e(d.Title) + `</h1></div>`)
	overall := map[string]string{"ok": "✓ " + tr("ui.pub.all_ok"), "degraded": "! " + tr("ui.pub.degraded")}[d.Status]
	if overall == "" {
		overall = "✗ " + tr("ui.pub.outage")
	}
	b.WriteString(`<div class="overall ` + e(d.Status) + `">` + overall + `</div>`)
	sev := map[string]string{"info": "info", "minor": "warning", "major": "error", "maintenance": "info"}
	for _, a := range d.Announcements {
		if a.Resolved != nil {
			continue
		}
		cls := sev[a.Severity]
		if cls == "" {
			cls = "info"
		}
		b.WriteString(`<div class="banner notice ` + cls + `"><div class="spacer"><div class="row"><b>` + e(a.Title) + `</b><div class="spacer"></div><span class="small muted">` +
			tr("ui.pub.sev."+a.Severity) + `</span></div>`)
		if a.Body != "" {
			b.WriteString(`<div class="pre">` + e(a.Body) + `</div>`)
		}
		b.WriteString(`</div></div>`)
	}
	var groups []string
	byGroup := map[string][]int{}
	for i, m := range d.Monitors {
		if _, ok := byGroup[m.Group]; !ok {
			groups = append(groups, m.Group)
		}
		byGroup[m.Group] = append(byGroup[m.Group], i)
	}
	for _, g := range groups {
		b.WriteString(`<section class="card">`)
		if g != "" {
			b.WriteString(`<div class="hd"><h2>` + e(g) + `</h2></div>`)
		}
		b.WriteString(`<div class="bd flush">`)
		for _, i := range byGroup[g] {
			m := d.Monitors[i]
			var up []string
			for _, w := range d.Windows {
				up = append(up, w+" "+pctText(m.Uptime[w]))
			}
			b.WriteString(`<div class="mon"><div class="row"><i class="dot ` + e(m.Status) + `"></i><b>` + e(m.Title) +
				`</b><div class="spacer"></div><span class="small muted">` + e(strings.Join(up, " · ")) + `</span></div></div>`)
		}
		b.WriteString(`</div></section>`)
	}
	b.WriteString(`<footer>` + tr("ui.pub.updated", "time", d.Updated.UTC().Format("2006-01-02 15:04 UTC")) + `</footer></div>`)
	return b.String()
}

// pctText matches the UI's percentage: truncated, two decimals, "–" when
// there is no figure.
func pctText(r *float64) string {
	if r == nil {
		return "–"
	}
	if *r >= 1 {
		return "100%"
	}
	v := float64(int64(*r*10000)) / 100
	return strconv.FormatFloat(v, 'f', 2, 64) + "%"
}
