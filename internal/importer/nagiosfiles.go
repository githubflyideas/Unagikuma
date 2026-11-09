package importer

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// NagiosFiles is a Nagios object configuration read from disk.
type NagiosFiles struct {
	Name      string            // what was given, for the report
	Data      []byte            // the object files, joined
	Files     []string          // the files read, in order
	Resources map[string]string // $USERn$ macros from resource files
	Missing   []string          // cfg_file=, cfg_dir= or resource_file= that were not found
	// Parts are the files one by one, so problems name the file and line.
	Parts []NagiosPart
}

// NagiosPart is one object file.
type NagiosPart struct {
	Path string
	Data []byte
}

// Limits for reading a configuration: Nagios setups are big, but not this
// big.
const (
	maxNagiosBytes = 256 << 20
	maxNagiosFiles = 20000
)

// ReadNagiosFiles reads Nagios configuration the way Nagios does: a main
// file (nagios.cfg) has its cfg_file=, cfg_dir= and resource_file= lines
// followed; a directory with a nagios.cfg is read through it; any other
// directory contributes its *.cfg files, recursively; other files are
// object files.
func ReadNagiosFiles(paths []string) (*NagiosFiles, error) {
	r := &nagReader{out: &NagiosFiles{Resources: map[string]string{}}, seen: map[string]bool{}}
	var names []string
	for _, p := range paths {
		names = append(names, filepath.Base(filepath.Clean(p)))
		fi, err := os.Stat(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("%s: no such file or directory", p)
			}
			return nil, err
		}
		if fi.IsDir() {
			if main := filepath.Join(p, "nagios.cfg"); fileExists(main) {
				err = r.main(main)
			} else {
				err = r.dir(p)
			}
		} else if isMainConfig(p) {
			err = r.main(p)
		} else {
			err = r.file(p)
		}
		if err != nil {
			return nil, err
		}
	}
	if len(r.out.Files) == 0 {
		return nil, errors.New("no .cfg files found there")
	}
	r.out.Name = strings.Join(names, ", ")
	return r.out, nil
}

type nagReader struct {
	out   *NagiosFiles
	seen  map[string]bool
	bytes int
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// isMainConfig reports a main configuration file: it has cfg_file= or
// cfg_dir= lines and no object definitions of its own.
func isMainConfig(p string) bool {
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	main, objects := false, false
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(l, "cfg_file=") || strings.HasPrefix(l, "cfg_dir=") || strings.HasPrefix(l, "resource_file="):
			main = true
		case strings.HasPrefix(l, "define"):
			objects = true
		}
	}
	return main && !objects
}

func (r *nagReader) file(p string) error {
	abs, _ := filepath.Abs(p)
	if r.seen[abs] {
		return nil
	}
	r.seen[abs] = true
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	r.bytes += len(b)
	if r.bytes > maxNagiosBytes || len(r.out.Files) >= maxNagiosFiles {
		return fmt.Errorf("the configuration is larger than %d MB or %d files; import it in parts", maxNagiosBytes>>20, maxNagiosFiles)
	}
	r.out.Files = append(r.out.Files, p)
	r.out.Data = append(append(r.out.Data, b...), '\n')
	r.out.Parts = append(r.out.Parts, NagiosPart{Path: p, Data: b})
	return nil
}

func (r *nagReader) dir(p string) error {
	var files []string
	err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".cfg" && !isMainConfig(path) && filepath.Base(path) != "resource.cfg" && filepath.Base(path) != "cgi.cfg" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		if err := r.file(f); err != nil {
			return err
		}
	}
	return nil
}

// main follows a nagios.cfg. Paths in it are usually absolute
// (/etc/nagios4/...): when the configuration was copied elsewhere they
// are looked up relative to the main file as well.
func (r *nagReader) main(p string) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	base := filepath.Dir(p)
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		key, val, ok := strings.Cut(l, "=")
		if !ok || strings.HasPrefix(l, "#") || strings.HasPrefix(l, ";") {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "cfg_file":
			if f := locate(base, val, false); f != "" {
				err = r.file(f)
			} else {
				r.out.Missing = append(r.out.Missing, "cfg_file="+val)
			}
		case "cfg_dir":
			if d := locate(base, val, true); d != "" {
				err = r.dir(d)
			} else {
				r.out.Missing = append(r.out.Missing, "cfg_dir="+val)
			}
		case "resource_file":
			if f := locate(base, val, false); f != "" {
				r.resources(f)
			} else {
				r.out.Missing = append(r.out.Missing, "resource_file="+val)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// locate finds a path named in nagios.cfg: as written, relative to the
// main file, or, for an absolute path from another machine, its last
// parts below the main file's directory or one of its parents. A path
// starting with / counts as absolute on Windows too: the configuration
// usually comes from a Linux machine.
func locate(base, p string, dir bool) string {
	ok := func(x string) bool {
		fi, err := os.Stat(x)
		return err == nil && fi.IsDir() == dir
	}
	if !filepath.IsAbs(p) && !strings.HasPrefix(p, "/") {
		if x := filepath.Join(base, p); ok(x) {
			return x
		}
		return ""
	}
	if ok(p) {
		return p
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(p)), "/")
	for up, a := 0, base; up < 4; up, a = up+1, filepath.Dir(a) {
		for i := 1; i < len(parts); i++ {
			if x := filepath.Join(append([]string{a}, parts[i:]...)...); ok(x) {
				return x
			}
		}
	}
	return ""
}

// resources reads $USERn$=value lines.
func (r *nagReader) resources(p string) {
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(strings.TrimSuffix(l, "\r"))
		if k, v, ok := strings.Cut(l, "="); ok && strings.HasPrefix(k, "$USER") && strings.HasSuffix(k, "$") {
			r.out.Resources[strings.Trim(k, "$")] = strings.TrimSpace(v)
		}
	}
}
