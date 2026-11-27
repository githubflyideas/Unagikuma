package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

// The status page's first screen is in the HTML itself.
func TestStatusPageServerRendered(t *testing.T) {
	ui := fstest.MapFS{"status.html": {Data: []byte(`<!doctype html>
<html lang="en">
<head><title>Status</title></head>
<body class="public">
<div id="app"><div class="boot"></div></div>
</body>
</html>`)}}
	e := newEnv(t, testBind, ui)
	e.setup()
	get := func(path, lang string) (int, string) {
		req, _ := http.NewRequest("GET", e.ts.URL+path, nil)
		req.Header.Set("Accept-Language", lang)
		resp, err := e.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	code, page := get("/status/main", "zh-CN,zh;q=0.9")
	for _, want := range []string{`<html lang="zh">`, "<title>Service status</title>", `<h1>Service status</h1>`, "Web site", `class="overall`,
		`<script type="application/json" id="page-data">`} {
		if code != 200 || !strings.Contains(page, want) {
			t.Fatalf("%d: page lacks %q:\n%s", code, want, page)
		}
	}
	// Only what the page publishes: db is not selected by group:public.
	if strings.Contains(page, "192.168.1.20") || strings.Contains(page, `"db"`) {
		t.Fatalf("unpublished monitor in the page:\n%s", page)
	}
	if code, _ := get("/status/nope", "en"); code != 404 {
		t.Fatalf("unknown page: %d", code)
	}
}

func TestStatusPageCreateRemoved(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	if code := e.do("POST", "/api/v1/status-pages", map[string]any{"name": "public", "title": "Our services", "select": "type:HTTP"}, nil); code != 405 {
		t.Fatalf("create should be gone: %d", code)
	}
	var a map[string]any
	if code := e.do("POST", "/api/v1/announcements", map[string]any{"title": "Board", "body": "Hello **all**", "severity": "info"}, &a); code != 200 || a["title"] != "Board" {
		t.Fatalf("board: %d %+v", code, a)
	}
	var list []map[string]any
	if code := e.do("GET", "/api/v1/announcements", nil, &list); code != 200 || len(list) != 1 {
		t.Fatalf("list: %d %+v", code, list)
	}
}
