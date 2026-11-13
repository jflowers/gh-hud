// Command gh-hud is a terminal dashboard for GitHub things that need
// attention: review queues, your PRs, issues, failed runs, notifications.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jflowers/gh-hud/internal/api"
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
// the fleet loop, the hub socket) runs before the process exits.
//
// gh-hud runs one hub per state directory, reachable on its socket:
//
//   - gh-hud serve runs the hub alone (for launchd, or a spare terminal).
//   - gh-hud attaches the TUI to the hub on the socket when one answers.
//   - Otherwise gh-hud runs the hub in-process and serves the socket, so a
//     second gh-hud (or another client) attaches to this one. Quitting it
//     stops the hub; attached clients wait for the next one.
func run() int {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "epic" {
		return runEpic(args[1:], os.Stdout, os.Stderr)
	}
	serve := len(args) > 0 && args[0] == "serve"
	if serve {
		args = args[1:]
	}
	var (
		configPath = flag.String("config", config.DefaultPath(), "config file path")
		once       = flag.Bool("once", false, "fetch once and print a table, then exit")
		widthFlag  = flag.Int("width", 120, "table width for --once")
		showVer    = flag.Bool("version", false, "print version and exit")
		snapshot   = flag.String("snapshot", "", "render one frame at WxH to stdout (for screenshots)")
		stateDir   = flag.String("state-dir", "", "dispatch/fleet state directory, and the hub socket's (default ~/.local/state/gh-hud)")
		inProcess  = flag.Bool("no-attach", false, "run a hub in this process even when one is serving (it then only follows)")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: gh-hud [flags]        the dashboard\n       gh-hud serve [flags]  the hub alone, on its socket\n       gh-hud epic …         enroll and manage epics (gh-hud epic for help)\n\nflags:\n")
		flag.PrintDefaults()
	}
	if err := flag.CommandLine.Parse(args); err != nil {
		return 2
	}
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "gh-hud: unexpected argument %q\n", flag.Arg(0))
		flag.Usage()
		return 2
	}

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

	dir, err := resolveStateDir(*stateDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state dir:", err)
		return 1
	}
	sock := filepath.Join(dir, api.SocketName)
	if serve {
		return runServe(cfg, *configPath, client, dir, sock)
	}

	if !*inProcess {
		if c := attach(sock); c != nil {
			return runTUI(cfg, c)
		}
	}
	h, cleanup := buildHub(cfg, *configPath, client, dir)
	defer cleanup()
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan struct{})
	defer func() {
		stop()
		<-served // the socket is removed before we exit
	}()
	go h.Run(ctx)
	go func() {
		defer close(served)
		if *inProcess {
			return
		}
		l, err := api.ListenUnix(sock)
		if err != nil {
			fmt.Fprintln(os.Stderr, "hub socket:", err)
			return
		}
		if err := serveHub(ctx, cfg, h, dir, l, nil); err != nil {
			fmt.Fprintln(os.Stderr, "hub:", err)
		}
	}()
	return runTUI(cfg, h)
}

// serveHub serves h on the unix socket l and, unless [hub] listen is
// empty, on loopback TCP for apps that can't use the socket, until ctx
// ends. A TCP failure is reported but leaves the socket serving.
func serveHub(ctx context.Context, cfg *config.Config, h *hub.Hub, dir string, l net.Listener, tcpReady func(api.Discovery)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tcpDone := make(chan struct{})
	go func() {
		defer close(tcpDone)
		if cfg.Hub.Listen == "" {
			return
		}
		if err := api.ServeTCP(ctx, cfg.Hub.Listen, dir, h, os.Getpid(), tcpReady); err != nil {
			fmt.Fprintln(os.Stderr, "hub tcp:", err)
		}
	}()
	err := api.Serve(ctx, l, h)
	cancel() // the socket is gone: stop TCP too
	<-tcpDone
	return err
}

// resolveStateDir applies the default state directory.
func resolveStateDir(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "gh-hud"), nil
}

