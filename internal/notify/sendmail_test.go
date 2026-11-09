package notify

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeSendmail writes a sendmail stand-in that records its arguments and
// the message, and fails with exit 75 (as postfix does when it cannot
// queue) when the first recipient is fail@example.com.
func fakeSendmail(t *testing.T) (prog, args, msg string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no sendmail on Windows")
	}
	dir := t.TempDir()
	prog, args, msg = filepath.Join(dir, "sendmail"), filepath.Join(dir, "args"), filepath.Join(dir, "msg")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >" + args + "\ncat >" + msg + "\n" +
		"for a; do [ \"$a\" = fail@example.com ] && { echo 'postdrop: warning: unable to look up public/pickup' >&2; exit 75; }; done\nexit 0\n"
	if err := os.WriteFile(prog, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return prog, args, msg
}

func TestSendmail(t *testing.T) {
	prog, argsFile, msgFile := fakeSendmail(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m := &Message{Kind: "PROBLEM", Monitor: "db", Title: "Postgres", Reason: "refused", Time: time.Now()}

	// Without from=: no -f and no From header, the mail system adds them.
	o := map[string]string{"via": "sendmail", "program": prog, "to": "Ops <ops@example.com>, dba@example.com"}
	if err := ValidateChannel("email", map[string]string{"via": "sendmail", "to": o["to"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := sendEmail(ctx, o, m); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsFile)
	if got := strings.Fields(string(args)); strings.Join(got, " ") != "-oi ops@example.com dba@example.com" {
		t.Fatalf("args %q", got)
	}
	msg, _ := os.ReadFile(msgFile)
	if strings.Contains(string(msg), "\r") || strings.Contains(string(msg), "\nFrom:") || strings.HasPrefix(string(msg), "From:") {
		t.Fatalf("message should have plain newlines and no From:\n%s", msg)
	}
	if !strings.Contains(string(msg), "To: \"Ops\" <ops@example.com>, <dba@example.com>") || !strings.Contains(string(msg), "Subject: ") {
		t.Fatalf("headers missing:\n%s", msg)
	}

	// With from=: the envelope sender and the header follow it.
	o["from"] = "Monitoring <mon@example.com>"
	if _, err := sendEmail(ctx, o, m); err != nil {
		t.Fatal(err)
	}
	args, _ = os.ReadFile(argsFile)
	if got := strings.Fields(string(args)); strings.Join(got, " ") != "-oi -f mon@example.com ops@example.com dba@example.com" {
		t.Fatalf("args %q", got)
	}
	if msg, _ = os.ReadFile(msgFile); !strings.HasPrefix(string(msg), "From: \"Monitoring\" <mon@example.com>\n") {
		t.Fatalf("From header:\n%s", msg)
	}

	// A refusal reports the program's own words.
	o["to"] = "fail@example.com"
	_, err := sendEmail(ctx, o, m)
	if err == nil || !strings.Contains(err.Error(), "unable to look up public/pickup") || !strings.Contains(err.Error(), "75") {
		t.Fatalf("err %v", err)
	}

	// An address that looks like an option never reaches the program.
	o["to"] = "-oQ/tmp@example.com"
	if _, err := sendEmail(ctx, o, m); err == nil || !strings.Contains(err.Error(), "invalid recipient") {
		t.Fatalf("err %v", err)
	}

	// A program that is not there says so.
	o["to"], o["program"] = "ops@example.com", filepath.Join(t.TempDir(), "sendmail")
	if _, err := sendEmail(ctx, o, m); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err %v", err)
	}
}

func TestEmailOptions(t *testing.T) {
	for _, c := range []struct {
		opts map[string]string
		bad  string
	}{
		{map[string]string{"host": "smtp.example.com", "from": "a@example.com", "to": "b@example.com"}, ""},
		{map[string]string{"via": "smtp", "from": "a@example.com", "to": "b@example.com"}, "host is required"},
		{map[string]string{"host": "smtp.example.com", "to": "b@example.com"}, "from is required"},
		{map[string]string{"host": "smtp.example.com", "from": "a@example.com", "to": "b@example.com", "program": "/usr/sbin/sendmail"}, "for via=sendmail"},
		{map[string]string{"via": "sendmail", "to": "b@example.com"}, ""},
		{map[string]string{"via": "sendmail", "to": "b@example.com", "program": "/usr/sbin/sendmail.postfix"}, ""},
		{map[string]string{"via": "sendmail", "to": "b@example.com", "program": "/usr/bin/msmtp"}, ""},
		{map[string]string{"via": "sendmail", "to": "b@example.com", "host": "localhost"}, "host is for via=smtp"},
		{map[string]string{"via": "sendmail", "to": "b@example.com", "program": "sendmail"}, "absolute path"},
		{map[string]string{"via": "sendmail", "to": "b@example.com", "program": "/bin/sh"}, "sendmail command"},
		{map[string]string{"via": "pigeon", "to": "b@example.com"}, "via must be one of"},
	} {
		err := ValidateChannel("email", c.opts)
		if c.bad == "" && err != nil || c.bad != "" && (err == nil || !strings.Contains(err.Error(), c.bad)) {
			t.Errorf("%v: err %v, want %q", c.opts, err, c.bad)
		}
	}
}
