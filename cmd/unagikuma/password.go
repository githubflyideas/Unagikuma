package main

import (
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/githubflyideas/unagikuma/internal/store"
)

const resetUsage = `Usage: unagikuma reset-password [--data DIR] [--generate] [--revoke-tokens] [USER]

Sets a new password for USER (default: admin) when it is forgotten.
Run it on the machine where Unagikuma runs; it may keep running.

  --data DIR       the data directory (default: %s)
  --generate       make a strong random password and print it once
  --revoke-tokens  also revoke USER's API tokens (scripts using them stop working)

The new password is typed twice and not shown. From a script, pipe it in:
  echo 'new password' | unagikuma reset-password admin
Everyone signed in as USER is signed out.
`

func runResetPassword(args []string) error {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	data := fs.String("data", defaultDataDir(), "")
	generate := fs.Bool("generate", false, "")
	revoke := fs.Bool("revoke-tokens", false, "")
	if err := fs.Parse(interleavedFlags(args, map[string]bool{"data": true})); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Printf(resetUsage, defaultDataDir())
			return nil
		}
		fmt.Fprintf(os.Stderr, "unagikuma reset-password: %v\n\n"+resetUsage, err, defaultDataDir())
		os.Exit(2)
	}
	if fs.NArg() > 1 {
		fmt.Fprintf(os.Stderr, resetUsage, defaultDataDir())
		os.Exit(2)
	}
	user := "admin"
	if fs.NArg() == 1 {
		user = fs.Arg(0)
	}
	// Never create a new, empty installation by mistake.
	if _, err := os.Stat(filepath.Join(*data, "core.db")); err != nil {
		return fmt.Errorf("no Unagikuma data in %s: pass the data directory with --data DIR", *data)
	}
	st, err := store.Open(*data, store.Options{Tool: true})
	if err != nil {
		return err
	}
	defer st.Close()

	users, err := st.Users()
	if err != nil {
		return err
	}
	var found *store.User
	for i := range users {
		if users[i].Username == user {
			found = &users[i]
		}
	}
	if found == nil && len(users) > 0 {
		names := make([]string, len(users))
		for i, u := range users {
			names[i] = u.Username
		}
		return fmt.Errorf("there is no user %q. Users: %s", user, strings.Join(names, ", "))
	}

	var pw string
	switch {
	case *generate:
		pw = randomPassword()
	default:
		if pw, err = askPassword(os.Stdin, os.Stderr); err != nil {
			return err
		}
	}
	if found == nil {
		// No accounts yet: this one becomes the administrator.
		if _, err := st.CreateUser(user, pw); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "There were no users yet: created administrator %s.\n", user)
	} else {
		if err := st.SetPassword(found.ID, pw); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Password of %s changed. Anyone signed in as %s was signed out.\n", user, user)
		if *revoke {
			n, err := st.DeleteUserTokens(found.ID)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "API tokens of %s revoked: %d.\n", user, n)
		} else if n := userTokens(st, found.ID); n > 0 {
			fmt.Fprintf(os.Stderr, "%s still has %d API token(s); add --revoke-tokens if they may be known to someone else.\n", user, n)
		}
		if found.Disabled {
			fmt.Fprintf(os.Stderr, "Note: %s is disabled, so it still cannot sign in. An administrator can enable it again under Settings.\n", user)
		}
	}
	cliAudit(st, "user.password_reset", user, map[string]any{"revoke_tokens": *revoke, "created": found == nil})
	if *generate {
		fmt.Fprintln(os.Stderr, "New password (shown only now):")
		fmt.Println(pw)
	}
	return nil
}

// askPassword reads a new password: hidden and twice on a terminal, one
// line from a pipe.
func askPassword(in *os.File, out io.Writer) (string, error) {
	fd := int(in.Fd())
	if !term.IsTerminal(fd) {
		pw, err := readLine(in)
		if err == nil && pw == "" {
			err = errors.New("no password given on standard input")
		}
		return pw, err
	}
	for {
		fmt.Fprint(out, "New password (at least 8 characters, not shown): ")
		a, err := term.ReadPassword(fd)
		fmt.Fprintln(out)
		if err != nil {
			return "", err
		}
		if len(a) == 0 {
			return "", errors.New("no password given: nothing was changed")
		}
		if len(a) < 8 {
			fmt.Fprintln(out, "That is too short; use at least 8 characters.")
			continue
		}
		fmt.Fprint(out, "Type it again: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(out)
		if err != nil {
			return "", err
		}
		if string(a) != string(b) {
			fmt.Fprintln(out, "The two passwords differ; try again.")
			continue
		}
		return string(a), nil
	}
}

// randomPassword is 20 characters from an alphabet without look-alikes
// (about 115 bits).
func randomPassword() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, 0, 20)
	buf := make([]byte, 64)
	for len(out) < 20 {
		if _, err := rand.Read(buf); err != nil {
			panic(err)
		}
		for _, c := range buf {
			// Rejection keeps every character equally likely.
			if int(c) < 256-256%len(alphabet) && len(out) < 20 {
				out = append(out, alphabet[int(c)%len(alphabet)])
			}
		}
	}
	return string(out)
}

// interleavedFlags moves flags written after the arguments to the front.
func interleavedFlags(args []string, valued map[string]bool) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			if name := strings.TrimLeft(a, "-"); valued[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		rest = append(rest, a)
	}
	return append(flags, rest...)
}

// userTokens counts a user's API tokens.
func userTokens(st *store.Store, userID int64) int {
	toks, _ := st.Tokens()
	n := 0
	for _, t := range toks {
		if t.UserID == userID {
			n++
		}
	}
	return n
}
