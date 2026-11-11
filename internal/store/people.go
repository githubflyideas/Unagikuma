package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

// The contact directory lists the people who may receive alerts and their
// addresses. Channels name addresses (or @handles); e-mail and SMS go only
// to active people in it: an address of someone who left, is on leave or
// was transferred, one that keeps bouncing, and one the directory does not
// know are not sent to.

// Contact statuses. Left is kept as LeftAt (it also disables the login).
const (
	StatusActive      = "active"
	StatusLeave       = "leave"
	StatusTransferred = "transferred"
	StatusLeft        = "left"
)

// Contact kinds.
const (
	ContactEmail = "email"
	ContactPhone = "phone"
)

// Person is one entry of the directory.
type Person struct {
	ID       int64    `json:"id"`
	Handle   string   `json:"handle"` // used as @handle in channels
	Name     string   `json:"name"`
	Note     string   `json:"note,omitempty"`
	Username string   `json:"username,omitempty"` // linked login, disabled when the person leaves
	Emails   []string `json:"emails"`
	Phones   []string `json:"phones"`
	// Notify are the channels (e-mail, SMS, voice) through which the
	// system reaches this person directly.
	Notify []string `json:"notify"`
	// Status is active, leave, transferred or left.
	Status string `json:"status"`
	// NoReport: the person switched the daily status report off.
	NoReport bool      `json:"no_report"`
	LeftAt   time.Time `json:"left_at,omitzero"`
	Created  time.Time `json:"created_at"`
	Updated  time.Time `json:"updated_at"`
}

// Left reports whether the person left: nothing is sent to them.
func (p Person) Left() bool { return !p.LeftAt.IsZero() }

// Active reports whether the person is sent to: not left, on leave or
// transferred.
func (p Person) Active() bool { return !p.Left() && (p.Status == "" || p.Status == StatusActive) }

// ValidStatus reports whether s is a contact status.
func ValidStatus(s string) bool {
	switch s {
	case StatusActive, StatusLeave, StatusTransferred, StatusLeft:
		return true
	}
	return false
}

// BounceInfo is how often an address was refused as nonexistent.
type BounceInfo struct {
	Kind    string    `json:"kind"`
	Address string    `json:"address"`
	Count   int       `json:"count"`
	First   time.Time `json:"first_at"`
	Last    time.Time `json:"last_at"`
	Reason  string    `json:"reason"`
}

var handleRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidHandle reports whether s can be an @handle.
func ValidHandle(s string) bool { return handleRe.MatchString(s) }

// NormalizeEmail is the form addresses are compared in.
func NormalizeEmail(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "<"); i >= 0 && strings.HasSuffix(s, ">") {
		s = s[i+1 : len(s)-1]
	}
	return strings.ToLower(s)
}

// NormalizePhone keeps the digits and a leading '+' ("00" becomes '+').
func NormalizePhone(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if strings.HasPrefix(out, "00") {
		out = "+" + out[2:]
	}
	return out
}

// SamePhone compares two numbers written with or without the country
// code: +8613800000000 and 13800000000 are one phone.
func SamePhone(a, b string) bool {
	a, b = NormalizePhone(a), NormalizePhone(b)
	if a == b {
		return a != ""
	}
	if len(a) < len(b) {
		a, b = b, a
	}
	// a is longer: it must be '+', a 1-3 digit country code, then b.
	if !strings.HasPrefix(a, "+") || strings.HasPrefix(b, "+") || len(b) < 7 || !strings.HasSuffix(a, b) {
		return false
	}
	cc := len(a) - len(b) - 1
	return cc >= 1 && cc <= 3
}

func normalizeContact(kind, v string) string {
	if kind == ContactPhone {
		return NormalizePhone(v)
	}
	return NormalizeEmail(v)
}

