package main

import "strings"

// peelServiceName takes --service-name NAME (or --service-name=NAME) out
// of args so the remaining flags can be parsed as usual. "service install
// --name" inserts this flag so a Windows service can call svc.Run with
// the same name it was registered under.
func peelServiceName(args []string) (name string, rest []string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--service-name" && i+1 < len(args):
			name = args[i+1]
			i++
		case strings.HasPrefix(a, "--service-name="):
			name = strings.TrimPrefix(a, "--service-name=")
		default:
			rest = append(rest, a)
		}
	}
	return name, rest
}
