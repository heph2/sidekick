// Command sidekick orchestrates planner, implementer, reviewer, learner, and
// landing agents around a tmux console workspace.
package main

import (
	"fmt"
	"os"

	"sidekick/internal/cli"
)

func main() {
	if err := cli.Main(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "sidekick: %v\n", err)
		os.Exit(1)
	}
}
