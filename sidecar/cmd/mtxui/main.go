// Command mtxui is the MediaMTX UI sidecar: it serves the web UI, decides every MediaMTX authentication, proxies the
// MediaMTX API and owns mediamtx.yml.
//
//	mtxui [serve]              run the server (default)
//	mtxui healthcheck          exit 0 if the running server's internal /healthz answers; used by the image HEALTHCHECK
//	mtxui version              print the version
//	mtxui setup ...            first-run setup without the web wizard (see internal/cli)
//	mtxui credential ...       manage stream credentials (see internal/cli)
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mtxui/internal/app"
	"mtxui/internal/audit"
	"mtxui/internal/auth"
	"mtxui/internal/buildinfo"
	"mtxui/internal/cli"
	"mtxui/internal/credentials"
	"mtxui/internal/live"
	"mtxui/internal/liveproxy"
	"mtxui/internal/logs"
	"mtxui/internal/mtxapi"
	"mtxui/internal/mtxauth"
	"mtxui/internal/mtxconf"
	"mtxui/internal/mtxlog"
	"mtxui/internal/netguard"
	"mtxui/internal/portgate"
	"mtxui/internal/probe"
	"mtxui/internal/proxy"
	"mtxui/internal/settings"
	"mtxui/internal/setup"
	"mtxui/internal/store"
	"mtxui/internal/webui"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	syscall.Umask(0o077) // everything the sidecar creates is private: database, secrets, snapshots
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return serve()
	case "healthcheck":
		return healthcheck()
	case "setup-token": // for `docker compose exec sidecar /mtxui setup-token`: the scratch image has no cat
		cfg, err := settings.Load(os.Getenv)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mtxui: invalid settings:\n%v\n", err)
			return 1
		}
		b, err := os.ReadFile(filepath.Join(cfg.StateDir(), "setup-token"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "mtxui: no setup token: setup is done (or the sidecar has not started yet)")
			return 1
		}
		fmt.Println(strings.TrimSpace(string(b)))
		return 0
	case "version":
		fmt.Printf("mtxui %s (built for MediaMTX %s)\n", buildinfo.Version, buildinfo.MediaMTXVersion)
		return 0
	case "setup", "credential", "reset-password":
		ctx := context.Background()
		cfg, err := settings.Load(os.Getenv)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mtxui: invalid settings:\n%v\n", err)
			return 1
		}
		c, err := open(ctx, cfg, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
		if err != nil {
			fmt.Fprintln(os.Stderr, "mtxui:", err)
			return 1
		}
		defer c.store.Close()
		core := cli.Core{Setup: c.setup, Creds: c.creds, Audit: c.audit, JoinCode: app.IssueJoinCode}
		streams := cli.IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}
		switch cmd {
		case "setup":
			return cli.Setup(ctx, core, args, streams)
		case "reset-password":
			return cli.ResetPassword(ctx, core, args, streams)
		}
		return cli.Credential(ctx, core, args, streams)
	default:
		fmt.Fprintln(os.Stderr, "usage: mtxui [serve|healthcheck|version|setup-token|setup|credential|reset-password]")
		return 2
	}
}

// core holds what both the server and the subcommands need.
type core struct {
	cfg       *settings.Settings
	store     *store.Store
	creds     *credentials.Service
	writer    *mtxconf.Writer
	validator mtxconf.Validator
	setup     *setup.Service
	hasher    *auth.Hasher
	audit     *audit.Recorder
	// subnetSure: cfg.StackSubnet was set, or detected on the only network there is (detectSubnet)
	subnetSure bool
}

