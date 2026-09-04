package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"usaged/internal/config"
)

// runConfig implements `usaged config`: loads configuration (with the
// optional --config file, env, and flags) and prints the effective
// merged configuration as indented JSON with all secrets redacted.
func runConfig(args []string, stdout io.Writer) int {
	cfg, err := config.Load(args, os.Getenv)
	if err != nil {
		fmt.Fprintln(stdout, err.Error())
		return 2
	}

	data, err := json.MarshalIndent(cfg.Redacted(), "", "  ")
	if err != nil {
		fmt.Fprintf(stdout, "marshal config: %v\n", err)
		return 2
	}
	fmt.Fprintln(stdout, string(data))
	return 0
}
