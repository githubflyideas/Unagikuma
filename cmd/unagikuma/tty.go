package main

import (
	"bytes"
	"io"
)

// The command line reads passwords without echo and sizes its tables to
// the terminal. golang.org/x/sys is enough for that (tty_*.go).

// readTTYLine reads up to a newline from r, without the line ending.
func readTTYLine(r io.Reader) ([]byte, error) {
	var line []byte
	var b [1]byte
	for {
		n, err := r.Read(b[:])
		if n == 1 {
			if b[0] == '\n' {
				return bytes.TrimSuffix(line, []byte{'\r'}), nil
			}
			line = append(line, b[0])
		}
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				return line, nil
			}
			return nil, err
		}
	}
}