func open(ctx context.Context, cfg *settings.Settings, log *slog.Logger) (*core, error) {
	subnetSure := cfg.StackSubnet.IsValid()
	if !subnetSure {
		var n int
		cfg.StackSubnet, n = detectSubnet()
		subnetSure = n == 1
		if n > 1 {
			log.Warn("the sidecar is on more than one network; set MTXUI_STACK_SUBNET to the stack network's subnet "+
				"(taking the first, and leaving MediaMTX's trusted proxies as they are)", "subnet", cfg.StackSubnet)
		}
	}
	for _, dir := range []string{cfg.ConfigDir(), cfg.StateDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return nil, err
	}
	key, err := credentials.LoadKey(cfg.StateDir())
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	c := &core{
		cfg: cfg, store: st, creds: credentials.New(st, key), validator: mtxconf.Validator{Bin: cfg.MediaMTXBin},
		hasher: auth.NewHasher(auth.DefaultParams, 2), audit: audit.NewRecorder(st, log), subnetSure: subnetSure,
	}
	c.writer = &mtxconf.Writer{
		Path: cfg.ConfigFile(), LockPath: filepath.Join(cfg.StateDir(), "config.lock"),
		Rules: mtxconf.Rules{AuthURL: cfg.AuthURL()}, Checker: c.validator, Store: st,
	}
	c.setup = &setup.Service{
		Store: st, Hasher: c.hasher, Writer: c.writer, Seed: c.seed,
		TokenPath: filepath.Join(cfg.StateDir(), "setup-token"),
	}
	return c, nil
}

func (c *core) seed(in setup.Ingest) ([]byte, error) {
	return mtxconf.Seed(mtxconf.SeedParams{
		MediaMTXVersion: buildinfo.MediaMTXVersion, AuthURL: c.cfg.AuthURL(),
		PublicHost: c.cfg.PublicHost, StackSubnet: c.cfg.StackSubnet, RTSP: in.RTSP, RTMP: in.RTMP, SRT: in.SRT,
	})
}

func serve() int {
	cfg, err := settings.Load(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mtxui: invalid settings, refusing to start:\n%v\n", err)
		return 1
	}
	logHub := logs.NewHub() // the log viewer sees the sidecar's own lines too
	log := slog.New(logs.Tee(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}), logHub))
	log.Info("starting", "version", buildinfo.Version, "mediamtx", buildinfo.MediaMTXVersion)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// A restore ends the process after staging the backup; the container's restart policy starts it again.
	ctx, restart := context.WithCancel(ctx)
	defer restart()
	if err := start(ctx, cfg, log, logHub, restart); err != nil {
		log.Error("stopped", "err", err)
		return 1
	}
	log.Info("stopped")
	return 0
}

