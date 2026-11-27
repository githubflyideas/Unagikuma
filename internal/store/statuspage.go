package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Announcement is a notice on the single announcement board.
type Announcement struct {
	ID         int64      `json:"id"`
	Title      string     `json:"title"`
	Body       string     `json:"body"`
	Severity   string     `json:"severity"` // info | minor | major | maintenance
	Author     string     `json:"author,omitempty"`
	Pinned     bool       `json:"pinned"`
	StartsAt   *time.Time `json:"starts_at,omitempty"`
	EndsAt     *time.Time `json:"ends_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// AnnouncementSeverities are the accepted severities.
var AnnouncementSeverities = []string{"info", "minor", "major", "maintenance"}

// SaveAnnouncement adds a (ID 0) or updates an announcement and returns it.
func (s *Store) SaveAnnouncement(a Announcement, resolved bool) (Announcement, error) {
	now := s.now()
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if a.ID == 0 {
		res, err := s.core.Exec(`INSERT INTO announcements(title,body,severity,author,pinned,starts_at,ends_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			a.Title, a.Body, a.Severity, a.Author, boolInt(a.Pinned), msPtr(a.StartsAt), msPtr(a.EndsAt), ms(now), ms(now))
		if err != nil {
			return a, err
		}
		a.ID, _ = res.LastInsertId()
	} else {
		res, err := s.core.Exec(`UPDATE announcements SET title=?, body=?, severity=?, pinned=?, starts_at=?, ends_at=?, updated_at=? WHERE id=?`,
			a.Title, a.Body, a.Severity, boolInt(a.Pinned), msPtr(a.StartsAt), msPtr(a.EndsAt), ms(now), a.ID)
		if err != nil {
			return a, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return a, ErrNotFound
		}
	}
	if resolved {
		_, _ = s.core.Exec(`UPDATE announcements SET resolved_at=COALESCE(resolved_at, ?) WHERE id=?`, ms(now), a.ID)
	} else {
		_, _ = s.core.Exec(`UPDATE announcements SET resolved_at=NULL WHERE id=?`, a.ID)
	}
	return s.announcement(a.ID)
}

func (s *Store) announcement(id int64) (Announcement, error) {
	list, err := s.queryAnnouncements(`WHERE id=?`, id)
	if err != nil {
		return Announcement{}, err
	}
	if len(list) == 0 {
		return Announcement{}, ErrNotFound
	}
	return list[0], nil
}

// Announcement returns one announcement.
func (s *Store) Announcement(id int64) (Announcement, error) { return s.announcement(id) }

// DeleteAnnouncement removes an announcement.
func (s *Store) DeleteAnnouncement(id int64) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	res, err := s.core.Exec(`DELETE FROM announcements WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Announcements lists open announcements and those resolved since the
// given time, pinned first, then newest first.
func (s *Store) AnnouncementCount() (int, error) {
	var n int
	err := s.core.QueryRow(`SELECT COUNT(*) FROM announcements`).Scan(&n)
	return n, err
}

func (s *Store) Announcements(since time.Time, limit int) ([]Announcement, error) {
	return s.queryAnnouncements(`WHERE resolved_at IS NULL OR resolved_at >= ? ORDER BY pinned DESC, created_at DESC, id DESC LIMIT ?`,
		ms(since), limit)
}

func (s *Store) queryAnnouncements(where string, args ...any) ([]Announcement, error) {
	rows, err := s.core.Query(`SELECT id,title,body,severity,author,pinned,starts_at,ends_at,created_at,updated_at,resolved_at FROM announcements `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Announcement{}
	for rows.Next() {
		var a Announcement
		var pin int
		var start, end, r sql.NullInt64
		var c, u int64
		if err := rows.Scan(&a.ID, &a.Title, &a.Body, &a.Severity, &a.Author, &pin, &start, &end, &c, &u, &r); err != nil {
			return nil, err
		}
		a.Pinned = pin != 0
		a.StartsAt, a.EndsAt = timePtr(start), timePtr(end)
		a.CreatedAt, a.UpdatedAt = fromMS(c), fromMS(u)
		if r.Valid {
			t := fromMS(r.Int64)
			a.ResolvedAt = &t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func msPtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return ms(*t)
}

func timePtr(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := fromMS(n.Int64)
	return &t
}

// Subscriber is an e-mail address following a status page.
type Subscriber struct {
	Page      string
	Email     string
	Token     string // in confirmation and unsubscribe links
	Confirmed bool
}

// ErrTooManyPending means a page has too many unconfirmed subscriptions.
var ErrTooManyPending = errors.New("too many unconfirmed subscriptions")

// Subscribe records an unconfirmed subscription and returns its token;
// for an address already subscribed it returns the existing one, and
// whether it is confirmed.
func (s *Store) Subscribe(page, email string, maxPending int) (Subscriber, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	s.wmu.Lock()
	defer s.wmu.Unlock()
	sub := Subscriber{Page: page, Email: email}
	var confirmed sql.NullInt64
	err := s.core.QueryRow(`SELECT token, confirmed_at FROM subscribers WHERE page=? AND email=?`, page, email).Scan(&sub.Token, &confirmed)
	if err == nil {
		sub.Confirmed = confirmed.Valid
		return sub, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return sub, err
	}
	// Forget confirmations nobody clicked within a week.
	_, _ = s.core.Exec(`DELETE FROM subscribers WHERE confirmed_at IS NULL AND created_at < ?`, ms(s.now().Add(-7*24*time.Hour)))
	var pending int
	if err := s.core.QueryRow(`SELECT COUNT(*) FROM subscribers WHERE page=? AND confirmed_at IS NULL`, page).Scan(&pending); err != nil {
		return sub, err
	}
	if pending >= maxPending {
		return sub, ErrTooManyPending
	}
	sub.Token = randomString(24)
	_, err = s.core.Exec(`INSERT INTO subscribers(page,email,token,created_at) VALUES(?,?,?,?)`, page, email, sub.Token, ms(s.now()))
	return sub, err
}

// ConfirmSubscriber confirms the subscription with this token.
func (s *Store) ConfirmSubscriber(page, token string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	res, err := s.core.Exec(`UPDATE subscribers SET confirmed_at=COALESCE(confirmed_at, ?) WHERE page=? AND token=?`, ms(s.now()), page, token)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Unsubscribe removes the subscription with this token.
func (s *Store) Unsubscribe(page, token string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	res, err := s.core.Exec(`DELETE FROM subscribers WHERE page=? AND token=?`, page, token)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Subscribers lists a page's confirmed subscribers.
func (s *Store) Subscribers(page string) ([]Subscriber, error) {
	rows, err := s.core.Query(`SELECT email, token FROM subscribers WHERE page=? AND confirmed_at IS NOT NULL ORDER BY id`, page)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscriber
	for rows.Next() {
		sub := Subscriber{Page: page, Confirmed: true}
		if err := rows.Scan(&sub.Email, &sub.Token); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// SubscriberCounts returns confirmed and pending subscriptions per page.
func (s *Store) SubscriberCounts() (map[string][2]int, error) {
	rows, err := s.core.Query(`SELECT page, SUM(confirmed_at IS NOT NULL), SUM(confirmed_at IS NULL) FROM subscribers GROUP BY page`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][2]int{}
	for rows.Next() {
		var page string
		var c, p int
		if err := rows.Scan(&page, &c, &p); err != nil {
			return nil, err
		}
		out[page] = [2]int{c, p}
	}
	return out, rows.Err()
}
