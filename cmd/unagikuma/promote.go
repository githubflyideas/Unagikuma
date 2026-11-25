package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/githubflyideas/unagikuma/internal/app"
)

// runPromote prepares a stopped standby to start as the master, for when
// the master is gone and the standby's web page is not at hand.
func runPromote(args []string) error {
	fs := flag.NewFlagSet("promote", flag.ExitOnError)
	data := fs.String("data", defaultDataDir(), "data directory of the standby")
	_ = fs.Parse(args)
	term, st, err := app.PromoteOffline(*data, time.Now())
	if err != nil {
		return err
	}
	cliAuditDir(*data, "cluster.promote", "", map[string]any{"term": term})
	fmt.Printf("At its next start this server takes over as master (term %d) with the master's state copied %s.\n",
		term, st.Fetched.Local().Format("2006-01-02 15:04:05"))
	fmt.Println("Start it now. Nodes move to it once they cannot reach the old master; keep the old master stopped:")
	fmt.Println("if it comes back it learns the newer term and relinquishes configuration authority; local checks continue.")
	return nil
}
