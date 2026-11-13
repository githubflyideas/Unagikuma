// Package backup writes and restores a whole unagikuma installation: the
// databases, the configuration file and, in an archive protected by a passphrase,
// the key that encrypts the secrets and the CA that agents pin.
package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Files that are only included in an encrypted archive.
// idFiles say which installation this is and what it is registered for;
// a restored or moved server keeps both.
var idFiles = []string{"installation", "license.json"}

var keyFiles = []string{"unagikuma.key", "cluster-ca.crt", "cluster-ca.key", "cluster.crt", "cluster.key", "site-identity/cluster-ca.crt", "site-identity/cluster-ca.key", "site-identity/cluster.crt", "site-identity/cluster.key"}

// Backups made before 2.6.11 may carry theme packs under themes/; the
// console no longer has them, so a restore reads past those entries.
var themeEntry = regexp.MustCompile(`^themes/(active|[a-z0-9][a-z0-9-]{0,39}/(theme\.json|logo|favicon))$`)

const maxThemeFile = 1 << 20

// configEntry is the configuration file's name in an archive;
// legacyConfigEntry the one archives before 2.6 used.
const (
	configEntry       = "monitors.conf"
	legacyConfigEntry = "monitors.bind"
)


// DB writes a consistent copy of a database ("core" or "history").
type DB func(ctx context.Context, which string, w io.Writer) error

// Options choose what goes into an archive.
type Options struct {
	DataDir    string
	ConfigPath string // monitors.conf
	History    bool   // include history.db (can be large)
	// Passphrase encrypts the archive and adds the secret key and the
	// cluster CA; without one they are left out.
	Passphrase string
	Instance   string
	Version    string
	Now        time.Time
}

// Manifest describes an archive.
type Manifest struct {
	Format    int       `json:"format"`
	Created   time.Time `json:"created"`
	Instance  string    `json:"instance,omitempty"`
	Version   string    `json:"version,omitempty"`
	Files     []string  `json:"files"`
	Keys      bool      `json:"keys"`
	Encrypted bool      `json:"encrypted"`
}

const format = 1

// Write streams an archive to w.
func Write(ctx context.Context, w io.Writer, db DB, o Options) (Manifest, error) {
	m := Manifest{Format: format, Created: o.Now, Instance: o.Instance, Version: o.Version}
	out := w
	var enc *encWriter
	if o.Passphrase != "" {
		var err error
		if enc, err = newEncWriter(w, o.Passphrase); err != nil {
			return m, err
		}
		out = enc
		m.Encrypted, m.Keys = true, true
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	add := func(name string, mode int64, f func(io.Writer) error) error {
		// Databases are spooled to a temporary file because tar needs
		// the size up front.
		tmp, err := os.CreateTemp(o.DataDir, ".backup-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		if err := f(tmp); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		size, err := tmp.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: size, ModTime: o.Now}); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tmp); err != nil {
			return err
		}
		m.Files = append(m.Files, name)
		return nil
	}
	file := func(path string) func(io.Writer) error {
		return func(w io.Writer) error {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(w, f)
			return err
		}
	}
	if err := add("core.db", 0o600, func(w io.Writer) error { return db(ctx, "core", w) }); err != nil {
		return m, err
	}
	if o.History {
		if err := add("history.db", 0o600, func(w io.Writer) error { return db(ctx, "history", w) }); err != nil {
			return m, err
		}
	}
	if err := add(configEntry, 0o640, file(o.ConfigPath)); err != nil {
		return m, err
	}
	for _, k := range idFiles {
		p := filepath.Join(o.DataDir, k)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := add(k, 0o644, file(p)); err != nil {
			return m, err
		}
	}
	if m.Keys {
		for _, k := range keyFiles {
			p := filepath.Join(o.DataDir, k)
			if _, err := os.Stat(p); err != nil {
				continue // e.g. the key comes from UNAGIKUMA_SECRET_KEY
			}
			if err := add(k, 0o600, file(p)); err != nil {
				return m, err
			}
		}
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o644, Size: int64(len(mb)), ModTime: o.Now}); err != nil {
		return m, err
	}
	if _, err := tw.Write(mb); err != nil {
		return m, err
	}
	if err := tw.Close(); err != nil {
		return m, err
	}
	if err := gz.Close(); err != nil {
		return m, err
	}
	if enc != nil {
		return m, enc.Close()
	}
	return m, nil
}

