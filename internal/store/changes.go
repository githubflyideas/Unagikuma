package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Change states.
const (
	ChangePlanned    = "planned"
	ChangeRunning    = "running"
	ChangeDone       = "done"
	ChangeRolledBack = "rolled_back"
	ChangeCancelled  = "cancelled"
)

// Change is announced planned work: when, what it affects, who is told.
type Change struct {
	ID             int64     `json:"id"`
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	Impact         string    `json:"impact"`
	Select         string    `json:"select"`   // monitors held in maintenance during the window
	Policy         string    `json:"policy"`   // notified through this policy...
	Channels       []string  `json:"channels"` // ...or these channels
	StatusPage     string    `json:"status_page,omitempty"`
	AnnouncementID int64     `json:"announcement_id,omitempty"`
	Start          time.Time `json:"start"`
	End            time.Time `json:"end"`
	State          string    `json:"state"`
	Note           string    `json:"note,omitempty"` // the completion or rollback receipt
	CreatedBy      string    `json:"created_by"`
	Created        time.Time `json:"created_at"`
	Updated        time.Time `json:"updated_at"`
	Finished       time.Time `json:"finished_at,omitzero"`
	FinishedBy     string    `json:"finished_by,omitempty"`
	Sent           []string  `json:"sent"` // notices already sent (announced, remind-1h, started...)
}

// Open reports whether the change is still to happen or happening.
func (c Change) Open() bool { return c.State == ChangePlanned || c.State == ChangeRunning }

// HasSent reports whether a notice went out already.
func (c Change) HasSent(key string) bool {
	for _, k := range c.Sent {
		if k == key {
			return true
		}
	}
	return false
}

const changeCols = `id,title,description,impact,sel,policy,channels,status_page,announcement_id,start_at,end_at,state,note,
	created_by,created_at,updated_at,finished_at,finished_by,sent`

func scanChange(sc interface{ Scan(...any) error }) (Change, error) {
	var c Change
	var chans, sent string
	var start, end, created, updated, finished int64
	err := sc.Scan(&c.ID, &c.Title, &c.Description, &c.Impact, &c.Select, &c.Policy, &chans, &c.StatusPage, &c.AnnouncementID,
		&start, &end, &c.State, &c.Note, &c.CreatedBy, &created, &updated, &finished, &c.FinishedBy, &sent)
	c.Channels, c.Sent = splitCSV(chans), splitCSV(sent)
	c.Start, c.End, c.Created, c.Updated, c.Finished = fromMS(start), fromMS(end), fromMS(created), fromMS(updated), fromMS(finished)
	return c, err
}

func splitCSV(s string) []string {
	out := []string{}
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// SaveChange creates (ID 0) or updates the editable fields of a change.
func (s *Store) SaveChange(c Change) (Change, error) {
	if strings.TrimSpace(c.Title) == "" {
		return c, errors.New("a change needs a title")
	}
	if !c.End.After(c.Start) {
		return c, errors.New("the end must be after the start")
	}
	now := ms(s.now())
	s.wmu.Lock()
	if c.ID == 0 {
		if c.State == "" {
			c.State = ChangePlanned
		}
		res, err := s.core.Exec(`INSERT INTO changes(title,description,impact,sel,policy,channels,status_page,announcement_id,start_at,end_at,state,
			created_by,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			c.Title, c.Description, c.Impact, c.Select, c.Policy, strings.Join(c.Channels, ","), c.StatusPage, c.AnnouncementID,
			ms(c.Start), ms(c.End), c.State, c.CreatedBy, now, now)
		s.wmu.Unlock()
		if err != nil {
			return c, err
		}
		c.ID, _ = res.LastInsertId()
	} else {
		res, err := s.core.Exec(`UPDATE changes SET title=?,description=?,impact=?,sel=?,policy=?,channels=?,status_page=?,announcement_id=?,
			start_at=?,end_at=?,updated_at=? WHERE id=?`,
			c.Title, c.Description, c.Impact, c.Select, c.Policy, strings.Join(c.Channels, ","), c.StatusPage, c.AnnouncementID,
			ms(c.Start), ms(c.End), now, c.ID)
		s.wmu.Unlock()
		if err != nil {
			return c, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return c, ErrNotFound
		}
	}
	return s.Change(c.ID)
}

// Change returns one change.
func (s *Store) Change(id int64) (Change, error) {
	c, err := scanChange(s.core.QueryRow(`SELECT `+changeCols+` FROM changes WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// SetChangeState moves a change to a new state (with the receipt note
// when it finishes).
func (s *Store) SetChangeState(id int64, state, note, by string) (Change, error) {
	now := ms(s.now())
	finished := int64(0)
	switch state {
	case ChangeDone, ChangeRolledBack, ChangeCancelled:
		finished = now
	case ChangePlanned, ChangeRunning:
	default:
		return Change{}, fmt.Errorf("unknown state %q", state)
	}
	s.wmu.Lock()
	res, err := s.core.Exec(`UPDATE changes SET state=?, note=?, finished_at=?, finished_by=?, updated_at=? WHERE id=?`, state, note, finished, by, now, id)
	s.wmu.Unlock()
	if err != nil {
		return Change{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Change{}, ErrNotFound
	}
	return s.Change(id)
}

// MarkChangeSent records a notice as sent; false when it already was
// (another tick got there first).
func (s *Store) MarkChangeSent(id int64, key string) (bool, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	var sent string
	if err := s.core.QueryRow(`SELECT sent FROM changes WHERE id=?`, id).Scan(&sent); err != nil {
		return false, err
	}
	for _, k := range splitCSV(sent) {
		if k == key {
			return false, nil
		}
	}
	list := append(splitCSV(sent), key)
	_, err := s.core.Exec(`UPDATE changes SET sent=? WHERE id=?`, strings.Join(list, ","), id)
	return err == nil, err
}

// ResetChangeReminders forgets the reminders sent, after the window moved.
func (s *Store) ResetChangeReminders(id int64) error {
	c, err := s.Change(id)
	if err != nil {
		return err
	}
	var keep []string
	for _, k := range c.Sent {
		if !strings.HasPrefix(k, "remind-") && k != "started" && k != "overdue" {
			keep = append(keep, k)
		}
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = s.core.Exec(`UPDATE changes SET sent=? WHERE id=?`, strings.Join(keep, ","), id)
	return err
}

// ChangeFilter selects changes for the history.
type ChangeFilter struct {
	Query    string    // words in the title, description, impact or note
	State    string    // "open" or a state; "" = all
	From, To time.Time // window overlapping [From, To)
	Limit    int
}

// Changes lists changes, the newest start first.
func (s *Store) Changes(f ChangeFilter) ([]Change, error) {
	var where []string
	var args []any
	switch f.State {
	case "":
	case "open":
		where = append(where, "state IN ('planned','running')")
	default:
		where, args = append(where, "state=?"), append(args, f.State)
	}
	if !f.From.IsZero() {
		where, args = append(where, "end_at>=?"), append(args, ms(f.From))
	}
	if !f.To.IsZero() {
		where, args = append(where, "start_at<?"), append(args, ms(f.To))
	}
	for _, w := range strings.Fields(f.Query) {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(w) + "%"
		where = append(where, `(title LIKE ? ESCAPE '\' OR description LIKE ? ESCAPE '\' OR impact LIKE ? ESCAPE '\' OR note LIKE ? ESCAPE '\' OR created_by LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like, like, like)
	}
	q := `SELECT ` + changeCols + ` FROM changes`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY start_at DESC LIMIT %d", limitOf(f.Limit))
	rows, err := s.core.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Change{}
	for rows.Next() {
		c, err := scanChange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
