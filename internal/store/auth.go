package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// ErrNotFound is returned for missing users, sessions and tokens.
var ErrNotFound = errors.New("not found")

// argon2id parameters: interactive logins on small hardware.
const (
	argonTime    = 2
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// HashPassword hashes with argon2id in the PHC string format.
func HashPassword(pw string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// isBcrypt reports a bcrypt hash, as written by Uptime Kuma and many
// other tools: imported accounts keep their passwords.
func isBcrypt(hash string) bool {
	return strings.HasPrefix(hash, "$2a$") || strings.HasPrefix(hash, "$2b$") || strings.HasPrefix(hash, "$2y$")
}

// ValidHash reports whether a stored hash is one CheckPassword verifies.
func ValidHash(hash string) bool {
	if isBcrypt(hash) {
		_, err := bcrypt.Cost([]byte(hash))
		return err == nil
	}
	return strings.HasPrefix(hash, "$argon2id$") && len(strings.Split(hash, "$")) == 6
}

// CheckPassword verifies a password in constant time.
func CheckPassword(hash, pw string) bool {
	if isBcrypt(hash) {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m > 1<<22 || t > 20 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// User is an account.
type User struct {
	ID       int64     `json:"id"`
	Username string    `json:"username"`
	Prefs    string    `json:"-"`     // JSON
	Role     string    `json:"role"`  // admin | operator | viewer
	Areas    []string  `json:"areas"` // $SCOPE sections an operator may edit; empty = all
	Created  time.Time `json:"created_at"`
	Disabled bool      `json:"disabled,omitempty"` // left: cannot log in, its tokens stop working
}

// Roles.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// ValidRole reports whether r is a role.
func ValidRole(r string) bool { return r == RoleAdmin || r == RoleOperator || r == RoleViewer }

func splitAreas(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, "\n")
}

// UserCount returns the number of users (0 = setup needed).
func (s *Store) UserCount() (int, error) {
	var n int
	err := s.core.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser adds an administrator.
func (s *Store) CreateUser(username, password string) (int64, error) {
	return s.CreateUserRole(username, password, RoleAdmin, nil)
}

// ErrUserExists means the username is taken.
var ErrUserExists = errors.New("a user with this name exists")

// CreateUserRole adds an account with a role, optionally limited to some
// configuration sections.
func (s *Store) CreateUserRole(username, password, role string, areas []string) (int64, error) {
	if len(password) < 8 {
		return 0, errors.New("password must be at least 8 characters")
	}
	if !ValidRole(role) {
		return 0, fmt.Errorf("unknown role %q", role)
	}
	if _, err := s.UserID(username); err == nil {
		return 0, ErrUserExists
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	res, err := s.core.Exec(`INSERT INTO users(username,password,created_at,role,areas) VALUES(?,?,?,?,?)`,
		username, HashPassword(password), ms(s.now()), role, strings.Join(areas, "\n"))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CreateUserHash adds an account whose password hash comes from another
// system (bcrypt or argon2id).
func (s *Store) CreateUserHash(username, hash, role string, areas []string) (int64, error) {
	if !ValidHash(hash) {
		return 0, errors.New("unsupported password hash")
	}
	if !ValidRole(role) {
		return 0, fmt.Errorf("unknown role %q", role)
	}
	if _, err := s.UserID(username); err == nil {
		return 0, ErrUserExists
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	res, err := s.core.Exec(`INSERT INTO users(username,password,created_at,role,areas) VALUES(?,?,?,?,?)`,
		username, hash, ms(s.now()), role, strings.Join(areas, "\n"))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Users lists the accounts.
func (s *Store) Users() ([]User, error) {
	rows, err := s.core.Query(`SELECT id,username,role,areas,created_at,disabled FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var areas string
		var created int64
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &areas, &created, &u.Disabled); err != nil {
			return nil, err
		}
		u.Areas, u.Created = splitAreas(areas), fromMS(created)
		out = append(out, u)
	}
	return out, rows.Err()
}

// ErrLastAdmin protects the only remaining administrator.
var ErrLastAdmin = errors.New("the last administrator cannot be removed or demoted")

// SetRole changes a user's role and sections; the last administrator
// keeps the role. Sessions end so the new rights apply at once.
func (s *Store) SetRole(userID int64, role string, areas []string) error {
	if !ValidRole(role) {
		return fmt.Errorf("unknown role %q", role)
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if role != RoleAdmin && s.onlyAdmin(userID) {
		return ErrLastAdmin
	}
	res, err := s.core.Exec(`UPDATE users SET role=?, areas=? WHERE id=?`, role, strings.Join(areas, "\n"), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = s.core.Exec(`DELETE FROM sessions WHERE user_id=?`, userID)
	return err
}

// DeleteUser removes an account, its sessions and its API tokens.
func (s *Store) DeleteUser(userID int64) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.onlyAdmin(userID) {
		return ErrLastAdmin
	}
	if _, err := s.core.Exec(`DELETE FROM tokens WHERE user_id=?`, userID); err != nil {
		return err
	}
	res, err := s.core.Exec(`DELETE FROM users WHERE id=?`, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// onlyAdmin reports whether userID is the one administrator (lock held).
func (s *Store) onlyAdmin(userID int64) bool {
	var role string
	var admins int
	_ = s.core.QueryRow(`SELECT role FROM users WHERE id=?`, userID).Scan(&role)
	_ = s.core.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0`).Scan(&admins)
	return role == RoleAdmin && admins <= 1
}

// UserID looks up an account by name.
func (s *Store) UserID(username string) (int64, error) {
	var id int64
	err := s.core.QueryRow(`SELECT id FROM users WHERE username=?`, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// SetPassword changes a user's password and ends their sessions.
func (s *Store) SetPassword(userID int64, password string) error {
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.core.Exec(`UPDATE users SET password=? WHERE id=?`, HashPassword(password), userID); err != nil {
		return err
	}
	_, err := s.core.Exec(`DELETE FROM sessions WHERE user_id=?`, userID)
	return err
}

// Authenticate checks a username and password.
func (s *Store) Authenticate(username, password string) (*User, error) {
	var u User
	var hash string
	var prefs sql.NullString
	var areas string
	err := s.core.QueryRow(`SELECT id,username,password,prefs,role,areas FROM users WHERE username=? AND disabled=0`, username).Scan(&u.ID, &u.Username, &hash, &prefs, &u.Role, &areas)
	if errors.Is(err, sql.ErrNoRows) {
		// Spend the same time as a real check so usernames cannot be probed.
		CheckPassword(HashPassword("x"), password)
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !CheckPassword(hash, password) {
		return nil, ErrNotFound
	}
	if isBcrypt(hash) {
		// An imported hash: store our own from now on.
		s.wmu.Lock()
		_, _ = s.core.Exec(`UPDATE users SET password=? WHERE id=?`, HashPassword(password), u.ID)
		s.wmu.Unlock()
	}
	u.Prefs, u.Areas = prefs.String, splitAreas(areas)
	return &u, nil
}

// Session is a logged-in browser.
type Session struct {
	User    User
	CSRF    string
	Expires time.Time
}

// CreateSession returns the session id (only its hash is stored).
func (s *Store) CreateSession(userID int64, ttl time.Duration) (id, csrf string, err error) {
	id, csrf = randomString(32), randomString(24)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = s.core.Exec(`INSERT INTO sessions(id_hash,user_id,csrf,created_at,expires_at) VALUES(?,?,?,?,?)`,
		sha(id), userID, csrf, ms(s.now()), ms(s.now().Add(ttl)))
	return id, csrf, err
}

// SessionByID resolves a session cookie.
func (s *Store) SessionByID(id string) (*Session, error) {
	var se Session
	var exp int64
	var prefs sql.NullString
	var areas string
	err := s.core.QueryRow(`SELECT u.id,u.username,u.prefs,u.role,u.areas,s.csrf,s.expires_at FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.id_hash=? AND u.disabled=0`, sha(id)).
		Scan(&se.User.ID, &se.User.Username, &prefs, &se.User.Role, &areas, &se.CSRF, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	se.Expires, se.User.Prefs, se.User.Areas = fromMS(exp), prefs.String, splitAreas(areas)
	if s.now().After(se.Expires) {
		return nil, ErrNotFound
	}
	return &se, nil
}

// DeleteSession logs out.
func (s *Store) DeleteSession(id string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`DELETE FROM sessions WHERE id_hash=?`, sha(id))
	return err
}

// SetPrefs stores a user's UI preferences (JSON).
func (s *Store) SetPrefs(userID int64, prefs string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`UPDATE users SET prefs=? WHERE id=?`, prefs, userID)
	return err
}

// Token is an API token (the plaintext is shown once at creation).
type Token struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Scopes    []string   `json:"scopes"`
	UserID    int64      `json:"-"`
	CreatedAt time.Time  `json:"created_at"`
	LastUsed  *time.Time `json:"last_used,omitempty"`
}

// CreateToken returns the plaintext token "uk_...".
func (s *Store) CreateToken(userID int64, name string, scopes []string) (string, error) {
	plain := "uk_" + randomString(30)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`INSERT INTO tokens(name,prefix,hash,scopes,user_id,created_at) VALUES(?,?,?,?,?,?)`,
		name, plain[:10], sha(plain), strings.Join(scopes, ","), userID, ms(s.now()))
	return plain, err
}

// TokenByPlain resolves a bearer token and records its use.
func (s *Store) TokenByPlain(plain string) (*Token, error) {
	var t Token
	var scopes string
	var created int64
	err := s.core.QueryRow(`SELECT id,name,prefix,scopes,user_id,created_at FROM tokens t WHERE hash=?
		AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id=t.user_id AND u.disabled=1)`, sha(plain)).
		Scan(&t.ID, &t.Name, &t.Prefix, &scopes, &t.UserID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Scopes, t.CreatedAt = strings.Split(scopes, ","), fromMS(created)
	s.wmu.Lock()
	_, _ = s.core.Exec(`UPDATE tokens SET last_used=? WHERE id=?`, ms(s.now()), t.ID)
	s.wmu.Unlock()
	return &t, nil
}

// Tokens lists API tokens.
func (s *Store) Tokens() ([]Token, error) {
	rows, err := s.core.Query(`SELECT id,name,prefix,scopes,user_id,created_at,last_used FROM tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		var t Token
		var scopes string
		var created int64
		var last sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &scopes, &t.UserID, &created, &last); err != nil {
			return nil, err
		}
		t.Scopes, t.CreatedAt = strings.Split(scopes, ","), fromMS(created)
		if last.Valid {
			lu := fromMS(last.Int64)
			t.LastUsed = &lu
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteToken revokes a token.
func (s *Store) DeleteToken(id int64) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`DELETE FROM tokens WHERE id=?`, id)
	return err
}

// DeleteUserTokens revokes every API token of a user and says how many.
func (s *Store) DeleteUserTokens(userID int64) (int, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	res, err := s.core.Exec(`DELETE FROM tokens WHERE user_id=?`, userID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// PushToken returns the push token of a monitor, creating one if needed.
// Only the hash is stored; the plaintext is derived from a per-install
// secret (and a rotation counter) so the UI can show the URL again.
func (s *Store) PushToken(monitor string) (string, error) {
	if plain, _ := s.Setting("push_plain:" + monitor); plain != "" {
		// Brought over from another tool: kept as it was.
		return plain, s.storePushToken(monitor, plain)
	}
	key, err := s.instanceKey()
	if err != nil {
		return "", err
	}
	gen, _ := s.Setting("push_gen:" + monitor)
	plain := "p" + sha(key + "|push|" + monitor + "|" + gen)[:31]
	return plain, s.storePushToken(monitor, plain)
}

func (s *Store) storePushToken(monitor, plain string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`INSERT INTO push_tokens(hash,monitor,created_at) VALUES(?,?,?) ON CONFLICT(monitor) DO UPDATE SET hash=excluded.hash`,
		sha(plain), monitor, ms(s.now()))
	return err
}

var pushTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{6,128}$`)

// SetPushToken gives a monitor a push token chosen elsewhere: the one it
// had in Uptime Kuma, so senders keep working. A token used by another
// monitor is refused.
func (s *Store) SetPushToken(monitor, plain string) error {
	if !pushTokenRe.MatchString(plain) {
		return errors.New("a push token is 6 to 128 letters, digits, - or _")
	}
	if m, err := s.MonitorByPushToken(plain); err == nil && m != monitor {
		return fmt.Errorf("the push token is already used by %s", m)
	}
	if err := s.SetSetting("push_plain:"+monitor, plain); err != nil {
		return err
	}
	return s.storePushToken(monitor, plain)
}

// MonitorByPushToken resolves a push token.
func (s *Store) MonitorByPushToken(plain string) (string, error) {
	var m string
	err := s.core.QueryRow(`SELECT monitor FROM push_tokens WHERE hash=?`, sha(plain)).Scan(&m)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return m, err
}

// RotatePushToken invalidates a monitor's push URL and returns the new token.
func (s *Store) RotatePushToken(monitor string) (string, error) {
	if err := s.DeleteSetting("push_plain:" + monitor); err != nil {
		return "", err
	}
	gen, _ := s.Setting("push_gen:" + monitor)
	n, _ := strconv.Atoi(gen)
	if err := s.SetSetting("push_gen:"+monitor, strconv.Itoa(n+1)); err != nil {
		return "", err
	}
	return s.PushToken(monitor)
}

// instanceKey is a random per-installation secret.
func (s *Store) instanceKey() (string, error) {
	k, err := s.Setting("instance_key")
	if err != nil || k != "" {
		return k, err
	}
	k = randomString(32)
	return k, s.SetSetting("instance_key", k)
}
