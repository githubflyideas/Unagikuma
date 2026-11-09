package server

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// mailbox is a fake SMTP server that keeps every message it receives.
func mailbox(t *testing.T) (string, chan string) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	got := make(chan string, 16)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				w := func(s string) { io.WriteString(c, s+"\r\n") }
				w("220 fake ESMTP")
				var data strings.Builder
				in := false
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if in {
						if line == ".\r\n" {
							in = false
							w("250 queued")
							got <- data.String()
							data.Reset()
						} else {
							data.WriteString(line)
						}
						continue
					}
					switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
					case strings.HasPrefix(cmd, "DATA"):
						in = true
						w("354 go ahead")
					case strings.HasPrefix(cmd, "QUIT"):
						w("221 bye")
						return
					default:
						w("250 ok")
					}
				}
			}()
		}
	}()
	return l.Addr().String(), got
}

func TestStatusPageNoticesSubscriptionsDomain(t *testing.T) {
	addr, inbox := mailbox(t)
	host, port, _ := net.SplitHostPort(addr)
	bind := strings.Replace(testBind, `main    IN STATUSPAGE "Service status" select="group:public"`,
		`mailer  IN CHANNEL email host=`+host+` port=`+port+` tls=none from=status@example.com to=ops@example.com
main    IN STATUSPAGE "Service status" select="group:public" domain=status.example.com subscribe=mailer`, 1)
	e := newEnv(t, bind, nil)
	e.setup()

	var pages []map[string]any
	if code := e.do("GET", "/api/v1/status-pages", nil, &pages); code != 200 || len(pages) != 1 || pages[0]["url"] != "https://status.example.com/" {
		t.Fatalf("pages: %d %v", code, pages)
	}
	// Subscribe from the public page, confirm with the e-mailed link.
	pub := &http.Client{Timeout: 5 * time.Second}
	post := func(path, body string, host string) int {
		req, _ := http.NewRequest("POST", e.ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if host != "" {
			req.Host = host
		}
		resp, err := pub.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("/api/v1/status/main/subscribe", `{"email":"not an address"}`, ""); code != 400 {
		t.Fatalf("bad address: %d", code)
	}
	if code := post("/api/v1/status/main/subscribe", `{"email":"reader@example.org"}`, ""); code != 200 {
		t.Fatalf("subscribe: %d", code)
	}
	var msg string
	select {
	case msg = <-inbox:
	case <-time.After(5 * time.Second):
		t.Fatal("no confirmation e-mail")
	}
	tok := regexp.MustCompile(`confirm=([A-Za-z0-9_-]+)`).FindStringSubmatch(bodyOf(msg))
	if tok == nil || !strings.Contains(msg, "reader@example.org") {
		t.Fatalf("confirmation: %s", msg)
	}
	token := tok[1]
	if code := post("/api/v1/status/main/confirm", `{"token":"nope"}`, ""); code != 404 {
		t.Fatalf("bad token: %d", code)
	}
	// Through the page's own host name, as the e-mailed link would be.
	if code := post("/api/v1/status/-/confirm", `{"token":"`+token+`"}`, "status.example.com"); code != 200 {
		t.Fatalf("confirm: %d", code)
	}

	// An announcement reaches the page, the feed and the subscriber.
	var a map[string]any
	if code := e.do("POST", "/api/v1/status-pages/main/announcements",
		map[string]any{"title": "Database maintenance", "body": "Tonight 22:00", "severity": "maintenance", "notify": true}, &a); code != 200 {
		t.Fatalf("announce: %d %v", code, a)
	}
	select {
	case msg = <-inbox:
		if !strings.Contains(msg, "Database maintenance") || !strings.Contains(bodyOf(msg), "unsubscribe=") {
			t.Fatalf("announcement mail: %s", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscriber not e-mailed")
	}
	var page struct {
		Announcements []map[string]any
		Subscribe     bool
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/v1/status/-", nil)
	req.Host = "status.example.com"
	resp, err := pub.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(resp.Body).Decode(&page)
	resp.Body.Close()
	if len(page.Announcements) != 1 || page.Announcements[0]["author"] != nil || !page.Subscribe {
		t.Fatalf("public page: %+v", page)
	}
	req, _ = http.NewRequest("GET", e.ts.URL+"/feed.atom", nil)
	req.Host = "status.example.com"
	resp, _ = pub.Do(req)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "<title>Database maintenance</title>") {
		t.Fatalf("feed: %d %s", resp.StatusCode, b)
	}
	// The console is not served on the status host name.
	req, _ = http.NewRequest("GET", e.ts.URL+"/api/v1/monitors", nil)
	req.Host = "status.example.com"
	resp, _ = pub.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("console on the status domain: %d", resp.StatusCode)
	}

	id := int64(a["id"].(float64))
	if code := e.do("PUT", "/api/v1/announcements/"+itoa(id), map[string]any{"title": "Database maintenance", "body": "done", "severity": "maintenance", "resolved": true}, &a); code != 200 || a["resolved_at"] == nil {
		t.Fatalf("resolve: %d %v", code, a)
	}
	if code := e.do("DELETE", "/api/v1/announcements/"+itoa(id), nil, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code := post("/api/v1/status/main/unsubscribe", `{"token":"`+token+`"}`, ""); code != 200 {
		t.Fatalf("unsubscribe: %d", code)
	}
}

// bodyOf decodes a base64 message body.
func bodyOf(msg string) string {
	_, body, _ := strings.Cut(msg, "\r\n\r\n")
	b, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(body), "\r\n", ""))
	return string(b)
}
