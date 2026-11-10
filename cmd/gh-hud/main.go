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
	"github.com/jflowers/gh-hud/internal/fleet"
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
		stateDir   = flag.String("state-dir", "", "dispatch/fleet state directory (default ~/.local/state/gh-hud)")
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
	machines := make([]dispatch.Machine, 0, len(cfg.Machines))
	for _, mc := range cfg.Machines {
		machines = append(machines, dispatch.Machine{
			Name:     mc.Name,
			SSH:      mc.SSH,
			Max:      mc.MaxConcurrent,
			GhwsRoot: mc.GhwsRoot,
			Tasks:    mc.Tasks,
			Enabled:  mc.IsEnabled(),
			Local:    false,
			Mode:     mc.DispatchMode,
		})
	}
	dispOpts := dispatch.Options{
		Root:          cfg.GhwsRoot,
		Agent:         cfg.Agent,
		MaxConcurrent: cfg.MaxConcurrent,
		BusyWindow:    time.Duration(cfg.BusyWindowSecs) * time.Second,
		Dirty:         cfg.Dirty,
		Tasks:         cfg.Dispatch,
		AutoMerge:     cfg.AutoMerge,
		WatchCmd:      cfg.AgentWatchCmd,
		Runner:        cfg.Runner,
		Mode:          cfg.DispatchMode,
		HerdrWatchCmd: cfg.HerdrWatchCmd,
		Dirs:          wsDirs,
		Machines:      machines,
		LocalIndex:    idx,
		InstancesOnly: cfg.DispatchInstancesOnly,
		StateDir:      *stateDir,
		LearningsPR:   cfg.LearningsPR,
	}
	disp, dispErr := dispatch.New(dispOpts)
	if dispErr != nil {
		disp = nil
	} else {
		defer disp.Close()
	}

	var fleetCoord *fleet.Coordinator
	if cfg.Fleet.Enabled {
		fleetMachines := make([]dispatch.Machine, 0, len(cfg.Fleet.Order))
		for _, name := range cfg.Fleet.Order {
			if name == "local" {
				fleetMachines = append(fleetMachines, dispatch.Machine{
					Name: "local", Max: cfg.MaxConcurrent, GhwsRoot: cfg.GhwsRoot, Enabled: true, Local: true,
				})
				continue
			}
			for _, mc := range cfg.Machines {
				if mc.Name != name {
					continue
				}
				fleetMachines = append(fleetMachines, dispatch.Machine{
					Name: mc.Name, SSH: mc.SSH, Max: mc.MaxConcurrent, GhwsRoot: mc.GhwsRoot, Tasks: mc.Tasks, Enabled: true,
				})
				break
			}
		}
		var sched fleet.Scheduler
		if disp != nil {
			sched = disp
		}
		c, err := fleet.New(fleet.Options{Config: cfg.Fleet, Machines: fleetMachines, Scheduler: sched, StateDir: *stateDir})
		if err != nil {
			fmt.Fprintln(os.Stderr, "fleet:", err)
		} else {
			fleetCoord = c
			fleetCtx, fleetCancel := context.WithCancel(context.Background())
			defer fleetCancel()
			// Rollouts drain and update machines: only the gh-hud that owns
			// dispatch runs them, so a second one never competes.
			if owner, _ := ownsDispatch(disp); owner {
				go c.Run(fleetCtx)
			}
		}
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

	p := tea.NewProgram(ui.New(cfg, client, disp, idx, fleetCoord),
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

// ownsDispatch reports whether this gh-hud owns dispatch (true without a
// dispatcher, which has nothing to compete over).
func ownsDispatch(disp *dispatch.Dispatcher) (bool, string) {
	if disp == nil {
		return true, ""
	}
	return disp.Owner()
}
