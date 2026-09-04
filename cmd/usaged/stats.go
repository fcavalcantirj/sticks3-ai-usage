package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"usaged/internal/config"
	"usaged/internal/stats"
)

// runStats implements the `usaged stats` subcommand: scan local Claude Code
// and Codex transcripts and print today/month totals and the models table.
func runStats(args []string, stdout io.Writer) int {
	cfg, err := config.Load(args, os.Getenv)
	if err != nil {
		fmt.Fprintln(stdout, err.Error())
		return 2
	}

	scCfg := stats.ScanConfig{
		TZ:        cfg.TZ,
		ClaudeDir: cfg.ClaudeDir,
		CodexDir:  cfg.CodexDir,
	}
	if cfg.FixturesDir != "" {
		scCfg.ClaudeDir = filepath.Join(cfg.FixturesDir, "transcripts", "claude")
		scCfg.CodexDir = filepath.Join(cfg.FixturesDir, "transcripts", "codex")
	}

	sc := stats.NewScanner(scCfg.TZ)

	// Incremental scan: load the index from disk so the second run only
	// reads appended bytes; save it back afterwards.
	var index stats.Index
	if cfg.StatsIndexPath != "" {
		index, _ = stats.LoadIndex(cfg.StatsIndexPath)
	}
	report, index, err := sc.Scan(context.Background(), scCfg, index)
	if err != nil {
		fmt.Fprintf(stdout, "stats scan error: %v\n", err)
		return 1
	}
	if cfg.StatsIndexPath != "" {
		_ = stats.SaveIndex(cfg.StatsIndexPath, index)
	}

	printStatsReport(stdout, &report, cfg.TZ)
	return 0
}

// printStatsReport prints today/month totals and a models table.
func printStatsReport(w io.Writer, report *stats.Report, tz *time.Location) {
	if tz == nil {
		tz = time.UTC
	}

	fmt.Fprintf(w, "AI Usage — Local Stats\n")
	fmt.Fprintf(w, "Generated: %s\n", time.Unix(report.GeneratedAt, 0).In(tz).Format("2006-01-02 15:04:05"))
	fmt.Fprintln(w)

	for _, srcName := range []string{"claude_code", "codex"} {
		src, ok := report.Sources[srcName]
		if !ok {
			continue
		}

		label := "Claude Code"
		if srcName == "codex" {
			label = "Codex"
		}

		fmt.Fprintf(w, "%s:\n", label)
		costSuffix := ""
		if src.Partial {
			costSuffix = " (partial)"
		}
		if !src.Billed {
			// Subscription-plan traffic: label as API-equiv, not a bare dollar.
			costSuffix = " API-equiv (Max plan)"
		}
		fmt.Fprintf(w, "  Today: %d tokens (%d reqs), $%.6f%s\n",
			src.Today.Tokens.Total(), src.Today.Requests, src.Today.Cost, costSuffix)
		fmt.Fprintf(w, "  Month: %d tokens (%d reqs), $%.6f%s\n",
			src.Month.Tokens.Total(), src.Month.Requests, src.Month.Cost, costSuffix)
		fmt.Fprintf(w, "  Active days: %d, Peak: %s (%d tokens)\n",
			src.ActiveDays, src.Peak.Date, src.Peak.Tokens)

		if len(src.Models) > 0 {
			fmt.Fprintf(w, "  Models:\n")
			for _, m := range src.Models {
				costStr := fmt.Sprintf("%12.6f $", m.Cost)
				if !src.Partial {
					for _, um := range src.UnpricedModels {
						if m.Model == um {
							costStr = "      — $"
							break
						}
					}
				}
				fmt.Fprintf(w, "    %-30s %10d in  %10d out  %10d tot  %s  %6d reqs\n",
					truncModel(m.Model), m.Tokens.Input, m.Tokens.Output,
					m.Tokens.Total(), costStr, m.Requests)
			}
		}

		// Loud gap: report unpriced models.
		if len(src.UnpricedModels) > 0 {
			display := make([]string, len(src.UnpricedModels))
			for i, m := range src.UnpricedModels {
				if m == "" {
					display[i] = "unknown"
				} else {
					display[i] = m
				}
			}
			fmt.Fprintf(w, "  unpriced: %s\n", strings.Join(display, ", "))
		}
		fmt.Fprintln(w)
	}
}

// truncModel shortens a model name for display.
func truncModel(s string) string {
	if len(s) > 30 {
		return s[:27] + "..."
	}
	return strings.TrimSpace(s)
}
