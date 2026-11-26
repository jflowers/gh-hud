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
	"runtime/debug"
	"strings"
	"sync"
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
	"github.com/jflowers/gh-hud/internal/releases"
	"github.com/jflowers/gh-hud/internal/selfrestart"
	"github.com/jflowers/gh-hud/internal/ui"
)

// version is stamped by release builds (-ldflags -X main.version=…). A
// `go install …@v0.3.3`, the way mise installs the hub, stamps nothing, so
// init takes it from the module version the binary was built from.
var version = "dev"

func init() { version = moduleVersion(version) }

// moduleVersion is v without the "v" when it isn't "dev", else the main
// module's version from the build info ("0.3.3"), else v.
func moduleVersion(v string) string {
	if v != "dev" {
		return v
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi.Main.Version == "" || bi.Main.Version == "(devel)" {
		return v
	}
	return strings.TrimPrefix(bi.Main.Version, "v")
}

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
	if len(args) > 0 && args[0] == "branches" {
		return runBranches(args[1:], os.Stdout, os.Stderr)
	}
	if len(args) > 0 && ctlCommands[args[0]] {
		return runCtl(args[0], args[1:], os.Stdout, os.Stderr)
	}
	if len(args) > 0 && args[0] == "toolkit" {
		return runToolkit(args[1:], os.Stdout, os.Stderr)
	}
	if len(args) > 0 && args[0] == "dewey" {
		return runDewey(args[1:], os.Stdout, os.Stderr)
	}
	if len(args) > 0 && args[0] == "workspaces" {
		return runWorkspaces(args[1:], os.Stdout, os.Stderr)
	}
	if len(args) > 0 && args[0] == "checkout" {
		return runCheckout(args[1:], os.Stdout, os.Stderr)
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
		fmt.Fprintf(flag.CommandLine.Output(), "usage: gh-hud [flags]        the dashboard\n       gh-hud serve [flags]  the hub alone, on its socket\n       gh-hud epic …         enroll and manage epics (gh-hud epic for help)\n       gh-hud branches [-apply]  prune branches whose PR is done\n       gh-hud toolkit …      re-scaffold repos with the fleet's tools (gh-hud toolkit for help)\n       gh-hud dewey reindex <machine>|-all  reindex every Dewey vault there now (gh-hud dewey for help)\n       gh-hud workspaces     every fleet checkout's problem and leftovers (gh-hud workspaces -h)\n       gh-hud checkout pull|learnings|save|upkeep|reindex <machine> <checkout>\n                             act on one checkout now (gh-hud checkout for help)\n       gh-hud status|runs|choices|dispatch|queue|transcript|pane …\n                             read and drive the hub, here or on another machine (gh-hud status -h)\n\nflags:\n")
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
	h, cleanup, _ := buildHub(cfg, *configPath, client, dir, false)
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
// empty, on loopback TCP for apps that can't use the socket, on the local
// network when [hub] lan names an address, and on the tailnet when [hub]
// tailnet names a host, until ctx ends. A TCP or
// tailnet failure is reported but leaves the socket serving.
func serveHub(ctx context.Context, cfg *config.Config, h *hub.Hub, dir string, l net.Listener, tcpReady func(api.Discovery)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var others sync.WaitGroup
	if cfg.Hub.Listen != "" {
		others.Go(func() {
			if err := api.ServeTCP(ctx, cfg.Hub.Listen, dir, h, os.Getpid(), tcpReady); err != nil {
				fmt.Fprintln(os.Stderr, "hub tcp:", err)
			}
		})
	}
	if cfg.Hub.LAN != "" {
		others.Go(func() {
			ready := func(i api.LANInfo) {
				log.Printf("other Macs reach the hub at %s with the token in %s (private networks only)", strings.Join(i.URLs, " or "), i.TokenFile)
				if i.AgentTokenFile != "" {
					log.Printf("agents reach it there with the agent token in %s (no approvals or gates)", i.AgentTokenFile)
				}
			}
			var opts []api.LANOption
			if cfg.Hub.AgentToken {
				opts = append(opts, api.WithAgentToken(log.Printf))
			}
			if err := api.ServeLAN(ctx, cfg.Hub.LAN, dir, h, ready, opts...); err != nil {
				fmt.Fprintln(os.Stderr, "hub lan:", err)
			}
		})
	}
	if cfg.Hub.Tailnet != "" {
		others.Go(func() {
			logf := func(format string, args ...any) { log.Printf("tailnet: "+format, args...) }
			ready := func(url string) { log.Printf("the phone reaches the hub at %s (your tailnet devices only)", url) }
			if err := api.ServeTailnet(ctx, cfg.Hub.Tailnet, dir, h, logf, ready); err != nil {
				fmt.Fprintln(os.Stderr, "hub tailnet:", err)
			}
		})
	}
	err := api.Serve(ctx, l, h)
	cancel() // the socket is gone: stop the others too
	others.Wait()
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
// notices and alerts to stderr. When its binary is replaced it restarts
// itself into the new one, in place (the same pid and terminal pane).
func runServe(cfg *config.Config, cfgPath string, client *gh.Client, dir, sock string) int {
	// Claim the socket first: a second hub must not touch the dispatch lock.
	l, err := api.ListenUnix(sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gh-hud serve:", err)
		return 1
	}
	selfRestart := cfg.Hub.Restarts()
	h, cleanup, quiet := buildHub(cfg, cfgPath, client, dir, selfRestart)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go h.Run(ctx)
	go logEvents(ctx, h)
	log.Printf("gh-hud %s: hub serving on %s (pid %d)", version, sock, os.Getpid())
	for _, w := range cfg.Warnings {
		log.Print(w)
	}
	restart := make(chan string, 1) // the binary to exec, once the hub has stopped
	watch := selfrestart.New()
	if selfRestart {
		go func() {
			o := selfrestart.Options{Watch: watch, Logf: log.Printf}
			for {
				path := selfrestart.Wait(ctx, o)
				if path == "" {
					return
				}
				quiet(ctx) // held until exec: no rollout step starts meanwhile
				if ctx.Err() != nil {
					return
				}
				// Waiting for idle can take hours: look again, since a later
				// rollout may have replaced path and removed it.
				if path = selfrestart.Recheck(ctx, o, path); path == "" {
					if ctx.Err() != nil {
						return
					}
					continue
				}
				log.Printf("restarting into %s", path)
				restart <- path
				stop()
				return
			}
		}()
	}
	tcpReady := func(d api.Discovery) {
		log.Printf("apps reach the hub at %s (token in %s)", d.URL, filepath.Join(dir, api.DiscoveryName))
	}
	err = serveHub(ctx, cfg, h, dir, l, tcpReady)
	cleanup()
	if err != nil {
		log.Printf("serve: %v", err)
		return 1
	}
	select {
	case restartTo := <-restart:
		// The socket and the dispatch lock are released: the new binary
		// claims them as a fresh start would.
		err := syscall.Exec(restartTo, append([]string{restartTo}, os.Args[1:]...), os.Environ()) //nolint:gosec // gh-hud from PATH, just verified to run
		log.Printf("restart into %s failed: %v", restartTo, err)
		// The hub has stopped already: bring one back from whatever gh-hud
		// is still there rather than leave the fleet without a hub.
		for _, p := range watch.Fallbacks(restartTo) {
			log.Printf("restarting into %s instead", p)
			err = syscall.Exec(p, append([]string{p}, os.Args[1:]...), os.Environ()) //nolint:gosec // gh-hud from PATH or the running binary
			log.Printf("restart into %s failed: %v", p, err)
		}
		log.Print("no gh-hud to restart into; start gh-hud serve again")
		return 1
	default:
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
//
// quiet blocks until no fleet machine is applying and no toolkit step is
// running, then keeps the next toolkit step from starting: a hub about to
// restart itself calls it last.
func buildHub(cfg *config.Config, cfgPath string, client *gh.Client, stateDir string, selfRestart bool) (h *hub.Hub, cleanup func(), quiet func(context.Context)) {
	cleanup = func() {}
	idx, idxErr := ghws.Discover(cfg.GhwsRoot)
	if idxErr != nil {
		idx = nil
	}
	// Only the fleet's checkouts are gh-hud's (#157); the toolkit finds
	// its canonical clones with its own lookup.
	idx = idx.Fleet(cfg.Orgs)
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
		var dewey fleet.Dewey
		if disp != nil {
			sched, dewey = disp, disp
		}
		c, err := fleet.New(fleet.Options{Config: cfg.Fleet, Machines: fleetMachines, Scheduler: sched, Dewey: dewey, StateDir: stateDir, SelfRestart: selfRestart})
		if err != nil {
			fmt.Fprintln(os.Stderr, "fleet:", err)
		} else {
			// The hub runs rollouts, and only when it owns dispatch.
			fleetCoord = c
		}
	}

	tk := buildToolkit(cfg, stateDir)

	hopts := hub.Options{
		Config:          cfg,
		ConfigPath:      cfgPath,
		DispatchOptions: dispOptions,
		// The current config, so a reload's orgs and repos apply.
		Fetch: func(ctx context.Context) *model.FetchResult { return model.FetchAll(ctx, client, h.Config()) },
		Lookup: func(ctx context.Context, ref string) (model.Item, error) {
			return model.FetchItem(ctx, client, h.Config(), ref)
		},
		Notifier: notify.OSAScript{},
	}
	// Leave nil pointers out: a nil *Dispatcher in an interface is not nil.
	if disp != nil {
		hopts.Dispatcher = disp
	}
	if fleetCoord != nil {
		hopts.Fleet = fleetCoord
	}
	if tk != nil {
		hopts.Toolkit = tk
	}
	if rw := buildReleases(cfg.Releases, client); rw != nil {
		hopts.Releases = rw
	}
	h = hub.New(hopts)
	quiet = func(ctx context.Context) {
		for fleetCoord != nil && fleetCoord.Busy() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
		if tk != nil {
			tk.Hold() // never released: the process execs or exits
		}
	}
	return h, cleanup, quiet
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

			Labels:        mc.Labels,
			ExcludeLabels: mc.ExcludeLabels,

			RemoveUnmanaged: mc.RemoveUnmanaged,
			Keep:            mc.Keep,
			NoAgents:        !mc.RunsAgents(),
		})
	}
	return dispatch.Options{
		Root:                cfg.GhwsRoot,
		Agent:               cfg.Agent,
		MaxConcurrent:       cfg.MaxConcurrent,
		BusyWindow:          time.Duration(cfg.BusyWindowSecs) * time.Second,
		Dirty:               cfg.Dirty,
		Tasks:               cfg.Dispatch,
		AutoMerge:           cfg.AutoMerge,
		Supervisor:          cfg.Supervisor,
		Epics:               cfg.Epics,
		Branches:            cfg.Branches,
		Upkeep:              cfg.Upkeep.Mode,
		KeepBackups:         time.Duration(cfg.Upkeep.KeepBackups) * 24 * time.Hour,
		WatchCmd:            cfg.AgentWatchCmd,
		Runner:              cfg.Runner,
		Mode:                cfg.DispatchMode,
		HerdrWatchCmd:       cfg.HerdrWatchCmd,
		Dirs:                wsDirs,
		Machines:            machines,
		LocalLabels:         cfg.Labels,
		LocalExcludeLabels:  cfg.ExcludeLabels,
		LocalIndex:          idx,
		Orgs:                cfg.Orgs,
		InstancesOnly:       cfg.DispatchInstancesOnly,
		StateDir:            stateDir,
		LearningsPR:         cfg.LearningsPR,
		HumanGate:           cfg.HumanGateLabel,
		DeweyReindex:        cfg.DeweyReindex,
		DeweyReindexTimeout: cfg.DeweyReindexTimeout,
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

// buildReleases builds the Releases watcher, or returns nil when
// [releases] names no manifest. A poll that isn't a duration falls back to
// the default.
func buildReleases(rc config.ReleasesConfig, api releases.API) *releases.Watcher {
	if strings.TrimSpace(rc.Manifest) == "" {
		return nil
	}
	every, err := time.ParseDuration(rc.Poll)
	if err != nil || every < time.Minute {
		every = releases.DefaultEvery
	}
	return releases.New(releases.Options{Manifest: strings.TrimSpace(rc.Manifest), Every: every, API: api})
}
