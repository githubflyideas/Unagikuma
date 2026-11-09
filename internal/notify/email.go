package notify

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strings"
	"time"
)

// sendEmail delivers m over SMTP, or through the local sendmail command
// with via=sendmail. tls=starttls (default, port 587),
// tls=tls (implicit TLS, port 465) or tls=none (only for local relays:
// credentials are never sent over plain connections).
//
// A recipient refused as nonexistent is reported as a bounce and the
// message still goes to the others; it fails only when nobody accepts it.
func sendEmail(ctx context.Context, o map[string]string, m *Message) ([]Bounce, error) {
	if strings.EqualFold(o["via"], "sendmail") {
		return nil, sendSendmail(ctx, o, m)
	}
	host := o["host"]
	port := o["port"]
	mode := strings.ToLower(o["tls"])
	if mode == "" {
		mode = "starttls"
		if port == "465" {
			mode = "tls"
		}
	}
	if port == "" {
		port = map[string]string{"tls": "465", "none": "25"}[mode]
		if port == "" {
			port = "587"
		}
	}
	from, err := mail.ParseAddress(o["from"])
	if err != nil {
		return nil, fmt.Errorf("invalid from address")
	}
	to, err := mail.ParseAddressList(o["to"])
	if err != nil || len(to) == 0 {
		return nil, fmt.Errorf("invalid to addresses")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	d := net.Dialer{Deadline: deadline}
	addr := net.JoinHostPort(host, port)
	var conn net.Conn
	tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if mode == "tls" {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, tlsCfg)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("connect %s: %v", addr, rootCause(err))
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return nil, fmt.Errorf("SMTP greeting: %v", err)
	}
	defer c.Close()
	if mode == "starttls" {
		if okExt, _ := c.Extension("STARTTLS"); !okExt {
			return nil, errors.New("server does not offer STARTTLS (set tls=none only for a local relay)")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return nil, fmt.Errorf("STARTTLS: %v", err)
		}
	}
	if user := o["username"]; user != "" {
		if mode == "none" {
			return nil, errors.New("refusing to send credentials without TLS")
		}
		if err := c.Auth(smtp.PlainAuth("", user, o["password"], host)); err != nil {
			return nil, fmt.Errorf("authentication failed: %v", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return nil, fmt.Errorf("MAIL FROM rejected: %v", err)
	}
	var bounces []Bounce
	var accepted []*mail.Address
	var refused []string
	for _, a := range to {
		err := c.Rcpt(a.Address)
		switch {
		case err == nil:
			accepted = append(accepted, a)
		case isUnknownRecipient(err):
			bounces = append(bounces, Bounce{Address: a.Address, Reason: err.Error()})
		default:
			refused = append(refused, fmt.Sprintf("recipient %s rejected: %v", a.Address, err))
		}
	}
	if len(accepted) == 0 {
		msg := strings.Join(refused, "; ")
		if msg == "" {
			msg = "every recipient was refused as unknown"
		}
		return bounces, errors.New(msg)
	}
	w, err := c.Data()
	if err != nil {
		return bounces, fmt.Errorf("DATA rejected: %v", err)
	}
	if _, err := w.Write(buildMail(from, to, m)); err != nil {
		return bounces, fmt.Errorf("sending message: %v", err)
	}
	if err := w.Close(); err != nil {
		return bounces, fmt.Errorf("message rejected: %v", err)
	}
	return bounces, c.Quit()
}

// isUnknownRecipient tells "no such mailbox" (a bounce that will repeat
// every time) from other refusals such as relaying denied or a full disk.
func isUnknownRecipient(err error) bool {
	var te *textproto.Error
	if !errors.As(err, &te) || te.Code/100 != 5 {
		return false
	}
	msg := strings.ToLower(te.Msg)
	for _, enh := range []string{"5.1.1", "5.1.2", "5.1.6", "5.1.10"} {
		if strings.HasPrefix(msg, enh+" ") || strings.HasPrefix(msg, enh+"\n") || msg == enh {
			return true
		}
	}
	if te.Code != 550 && te.Code != 551 && te.Code != 553 {
		return false
	}
	for _, w := range []string{"user unknown", "unknown user", "no such user", "does not exist", "not exist", "mailbox unavailable", "recipient unknown", "invalid recipient", "no mailbox", "address rejected", "用户不存在", "收件人不存在"} {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}

func buildMail(from *mail.Address, to []*mail.Address, m *Message) []byte {
	title, body := Render(m)
	var b strings.Builder
	list := make([]string, len(to))
	for i, a := range to {
		list[i] = a.String()
	}
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	domain := "unagikuma.local"
	if from == nil {
		// Through sendmail without from=: the mail system adds From as
		// it does for Nagios, and the Message-ID names this machine.
		if h, err := os.Hostname(); err == nil && h != "" {
			domain = h
		}
	} else if i := strings.LastIndex(from.Address, "@"); i >= 0 {
		domain = from.Address[i+1:]
	}
	var headers [][2]string
	if from != nil {
		headers = append(headers, [2]string{"From", from.String()})
	}
	headers = append(headers, [][2]string{
		{"To", strings.Join(list, ", ")},
		{"Subject", mime.QEncoding.Encode("utf-8", strings.Join(strings.Fields(title), " "))},
		{"Date", time.Now().Format(time.RFC1123Z)},
		{"Message-ID", "<" + hex.EncodeToString(id) + "@" + domain + ">"},
		{"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=utf-8"},
		{"Content-Transfer-Encoding", "base64"},
		{"Auto-Submitted", "auto-generated"},
	}...)
	if m.DeliveryID != "" {
		headers = append(headers, [2]string{"X-Unagikuma-Delivery-Id", m.DeliveryID})
	}
	for _, h := range headers {
		b.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	b.WriteString("\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(body + "\n"))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return []byte(b.String())
}

func rootCause(err error) error {
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Err
	}
	return err
}
