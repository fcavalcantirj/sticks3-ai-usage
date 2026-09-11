package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

// run dispatches subcommands. Returns the process exit code.
// Extracted from main() so tests can call it in-process without os.Exit.
func run(args []string, stdout io.Writer) int {
	var name string
	var rest []string
	if len(args) > 0 {
		name = args[0]
		rest = args[1:]
	} else {
		name = "serve"
	}

	switch name {
	case "version":
		fs := flag.NewFlagSet("version", flag.ContinueOnError)
		fs.SetOutput(stdout)
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		fmt.Fprintf(stdout, "ai-usage %s (%s)\n", version, commit)
		return 0

	case "serve":
		return runServe(rest, stdout)

	case "once":
		return runOnce(rest, stdout)

	case "stats":
		return runStats(rest, stdout)

	case "config":
		return runConfig(rest, stdout)

	default:
		fmt.Fprintf(stdout, "unknown subcommand: %s\n", name)
		return 2
	}
}
