package server

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
)

// Two installations on the same host use different session cookie names
// (the installation number), so a login on one is not a login on the other.
func TestSessionCookiesDoNotClashAcrossInstallations(t *testing.T) {
	a := newEnv(t, testBind, nil)
	b := newEnv(t, testBind, nil)
	a.setup()
	b.setup()
	an, bn := a.srv.sessionCookie(), b.srv.sessionCookie()
	if an == bn {
		t.Fatalf("two installations share a cookie name %q", an)
	}
	if !strings.HasPrefix(an, sessionCookiePrefix+"_") || !strings.HasPrefix(bn, sessionCookiePrefix+"_") {
		t.Fatalf("cookie names %q and %q are not scoped to an installation", an, bn)
	}

	ua, _ := url.Parse(a.ts.URL)
	ub, _ := url.Parse(b.ts.URL)
	var aCookie *http.Cookie
	for _, c := range a.c.Jar.Cookies(ua) {
		if c.Name == an {
			aCookie = c
		}
	}
	if aCookie == nil {
		t.Fatalf("setup did not set %s", an)
	}
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(ub, []*http.Cookie{aCookie})
	cl := &http.Client{Jar: jar}
	resp, err := cl.Get(b.ts.URL + "/api/v1/session")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("B accepted A's session cookie: %d", resp.StatusCode)
	}
}

func TestHTMLShellCarriesInstallation(t *testing.T) {
	ui := fstest.MapFS{
		"index.html": {Data: []byte("<html><head><title>x</title></head><body>app</body></html>")},
	}
	e := newEnv(t, testBind, ui)
	code, body := e.raw("GET", "/", nil, "")
	if code != 200 {
		t.Fatalf("GET /: %d", code)
	}
	id := e.app.License().InstallationID()
	want := `data-install="` + id + `"`
	if !strings.Contains(string(body), want) {
		t.Fatalf("shell missing %s: %s", want, body)
	}
}