// start prepares everything MediaMTX needs before the healthcheck turns green (a valid mediamtx.yml, the auth
// endpoint), then serves until ctx ends.
func start(ctx context.Context, cfg *settings.Settings, log *slog.Logger, logHub *logs.Hub, restart func()) error {
	// A restore staged before the last stop swaps the database and credential key in before they are opened.
	staged, err := app.PrepareRestore(cfg, log)
	if err != nil {
		return fmt.Errorf("restoring a backup: %w", err)
	}
	c, err := open(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer c.store.Close()

	version, err := c.validator.Version(ctx)
	if err != nil {
		return fmt.Errorf("the MediaMTX binary used for validation is missing or broken: %w", err)
	}
	if want := "v" + buildinfo.MediaMTXVersion; buildinfo.MediaMTXVersion != "unknown" && version != want {
		return fmt.Errorf("the validation binary is MediaMTX %s, but this sidecar was built for %s", version, want)
	}

	// The offline screen's clips, before the config that may name them is checked.
	if err := app.InstallOfflineClips(cfg.HoldingDir); err != nil {
		log.Warn("installing the offline clips", "dir", cfg.HoldingDir, "err", err)
	}
	app.FinishRestore(ctx, staged, cfg, c.store, c.writer, c.audit, log)
	if err := os.MkdirAll(cfg.BackupsDir(), 0o700); err != nil {
		log.Warn("the backups directory is missing; mount backups/ to make backups", "dir", cfg.BackupsDir(), "err", err)
	}
	locked, err := c.seed(setup.Ingest{})
	if err != nil {
		return err
	}
	outcome, err := c.writer.Reconcile(ctx, locked)
	if err != nil {
		return err
	}
	switch {
	case outcome.Notable:
		log.Warn(outcome.Message)
		c.audit.Record(ctx, store.AuditEvent{
			Actor: "system", Action: "config.startup", Target: "mediamtx.yml",
			Details: map[string]any{"result": outcome.Message},
		})
	case outcome.Message != "":
		log.Info(outcome.Message)
	}

	// Only for a subnet that is surely the stack's: detectSubnet may have taken another network's, and open() said so.
	if c.subnetSure {
		if changed, err := c.writer.SyncTrustedProxies(ctx, cfg.StackSubnet); err != nil {
			log.Warn("setting MediaMTX's trusted proxies to the stack subnet", "subnet", cfg.StackSubnet, "err", err)
		} else if changed {
			log.Info("MediaMTX's trusted proxies now follow the stack subnet", "subnet", cfg.StackSubnet)
		}
	}

	principal, err := mtxauth.NewPrincipal()
	if err != nil {
		return err
	}
	// UI edits are read back from MediaMTX after they are written, and undone if MediaMTX stops answering.
	mtxAPI := &mtxconf.APIClient{Base: cfg.MediaMTXURL(settings.MediaMTXAPIPort), Principal: principal}
	c.writer.Verifier = &mtxconf.Verifier{API: mtxAPI}
	mtxAuth := mtxauth.NewHandler(principal, c.creds, log)
	notes := mtxlog.New() // what MediaMTX's log says about encoders, for the stream pages
	c.writer.MinGap = mtxconf.ReloadGap
	ops, err := mtxapi.Operations()
	if err != nil {
		return err
	}
	apiURL := cfg.MediaMTXURL(settings.MediaMTXAPIPort)
	prox, err := proxy.New(ops, apiURL, principal, app.RoleOf)
	if err != nil {
		return err
	}
	hub, err := live.New(apiURL, principal, ops, log)
	if err != nil {
		return err
	}
	prober := probe.New(apiURL, principal, buildinfo.MediaMTXVersion, cfg.PublicHost, log)
	prober.StackSubnet = cfg.StackSubnet
	sessions := auth.NewSessions(c.store, cfg.Secure(), cfg.SessionIdleTimeout, cfg.SessionMaxAge)
	// Exposure control: the host helper reads desired.json from portgate/ and writes its status into portgate-status/
	// (a read-only mount of /var/lib/mtx-portgate). Without the helper there is no status and the UI says so.
	exposure := &portgate.Client{
		Dir:        filepath.Join(cfg.DataDir, "portgate"),
		StatusPath: filepath.Join(cfg.DataDir, "portgate-status", portgate.StatusFile),
		ManualPath: filepath.Join(cfg.DataDir, "state", "exposure-manual.json"),
	}
	srv := app.New(app.Deps{
		Settings: cfg, Store: c.store, Sessions: sessions, Hasher: c.hasher, Setup: c.setup,
		Probe: prober, Audit: c.audit, Live: hub, Config: c.writer, NetGuard: netguard.New(cfg.StackSubnet), Creds: c.creds, Opened: mtxAuth,
		Kicker: mtxAPI, MTX: mtxAPI, Notes: notes, Logs: logHub,
		Playback: &mtxconf.APIClient{Base: cfg.MediaMTXURL(settings.MediaMTXPlaybackPort), Principal: principal}, Watch: liveproxy.New(cfg.MediaMTXURL(8888), cfg.MediaMTXURL(8889), mtxAuth.Viewers),
		Exposure: exposure, Proxy: prox, MTXAuth: mtxAuth,
		UI: webui.Handler(), Log: log, Restart: restart,
	})

	mtxAuth.Public = srv.IsPublicPath                                             // anyone may read a public stream
	mtxAuth.OnPublish = notes.Publish                                             // ties refused encoders in MediaMTX's log to their stream
	notes.OnRefused = srv.FollowEncoder                                           // the holding version follows the encoder's audio
	notes.OnLine = func(line string) { logHub.Publish(logs.ParseMediaMTX(line)) } // the log viewer's live tail
	go notes.Follow(ctx, cfg.MediaMTXLog(), time.Second)
	go logs.RunRotation(ctx, cfg.MediaMTXLog(), cfg.MediaMTXLogMaxBytes, cfg.MediaMTXLogKeep, time.Minute, func(err error) {
		if err != nil {
			log.Warn("rotating MediaMTX's log", "err", err)
		} else {
			log.Info("rotated MediaMTX's log", "keep", cfg.MediaMTXLogKeep)
		}
	})

	token, err := c.setup.EnsureToken(ctx)
	if err != nil {
		return err
	}
	if token != "" {
		banner(os.Stderr, cfg, token)
	}

	go prober.Run(ctx)
	go c.writer.Watch(ctx, 2*time.Second, locked, func(out mtxconf.Outcome, err error) {
		reportDrift(ctx, c, prober, log, out, err)
	})
	go hub.Run(ctx) // when ctx ends, it closes the event streams, so shutdown need not wait for them
	if cfg.ExposureControl {
		go exposure.Watch(ctx, time.Second, func(portgate.View) { hub.SetExtra("exposure", auth.RoleAdmin, srv.ExposureLive(ctx)) })
		go srv.RunAutoExpose(ctx)
	}
	go srv.RunAccess(ctx)                    // closes live views, guest sessions and the like whose access has ended
	go mtxAuth.RunSessions(ctx, mtxAPI, hub) // disconnects anonymous readers of streams made private
	go srv.RunRecordings(ctx)
	go srv.RunHistory(ctx)
	go srv.RunBackups(ctx)
	go c.creds.StartTouches(ctx)
	go sweep(ctx, sessions, log)
	listeners := app.Listeners{Public: cfg.Listen, Internal: cfg.InternalListen}
	if cfg.TLS == "acme" {
		tc, redirect, err := app.ACME(cfg)
		if err != nil {
			return err
		}
		listeners.PublicTLS, listeners.HTTP, listeners.HTTPHandler = tc, cfg.HTTPListen, redirect
		app.WarmCertificate(ctx, tc, cfg.PublicURL.Hostname(), log)
		log.Info("HTTPS with ACME certificates", "host", cfg.PublicURL.Hostname(), "directory", cfg.ACMEDirectory)
	}
	return app.Run(ctx, listeners, srv.Public(), srv.Internal(), log)
}

// reportDrift logs and audits what the config watch did about an outside edit, and shows it on every page until an
// admin dismisses it.
func reportDrift(ctx context.Context, c *core, prober *probe.Prober, log *slog.Logger, out mtxconf.Outcome, err error) {
	if err != nil {
		log.Error("config check failed", "err", err)
		prober.Warn("config_check", "The sidecar cannot check mediamtx.yml: "+err.Error())
		return
	}
	prober.Clear("config_check")
	if !out.Notable {
		log.Info(out.Message)
		return
	}
	log.Warn(out.Message)
	prober.Warn("config_drift", out.Message+" See the config history.")
	details := map[string]any{"result": out.Message}
	if out.Rejected != nil {
		rejected := out.Rejected
		if len(rejected) > 64<<10 {
			rejected = rejected[:64<<10]
		}
		details["rejected"] = string(rejected)
	}
	c.audit.Record(ctx, store.AuditEvent{Actor: "system", Action: "config.drift", Target: "mediamtx.yml", Details: details})
}

func banner(w io.Writer, cfg *settings.Settings, token string) {
	fmt.Fprintf(w, "\n==================================================================\n"+
		"MediaMTX UI first-run setup\n\n"+
		"Open %s/setup and enter this setup token:\n\n"+
		"    %s\n\n"+
		"Show it again with: docker compose exec sidecar /mtxui setup-token\n"+
		"It is also in %s. It stops working once setup is done.\n"+
		"==================================================================\n\n",
		cfg.Origin(), token, filepath.Join(cfg.StateDir(), "setup-token"))
}

func sweep(ctx context.Context, sessions *auth.Sessions, log *slog.Logger) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := sessions.Sweep(ctx); err != nil {
				log.Warn("session sweep failed", "err", err)
			} else if n > 0 {
				log.Debug("expired sessions removed", "count", n)
			}
		}
	}
}

// detectSubnet returns the prefix of the first non-loopback IPv4 interface, and how many there are. In a compose stack
// with one network, that is the stack network; with more (a reverse proxy's network too), it may be another, and
// MediaMTX starts only after the sidecar, so its address cannot tell.
func detectSubnet() (netip.Prefix, int) {
	var first netip.Prefix
	n := 0
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			if n++; n == 1 {
				addr, _ := netip.AddrFromSlice(ipn.IP.To4())
				ones, _ := ipn.Mask.Size()
				first = netip.PrefixFrom(addr, ones).Masked()
			}
		}
	}
	return first, n
}

func healthcheck() int {
	listen := os.Getenv("MTXUI_INTERNAL_LISTEN")
	if listen == "" {
		listen = ":9081"
	}
	url, err := app.HealthURL(listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// The URL is the sidecar's own internal listener, derived from its own MTXUI_INTERNAL_LISTEN setting.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec // see above
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // see above
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.Status)
		return 1
	}
	return 0
}