// attach returns a client for the hub on sock, or nil when none answers.
func attach(sock string) *api.Client {
	if _, err := os.Stat(sock); err != nil {
		return nil
	}
	c := api.DialUnix(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Fetch(ctx); err != nil {
		return nil
	}
	return c
}

func runTUI(cfg *config.Config, b hub.Backend) int {
	p := tea.NewProgram(ui.New(cfg, b),
		tea.WithAltScreen(),
		tea.WithMouseAllMotion(),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		return 1
	}
	return 0
}

// runServe runs the hub with no TUI until SIGINT or SIGTERM, logging its
// notices and alerts to stderr.
func runServe(cfg *config.Config, cfgPath string, client *gh.Client, dir, sock string) int {
	// Claim the socket first: a second hub must not touch the dispatch lock.
	l, err := api.ListenUnix(sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gh-hud serve:", err)
		return 1
	}
	h, cleanup := buildHub(cfg, cfgPath, client, dir)
	defer cleanup()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go h.Run(ctx)
	go logEvents(ctx, h)
	log.Printf("gh-hud %s: hub serving on %s (pid %d)", version, sock, os.Getpid())
	for _, w := range cfg.Warnings {
		log.Print(w)
	}
	tcpReady := func(d api.Discovery) {
		log.Printf("apps reach the hub at %s (token in %s)", d.URL, filepath.Join(dir, api.DiscoveryName))
	}
	if err := serveHub(ctx, cfg, h, dir, l, tcpReady); err != nil {
		log.Printf("serve: %v", err)
		return 1
	}
	log.Print("gh-hud: hub stopped")
	return 0
}

// logEvents writes the hub's notices and alerts to the log, and says when
// the hub only follows another gh-hud.
func logEvents(ctx context.Context, h *hub.Hub) {
	snaps, events := h.Subscribe(ctx)
	follower := ""
	for {
		select {
		case s, ok := <-snaps:
			if !ok {
				return
			}
			if s.Follower != follower {
				follower = s.Follower
				if follower != "" {
					log.Printf("following gh-hud pid %s, which owns dispatch: this hub won't dispatch, merge or notify", follower)
				} else {
					log.Print("this hub owns dispatch")
				}
			}
		case e, ok := <-events:
			if !ok {
				return
			}
			log.Print(e.Text)
		}
	}
}

// buildHub builds the hub over this machine's dispatcher and fleet (each
// nil when off or failing to start). cleanup releases the dispatch lock.
// buildHub builds the hub, its dispatcher and its fleet from cfg. The hub
// watches cfgPath and applies what it can of each change.
func buildHub(cfg *config.Config, cfgPath string, client *gh.Client, stateDir string) (h *hub.Hub, cleanup func()) {
	cleanup = func() {}
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
	dispOptions := func(cfg *config.Config) dispatch.Options {
		return dispatchOptions(cfg, stateDir, wsDirs, idx)
	}
	dispOpts := dispOptions(cfg)
	disp, dispErr := dispatch.New(dispOpts)
	if dispErr != nil {
		disp = nil
	} else {
		cleanup = func() { disp.Close() }
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
		c, err := fleet.New(fleet.Options{Config: cfg.Fleet, Machines: fleetMachines, Scheduler: sched, StateDir: stateDir})
		if err != nil {
			fmt.Fprintln(os.Stderr, "fleet:", err)
		} else {
			// The hub runs rollouts, and only when it owns dispatch.
			fleetCoord = c
		}
	}

	hopts := hub.Options{
		Config:          cfg,
		ConfigPath:      cfgPath,
		DispatchOptions: dispOptions,
		// The current config, so a reload's orgs and repos apply.
		Fetch:    func(ctx context.Context) *model.FetchResult { return model.FetchAll(ctx, client, h.Config()) },
		Notifier: notify.OSAScript{},
	}
	// Leave nil pointers out: a nil *Dispatcher in an interface is not nil.
	if disp != nil {
		hopts.Dispatcher = disp
	}
	if fleetCoord != nil {
		hopts.Fleet = fleetCoord
	}
	h = hub.New(hopts)
	return h, cleanup
}

// dispatchOptions are the dispatcher's options for cfg; the checkout dirs
// and the ghws index come from startup (a change to them needs a restart).
func dispatchOptions(cfg *config.Config, stateDir string, wsDirs []string, idx *ghws.Index) dispatch.Options {
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
	return dispatch.Options{
		Root:          cfg.GhwsRoot,
		Agent:         cfg.Agent,
		MaxConcurrent: cfg.MaxConcurrent,
		BusyWindow:    time.Duration(cfg.BusyWindowSecs) * time.Second,
		Dirty:         cfg.Dirty,
		Tasks:         cfg.Dispatch,
		AutoMerge:     cfg.AutoMerge,
		Supervisor:    cfg.Supervisor,
		Epics:         cfg.Epics,
		WatchCmd:      cfg.AgentWatchCmd,
		Runner:        cfg.Runner,
		Mode:          cfg.DispatchMode,
		HerdrWatchCmd: cfg.HerdrWatchCmd,
		Dirs:          wsDirs,
		Machines:      machines,
		LocalIndex:    idx,
		InstancesOnly: cfg.DispatchInstancesOnly,
		StateDir:      stateDir,
		LearningsPR:   cfg.LearningsPR,
	}
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
