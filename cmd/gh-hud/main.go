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
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/model"
	"github.com/jflowers/gh-hud/internal/notify"
	"github.com/jflowers/gh-hud/internal/ui"
)

var version = "dev"

func main() {
	os.Exit(run())
}

// run is main with an exit code, so deferred cleanup (the dispatch lock,
// the fleet loop) runs before the process exits.
func run() int {
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
		return 0
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
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
			// The hub runs rollouts, and only when it owns dispatch.
			fleetCoord = c
		}
	}

	if *snapshot != "" {
		var w, h int
		if _, err := fmt.Sscanf(*snapshot, "%dx%d", &w, &h); err != nil || w < 20 || h < 5 {
			fmt.Fprintln(os.Stderr, "snapshot: want WxH, e.g. 160x40")
			return 2
		}
		res := model.FetchAll(context.Background(), client, cfg)
		fmt.Println(ui.Snapshot(cfg, res, w, h))
		return 0
	}

	if *once {
		return runOnce(cfg, client, *widthFlag)
	}

	hopts := hub.Options{
		Config:   cfg,
		Fetch:    func(ctx context.Context) *model.FetchResult { return model.FetchAll(ctx, client, cfg) },
		Notifier: notify.OSAScript{},
	}
	// Leave nil pointers out: a nil *Dispatcher in an interface is not nil.
	if disp != nil {
		hopts.Dispatcher = disp
	}
	if fleetCoord != nil {
		hopts.Fleet = fleetCoord
	}
	h := hub.New(hopts)
	hubCtx, stopHub := context.WithCancel(context.Background())
	defer stopHub()
	go h.Run(hubCtx)

	p := tea.NewProgram(ui.New(cfg, h, disp, idx, fleetCoord),
		tea.WithAltScreen(),
		tea.WithMouseAllMotion(),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		return 1
	}
	return 0
}

func runOnce(cfg *config.Config, client *gh.Client, width int) int {
	res := model.FetchAll(context.Background(), client, cfg)
	fmt.Print(ui.PlainTable(res, cfg, width, time.Now()))
	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "⚠ %s\n", e.Error())
	}
	if res.User == "" {
		return 1
	}
	return 0
}
