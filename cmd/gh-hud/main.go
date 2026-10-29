// Command gh-hud is a terminal dashboard for GitHub things that need
// attention: review queues, your PRs, issues, failed runs, notifications.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/gh"
	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
	"github.com/jflowers/gh-hud/internal/ui"
)

var version = "dev"

func main() {
	var (
		configPath = flag.String("config", config.DefaultPath(), "config file path")
		once       = flag.Bool("once", false, "fetch once and print a table, then exit")
		widthFlag  = flag.Int("width", 120, "table width for --once")
		showVer    = flag.Bool("version", false, "print version and exit")
		snapshot   = flag.String("snapshot", "", "render one frame at WxH to stdout (for screenshots)")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("gh-hud", version)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	client := gh.New()

	idx, idxErr := ghws.Discover(cfg.GhwsRoot)
	if idxErr != nil {
		idx = nil
	}
	var wsDirs []string
	if idx != nil {
		for _, co := range idx.Checkouts() {
			wsDirs = append(wsDirs, co.Dir)
		}
	}
	dispOpts := dispatch.Options{
		Root:          cfg.GhwsRoot,
		Agent:         cfg.Agent,
		MaxConcurrent: cfg.MaxConcurrent,
		BusyWindow:    time.Duration(cfg.BusyWindowSecs) * time.Second,
		Dirty:         cfg.Dirty,
		Tasks:         cfg.Dispatch,
		WatchCmd:      cfg.AgentWatchCmd,
		Runner:        cfg.Runner,
		HerdrWatchCmd: cfg.HerdrWatchCmd,
		Dirs:          wsDirs,
	}
	disp, dispErr := dispatch.New(dispOpts)
	if dispErr != nil {
		disp = nil
	}

	if *snapshot != "" {
		var w, h int
		if _, err := fmt.Sscanf(*snapshot, "%dx%d", &w, &h); err != nil || w < 20 || h < 5 {
			fmt.Fprintln(os.Stderr, "snapshot: want WxH, e.g. 160x40")
			os.Exit(2)
		}
		res := model.FetchAll(context.Background(), client, cfg)
		fmt.Println(ui.Snapshot(cfg, client, res, w, h))
		return
	}

	if *once {
		runOnce(cfg, client, *widthFlag)
		return
	}

	p := tea.NewProgram(ui.New(cfg, client, disp, idx),
		tea.WithAltScreen(),
		tea.WithMouseAllMotion(),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
}

func runOnce(cfg *config.Config, client *gh.Client, width int) {
	res := model.FetchAll(context.Background(), client, cfg)
	fmt.Print(ui.PlainTable(res, cfg, width, time.Now()))
	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "⚠ %s\n", e.Error())
	}
	if res.User == "" {
		os.Exit(1)
	}
}