// Restore unpacks an archive into dataDir (the configuration file to
// configPath). Archives made before 2.6 name it monitors.bind.
// Existing files are replaced only with force. The server must not run.
func Restore(r io.Reader, dataDir, configPath, passphrase string, force bool) (m Manifest, err error) {
	in, err := openArchive(r, passphrase)
	if err != nil {
		return m, err
	}
	gz, err := gzip.NewReader(in)
	if err != nil {
		return m, fmt.Errorf("not a unagikuma backup: %w", err)
	}
	if !force {
		for _, f := range []string{filepath.Join(dataDir, "core.db"), configPath} {
			if _, err := os.Stat(f); err == nil {
				return m, fmt.Errorf("%s exists; stop the server and use --force to replace it", f)
			}
		}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return m, err
	}
	u := &unpacker{dataDir: dataDir, configPath: configPath, m: &m, allowed: map[string]bool{"core.db": true, "history.db": true, configEntry: true, legacyConfigEntry: true, "manifest.json": true}}
	for _, k := range append(keyFiles, idFiles...) {
		u.allowed[k] = true
	}
	defer func() {
		if err != nil {
			u.cleanup()
		}
	}()
	if err := u.unpack(tar.NewReader(gz)); err != nil {
		return m, err
	}
	if m.Format == 0 {
		return m, errors.New("not a unagikuma backup (no manifest)")
	}
	if m.Format > format {
		return m, fmt.Errorf("backup format %d is newer than this unagikuma understands", m.Format)
	}
	// Everything is unpacked and checked: put it in place. Stale WAL
	// files of the replaced databases would otherwise be replayed.
	for _, s := range u.done {
		if strings.HasSuffix(s.dst, ".db") {
			os.Remove(s.dst + "-wal")
			os.Remove(s.dst + "-shm")
		}
		if err := os.Rename(s.tmp, s.dst); err != nil {
			return m, err
		}
	}
	if err := os.WriteFile(filepath.Join(dataDir, ".db-epoch-reset"), []byte("rotate before starting restored database\n"), 0600); err != nil {
		return m, err
	}
	return m, nil
}

// openArchive returns the gzip stream of an archive, decrypting it when it
// is encrypted.
func openArchive(r io.Reader, passphrase string) (io.Reader, error) {
	br := bufio.NewReader(r)
	if head, _ := br.Peek(len(magic)); string(head) != magic && string(head) != string(legacyMagic) {
		return br, nil
	}
	if passphrase == "" {
		return nil, errors.New("this backup is encrypted: a passphrase is needed")
	}
	return newDecReader(br, passphrase)
}

// unpacker writes an archive's files next to where they go; nothing is
// replaced until every entry has been read and checked.
type unpacker struct {
	dataDir, configPath string
	m                   *Manifest
	allowed             map[string]bool
	done                []stagedFile
}

type stagedFile struct{ tmp, dst string }

func (u *unpacker) cleanup() {
	for _, s := range u.done {
		os.Remove(s.tmp)
	}
}

func (u *unpacker) unpack(tr *tar.Reader) error {
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("damaged backup: %w", err)
		}
		if err := u.entry(h, tr); err != nil {
			return err
		}
	}
}

// entry stages one file of the archive, or reads the manifest.
func (u *unpacker) entry(h *tar.Header, tr *tar.Reader) error {
	theme := themeEntry.MatchString(h.Name)
	if (!u.allowed[h.Name] && !theme) || h.Typeflag != tar.TypeReg || (theme && h.Size > maxThemeFile) {
		return fmt.Errorf("unexpected entry %q in backup", h.Name)
	}
	if theme {
		_, err := io.Copy(io.Discard, tr)
		return err
	}
	if h.Name == "manifest.json" {
		if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(u.m); err != nil {
			return fmt.Errorf("manifest: %w", err)
		}
		return nil
	}
	dst := filepath.Join(u.dataDir, filepath.FromSlash(h.Name))
	isConfig := h.Name == configEntry || h.Name == legacyConfigEntry
	if isConfig {
		dst = u.configPath
	}
	if !isConfig && strings.Contains(h.Name, "/") {
		parent := filepath.Dir(dst)
		if fi, err := os.Lstat(parent); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return errors.New("restore identity directory must not be a symlink")
		}
		if err := os.MkdirAll(parent, 0700); err != nil {
			return err
		}
	}
	tmp := dst + ".restore"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(h.Mode)&0o640|0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, tr)
	f.Close()
	u.done = append(u.done, stagedFile{tmp, dst})
	if err != nil {
		return fmt.Errorf("damaged backup: %w", err)
	}
	return nil
}

