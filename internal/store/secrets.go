package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Secrets use envelope encryption: each value is sealed with its own random
// data key (AES-256-GCM), and the data key is sealed with the installation
// key (KEK). Rotating the KEK only rewraps the small data keys.

// LoadOrCreateKey returns the 32-byte KEK: UNAGIKUMA_SECRET_KEY (base64)
// when set, else the key file in dir, created with mode 0600 if missing.
func LoadOrCreateKey(dir string) ([]byte, error) {
	if v := os.Getenv("UNAGIKUMA_SECRET_KEY"); v != "" {
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
		if err != nil || len(k) != 32 {
			return nil, errors.New("UNAGIKUMA_SECRET_KEY must be 32 bytes, base64-encoded")
		}
		return k, nil
	}
	path := filepath.Join(dir, "unagikuma.key")
	if b, err := os.ReadFile(path); err == nil {
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(k) != 32 {
			return nil, fmt.Errorf("%s is not a valid key", path)
		}
		return k, nil
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(k)+"\n"), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

func keyID(kek []byte) string {
	h := sha256.Sum256(kek)
	return hex.EncodeToString(h[:4])
}

func seal(key, plain []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, nil)), nil
}

func unseal(key []byte, sealed string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(b) < gcm.NonceSize() {
		return nil, errors.New("corrupt secret")
	}
	return gcm.Open(nil, b[:gcm.NonceSize()], b[gcm.NonceSize():], nil)
}

// SecretInfo describes a secret without its value.
type SecretInfo struct {
	Name      string    `json:"name"`
	Scopes    []string  `json:"scopes,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Secrets is the encrypted secret store.
type Secrets struct {
	s   *Store
	kek []byte
}

// Secrets binds the secret store to a KEK.
func (s *Store) Secrets(kek []byte) *Secrets { return &Secrets{s: s, kek: kek} }

// Put stores or replaces a secret.
func (x *Secrets) Put(name, value string, scopes []string) error {
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return err
	}
	val, err := seal(dek, []byte(value))
	if err != nil {
		return err
	}
	wrapped, err := seal(x.kek, dek)
	if err != nil {
		return err
	}
	sc, _ := json.Marshal(scopes)
	x.s.wmu.Lock()
	defer x.s.wmu.Unlock()
	_, err = x.s.core.Exec(`INSERT INTO secrets(name,key_id,dek,value,scopes,updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET key_id=excluded.key_id,dek=excluded.dek,value=excluded.value,scopes=excluded.scopes,updated_at=excluded.updated_at`,
		name, keyID(x.kek), wrapped, val, string(sc), ms(x.s.now()))
	return err
}

// Get decrypts a secret.
func (x *Secrets) Get(name string) (string, error) {
	var kid, dek, val string
	err := x.s.core.QueryRow(`SELECT key_id,dek,value FROM secrets WHERE name=?`, name).Scan(&kid, &dek, &val)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if kid != keyID(x.kek) {
		return "", errors.New("secret was encrypted with a different key (unagikuma.key changed?)")
	}
	d, err := unseal(x.kek, dek)
	if err != nil {
		return "", errors.New("cannot unwrap the secret's key")
	}
	v, err := unseal(d, val)
	if err != nil {
		return "", errors.New("cannot decrypt the secret")
	}
	return string(v), nil
}

// Has reports whether a secret exists.
func (x *Secrets) Has(name string) bool {
	var n int
	_ = x.s.core.QueryRow(`SELECT COUNT(*) FROM secrets WHERE name=?`, name).Scan(&n)
	return n > 0
}

// Delete removes a secret.
func (x *Secrets) Delete(name string) error {
	x.s.wmu.Lock()
	defer x.s.wmu.Unlock()
	_, err := x.s.core.Exec(`DELETE FROM secrets WHERE name=?`, name)
	return err
}

// List returns secret names and scopes, never values.
func (x *Secrets) List() ([]SecretInfo, error) {
	rows, err := x.s.core.Query(`SELECT name,scopes,updated_at FROM secrets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SecretInfo{}
	for rows.Next() {
		var si SecretInfo
		var sc sql.NullString
		var up int64
		if err := rows.Scan(&si.Name, &sc, &up); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(sc.String), &si.Scopes)
		si.UpdatedAt = fromMS(up)
		out = append(out, si)
	}
	return out, rows.Err()
}

// Rotate rewraps every data key with a new KEK in one transaction.
func (x *Secrets) Rotate(newKEK []byte) error {
	x.s.wmu.Lock()
	defer x.s.wmu.Unlock()
	tx, err := x.s.core.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT name,dek FROM secrets`)
	if err != nil {
		return err
	}
	type rw struct{ name, dek string }
	var all []rw
	for rows.Next() {
		var r rw
		if err := rows.Scan(&r.name, &r.dek); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	for _, r := range all {
		d, err := unseal(x.kek, r.dek)
		if err != nil {
			return fmt.Errorf("secret %s: cannot unwrap with the current key", r.name)
		}
		wrapped, err := seal(newKEK, d)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE secrets SET dek=?, key_id=? WHERE name=?`, wrapped, keyID(newKEK), r.name); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	x.kek = newKEK
	return nil
}
