package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// Sending through the local mail system (via=sendmail) hands the message
// to the machine's sendmail command, the way Nagios and cron do. Postfix,
// Sendmail, Exim, OpenSMTPD, msmtp, ssmtp, nullmailer and dma all install
// one under that name, so nothing about the mail system needs to be known
// here: it relays, queues and retries with its own configuration.

// sendmailPaths are where mail systems put their sendmail command, the
// first that exists is used: /usr/sbin on Linux, the BSDs and macOS (on
// the BSDs through mailwrapper), /usr/lib for old Unix layouts, /usr/local
// for mail systems installed from ports.
var sendmailPaths = []string{
	"/usr/sbin/sendmail",
	"/usr/lib/sendmail",
	"/usr/bin/sendmail",
	"/usr/local/sbin/sendmail",
	"/usr/local/bin/sendmail",
}

// sendmailNames are the program names accepted for program=: the sendmail
// command of each mail system under its own name. Any other program is
// refused, so a channel cannot be used to run arbitrary commands.
var sendmailNames = map[string]bool{
	"sendmail": true, "sendmail.postfix": true, "sendmail.sendmail": true, "sendmail.exim": true,
	"exim": true, "exim4": true, "msmtp": true, "ssmtp": true, "esmtp": true, "dma": true,
}

// checkSendmailProgram validates program= without touching the disk:
// configuration is checked on machines that may not send.
func checkSendmailProgram(p string) error {
	if p == "" || strings.Contains(p, "${") {
		return nil
	}
	// Checked as a Unix path on every system: a configuration may be
	// checked on a Windows machine for the Unix nodes that send.
	if !path.IsAbs(filepath.ToSlash(p)) && !filepath.IsAbs(p) {
		return errors.New("program must be an absolute path, such as /usr/sbin/sendmail")
	}
	base := strings.TrimSuffix(path.Base(filepath.ToSlash(p)), ".exe")
	if !sendmailNames[base] {
		return fmt.Errorf("program must be the mail system's sendmail command (sendmail, exim, msmtp, ssmtp, …), not %s", base)
	}
	return nil
}

// FindSendmail returns this machine's sendmail command, or "" when there
// is none. program names one explicitly.
func FindSendmail(program string) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	cands := sendmailPaths
	if program != "" {
		cands = []string{program}
	}
	for _, p := range cands {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	if program == "" {
		// A service started with a short PATH may still find it there.
		if p, err := exec.LookPath("sendmail"); err == nil && filepath.IsAbs(p) {
			return p
		}
	}
	return ""
}

// sendSendmail delivers m through the local sendmail command. Only the
// options every sendmail-compatible program knows are used: -oi (a line
// with a single dot does not end the message) and -f (the envelope
// sender, only when from= is set; otherwise the mail system picks it, as
// it does for Nagios). Recipients follow as arguments.
func sendSendmail(ctx context.Context, o map[string]string, m *Message) error {
	if runtime.GOOS == "windows" {
		return errors.New("sending through sendmail is not available on Windows; use an SMTP server")
	}
	prog := FindSendmail(o["program"])
	if prog == "" {
		if o["program"] != "" {
			return fmt.Errorf("%s not found or not executable", o["program"])
		}
		return fmt.Errorf("no sendmail command on this machine (looked in %s); install the mail system's sendmail command (postfix, exim, msmtp, …) or use an SMTP server", strings.Join(sendmailPaths, ", "))
	}
	var from *mail.Address
	if o["from"] != "" {
		a, err := mail.ParseAddress(o["from"])
		if err != nil {
			return errors.New("invalid from address")
		}
		from = a
	}
	to, err := mail.ParseAddressList(o["to"])
	if err != nil || len(to) == 0 {
		return errors.New("invalid to addresses")
	}
	args := []string{"-oi"}
	if from != nil {
		if strings.HasPrefix(from.Address, "-") {
			return errors.New("invalid from address")
		}
		args = append(args, "-f", from.Address)
	}
	for _, a := range to {
		// Not every sendmail understands "--"; an address that looks
		// like an option is refused instead.
		if strings.HasPrefix(a.Address, "-") {
			return fmt.Errorf("invalid recipient %s", a.Address)
		}
		args = append(args, a.Address)
	}
	msg := bytes.ReplaceAll(buildMail(from, to, m), []byte("\r\n"), []byte("\n"))
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Stdin = bytes.NewReader(msg)
	cmd.Dir = "/"
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		detail := strings.Join(strings.Fields(out.String()), " ")
		if len(detail) > 300 {
			detail = detail[:300] + "…"
		}
		if ctx.Err() != nil {
			return fmt.Errorf("%s did not finish in time", prog)
		}
		if detail != "" {
			return fmt.Errorf("%s: %v: %s", prog, err, detail)
		}
		return fmt.Errorf("%s: %v", prog, err)
	}
	return nil
}