// Encrypted archives: magic, salt and nonce prefix, then chunks of up to
// chunkSize plaintext bytes, each sealed with AES-256-GCM under a key
// derived from the passphrase with argon2id. Each chunk's nonce carries
// its index, and the last chunk is marked in its additional data, so
// chunks cannot be reordered, dropped or truncated unnoticed.
const (
	magic     = "UKBACKUP1\n"
	chunkSize = 1 << 20
	saltLen   = 16
	prefixLen = 4
)

func deriveKey(pass string, salt []byte) []byte {
	return argon2.IDKey([]byte(pass), salt, 3, 64*1024, 4, 32)
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func chunkNonce(prefix []byte, i uint64) []byte {
	n := make([]byte, 12)
	copy(n, prefix)
	binary.BigEndian.PutUint64(n[4:], i)
	return n
}

func chunkAD(last bool) []byte {
	if last {
		return []byte{1}
	}
	return []byte{0}
}

type encWriter struct {
	w      io.Writer
	aead   cipher.AEAD
	prefix []byte
	buf    bytes.Buffer
	i      uint64
}

func newEncWriter(w io.Writer, pass string) (*encWriter, error) {
	salt := make([]byte, saltLen)
	prefix := make([]byte, prefixLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(prefix); err != nil {
		return nil, err
	}
	aead, err := newAEAD(deriveKey(pass, salt))
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(w, magic); err != nil {
		return nil, err
	}
	if _, err := w.Write(append(salt, prefix...)); err != nil {
		return nil, err
	}
	return &encWriter{w: w, aead: aead, prefix: prefix}, nil
}

func (e *encWriter) Write(p []byte) (int, error) {
	n := len(p)
	e.buf.Write(p)
	for e.buf.Len() > chunkSize {
		if err := e.seal(e.buf.Next(chunkSize), false); err != nil {
			return 0, err
		}
	}
	return n, nil
}

func (e *encWriter) seal(chunk []byte, last bool) error {
	ct := e.aead.Seal(nil, chunkNonce(e.prefix, e.i), chunk, chunkAD(last))
	e.i++
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(ct)))
	if _, err := e.w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := e.w.Write(ct)
	return err
}

// Close seals the remaining bytes as the last chunk (possibly empty).
func (e *encWriter) Close() error { return e.seal(e.buf.Next(e.buf.Len()), true) }

type decReader struct {
	r      io.Reader
	aead   cipher.AEAD
	prefix []byte
	i      uint64
	buf    []byte
	eof    bool
}

func newDecReader(r io.Reader, pass string) (*decReader, error) {
	head := make([]byte, len(magic)+saltLen+prefixLen)
	if _, err := io.ReadFull(r, head); err != nil {
		return nil, errors.New("damaged backup")
	}
	salt := head[len(magic) : len(magic)+saltLen]
	aead, err := newAEAD(deriveKey(pass, salt))
	if err != nil {
		return nil, err
	}
	return &decReader{r: r, aead: aead, prefix: append([]byte(nil), head[len(magic)+saltLen:]...)}, nil
}

func (d *decReader) Read(p []byte) (int, error) {
	for len(d.buf) == 0 {
		if d.eof {
			return 0, io.EOF
		}
		var hdr [4]byte
		if _, err := io.ReadFull(d.r, hdr[:]); err != nil {
			return 0, errors.New("backup is truncated")
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n > chunkSize+uint32(d.aead.Overhead()) {
			return 0, errors.New("damaged backup")
		}
		ct := make([]byte, n)
		if _, err := io.ReadFull(d.r, ct); err != nil {
			return 0, errors.New("backup is truncated")
		}
		nonce := chunkNonce(d.prefix, d.i)
		pt, err := d.aead.Open(nil, nonce, ct, chunkAD(false))
		if err != nil {
			if pt, err = d.aead.Open(nil, nonce, ct, chunkAD(true)); err != nil {
				if d.i == 0 {
					return 0, errors.New("wrong passphrase or damaged backup")
				}
				return 0, errors.New("damaged backup")
			}
			d.eof = true
		}
		d.i++
		d.buf = pt
	}
	n := copy(p, d.buf)
	d.buf = d.buf[n:]
	return n, nil
}

// legacyMagic starts the encrypted archives written before v1.6: the same
// format under an older header. They still restore.
var legacyMagic = []byte{'N', 'K', 'B', 'A', 'C', 'K', 'U', 'P', '1', '\n'}
