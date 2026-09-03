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
		fmt.Fprintf(stdout, "usaged %s (%s)\n", version, commit)
		return 0

	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		fs.SetOutput(stdout)
		// flags will be added by later tasks
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		fmt.Fprintln(stdout, "serve: not implemented")
		return 2

	case "once":
		return runOnce(rest, stdout)

	default:
		fmt.Fprintf(stdout, "unknown subcommand: %s\n", name)
		return 2
	}
}