// People lists the directory with each person's addresses.
func (s *Store) People() ([]Person, error) {
	rows, err := s.core.Query(`SELECT id,handle,name,note,username,left_at,created_at,updated_at,notify,status,no_report FROM people ORDER BY name, handle`)
	if err != nil {
		return nil, err
	}
	out := []Person{}
	byID := map[int64]int{}
	for rows.Next() {
		var p Person
		var left, created, updated int64
		var notify string
		if err := rows.Scan(&p.ID, &p.Handle, &p.Name, &p.Note, &p.Username, &left, &created, &updated, &notify, &p.Status, &p.NoReport); err != nil {
			rows.Close()
			return nil, err
		}
		p.LeftAt, p.Created, p.Updated = fromMS(left), fromMS(created), fromMS(updated)
		switch {
		case !p.LeftAt.IsZero():
			p.Status = StatusLeft
		case p.Status == "":
			p.Status = StatusActive
		}
		p.Emails, p.Phones, p.Notify = []string{}, []string{}, splitNotify(notify)
		byID[p.ID] = len(out)
		out = append(out, p)
	}
	rows.Close()
	crows, err := s.core.Query(`SELECT person_id,kind,value FROM contacts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	for crows.Next() {
		var id int64
		var kind, v string
		if err := crows.Scan(&id, &kind, &v); err != nil {
			return nil, err
		}
		i, ok := byID[id]
		if !ok {
			continue
		}
		if kind == ContactPhone {
			out[i].Phones = append(out[i].Phones, v)
		} else {
			out[i].Emails = append(out[i].Emails, v)
		}
	}
	return out, crows.Err()
}

// ErrContactTaken means the address already belongs to someone else.
var ErrContactTaken = errors.New("address already belongs to another person")

// ErrNoAddress means a person needs at least one address to be reached at.
var ErrNoAddress = errors.New("a contact needs at least one valid e-mail address or phone number")

// ValidContact reports whether v is a usable address of the kind: an
// e-mail address with a domain, or a phone number of 6 to 15 digits
// (an optional leading '+').
func ValidContact(kind, v string) bool {
	if kind == ContactPhone {
		p := strings.TrimPrefix(NormalizePhone(v), "+")
		return len(p) >= 6 && len(p) <= 15
	}
	a, err := mail.ParseAddress(strings.TrimSpace(v))
	if err != nil {
		return false
	}
	at := strings.LastIndex(a.Address, "@")
	return at > 0 && strings.Contains(a.Address[at+1:], ".") && !strings.HasSuffix(a.Address, ".")
}

func splitNotify(s string) []string {
	out := []string{}
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// SavePerson creates (ID 0) or replaces a person and their addresses.
func (s *Store) SavePerson(p Person) (int64, error) {
	p.Handle = strings.ToLower(strings.TrimSpace(p.Handle))
	if !ValidHandle(p.Handle) {
		return 0, fmt.Errorf("invalid handle %q: lower-case letters, digits, '.', '_', '-'", p.Handle)
	}
	if strings.TrimSpace(p.Name) == "" {
		p.Name = p.Handle
	}
	// A contact is someone the system can reach: at least one valid
	// address, and every address valid.
	n := 0
	for kind, list := range map[string][]string{ContactEmail: p.Emails, ContactPhone: p.Phones} {
		for _, v := range list {
			if strings.TrimSpace(v) == "" {
				continue
			}
			if !ValidContact(kind, v) {
				return 0, fmt.Errorf("%q is not a valid %s", strings.TrimSpace(v), map[string]string{ContactEmail: "e-mail address", ContactPhone: "phone number"}[kind])
			}
			n++
		}
	}
	if n == 0 {
		return 0, ErrNoAddress
	}
	notify := strings.Join(p.Notify, ",")
	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := s.core.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := ms(s.now())
	if p.ID == 0 {
		res, err := tx.Exec(`INSERT INTO people(handle,name,note,username,created_at,updated_at,notify) VALUES(?,?,?,?,?,?,?)`, p.Handle, p.Name, p.Note, p.Username, now, now, notify)
		if err != nil {
			return 0, uniqueErr(err, "handle @"+p.Handle+" is already used")
		}
		p.ID, _ = res.LastInsertId()
	} else {
		res, err := tx.Exec(`UPDATE people SET handle=?,name=?,note=?,username=?,updated_at=?,notify=? WHERE id=?`, p.Handle, p.Name, p.Note, p.Username, now, notify, p.ID)
		if err != nil {
			return 0, uniqueErr(err, "handle @"+p.Handle+" is already used")
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return 0, ErrNotFound
		}
		if _, err := tx.Exec(`DELETE FROM contacts WHERE person_id=?`, p.ID); err != nil {
			return 0, err
		}
	}
	for kind, list := range map[string][]string{ContactEmail: p.Emails, ContactPhone: p.Phones} {
		for _, v := range list {
			if v = normalizeContact(kind, v); v == "" {
				continue
			}
			if _, err := tx.Exec(`INSERT INTO contacts(person_id,kind,value) VALUES(?,?,?)`, p.ID, kind, v); err != nil {
				return 0, uniqueErr(err, v+": "+ErrContactTaken.Error())
			}
		}
	}
	return p.ID, tx.Commit()
}

func uniqueErr(err error, msg string) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return errors.New(msg)
	}
	return err
}

// DeletePerson removes a person and their addresses.
func (s *Store) DeletePerson(id int64) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.core.Exec(`DELETE FROM contacts WHERE person_id=?`, id); err != nil {
		return err
	}
	res, err := s.core.Exec(`DELETE FROM people WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetLeft marks a person as having left (or back). Leaving also disables
// their linked login: sessions end and API tokens stop working.
func (s *Store) SetLeft(id int64, left bool) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	var username string
	err := s.core.QueryRow(`SELECT username FROM people WHERE id=?`, id).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	at := int64(0)
	if left {
		at = ms(s.now())
	}
	if username != "" {
		var uid int64
		if err := s.core.QueryRow(`SELECT id FROM users WHERE username=?`, username).Scan(&uid); err == nil {
			if left && s.onlyAdmin(uid) {
				return ErrLastAdmin
			}
			if _, err := s.core.Exec(`UPDATE users SET disabled=? WHERE id=?`, boolInt(left), uid); err != nil {
				return err
			}
			if left {
				if _, err := s.core.Exec(`DELETE FROM sessions WHERE user_id=?`, uid); err != nil {
					return err
				}
			}
		}
	}
	_, err = s.core.Exec(`UPDATE people SET left_at=?, updated_at=? WHERE id=?`, at, ms(s.now()), id)
	return err
}

// SetStatus sets a person's status. Leaving disables their login (see
// SetLeft); any other status enables it again.
func (s *Store) SetStatus(id int64, status string) error {
	if !ValidStatus(status) {
		return fmt.Errorf("invalid status %q: active, leave, transferred or left", status)
	}
	// Only a change into or out of "left" touches the login.
	var left int64
	err := s.core.QueryRow(`SELECT left_at FROM people WHERE id=?`, id).Scan(&left)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if was := left != 0; was != (status == StatusLeft) {
		if err := s.SetLeft(id, status == StatusLeft); err != nil {
			return err
		}
	}
	v := status
	if v == StatusActive || v == StatusLeft {
		v = ""
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = s.core.Exec(`UPDATE people SET status=?, updated_at=? WHERE id=?`, v, ms(s.now()), id)
	return err
}

// SetReport switches the daily status report on or off for a person.
func (s *Store) SetReport(id int64, receive bool) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	res, err := s.core.Exec(`UPDATE people SET no_report=?, updated_at=? WHERE id=?`, boolInt(!receive), ms(s.now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// RecordBounce counts one refusal of an address as nonexistent.
func (s *Store) RecordBounce(kind, address, reason string) error {
	v := normalizeContact(kind, address)
	if v == "" {
		return nil
	}
	if len(reason) > 300 {
		reason = reason[:300]
	}
	now := ms(s.now())
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`INSERT INTO bounces(kind,value,n,first_at,last_at,reason) VALUES(?,?,1,?,?,?)
		ON CONFLICT(kind,value) DO UPDATE SET n=n+1, last_at=excluded.last_at, reason=excluded.reason`, kind, v, now, now, reason)
	return err
}

// Bounces lists the addresses that bounced.
func (s *Store) Bounces() ([]BounceInfo, error) {
	rows, err := s.core.Query(`SELECT kind,value,n,first_at,last_at,reason FROM bounces ORDER BY last_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BounceInfo{}
	for rows.Next() {
		var b BounceInfo
		var first, last int64
		if err := rows.Scan(&b.Kind, &b.Address, &b.Count, &first, &last, &b.Reason); err != nil {
			return nil, err
		}
		b.First, b.Last = fromMS(first), fromMS(last)
		out = append(out, b)
	}
	return out, rows.Err()
}

// ClearBounces forgets an address's bounces (it was fixed).
func (s *Store) ClearBounces(kind, address string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`DELETE FROM bounces WHERE kind=? AND value=?`, kind, normalizeContact(kind, address))
	return err
}
