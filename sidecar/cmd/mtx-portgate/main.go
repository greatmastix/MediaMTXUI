// Command mtx-portgate is the host helper for exposure control. It runs as root, from systemd: on every
// change of the sidecar's desired.json (mtx-portgate.path) and every minute (mtx-portgate.timer, which also enforces
// expiry when the sidecar is gone). It opens only the ports named in its root-owned policy, within the policy's limits,
// and adds and deletes only firewall rules marked mtx-portgate:.
//
//	mtx-portgate [-policy /etc/mtx-portgate/policy.json] apply|reconcile   converge to the desired state
//	mtx-portgate close-all                                              close everything now (needs no sidecar)
//	mtx-portgate status                                                 print the last status
//	mtx-portgate check-policy                                           validate the policy file
//	mtx-portgate -every 1s apply                                        loop, for test stacks without systemd
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"mtxui/internal/portgate"
)

func main() {
	policyPath := flag.String("policy", "/etc/mtx-portgate/policy.json", "the policy file (root-owned)")
	every := flag.Duration("every", 0, "repeat apply at this interval instead of running once (test stacks)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: mtx-portgate [-policy file] apply|reconcile|close-all|status|check-policy")
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	for *every > 0 && flag.Arg(0) == "apply" {
		if err := run("apply", *policyPath); err != nil {
			fmt.Fprintln(os.Stderr, "mtx-portgate:", err)
		}
		time.Sleep(*every)
	}
	if err := run(flag.Arg(0), *policyPath); err != nil {
		fmt.Fprintln(os.Stderr, "mtx-portgate:", err)
		os.Exit(1)
	}
}

func run(cmd, policyPath string) error {
	pol, err := portgate.LoadPolicy(policyPath)
	if err != nil {
		return err
	}
	if cmd == "check-policy" {
		fmt.Printf("policy ok: driver %s, %d ports\n", pol.Driver, len(pol.Ports))
		return nil
	}
	if cmd == "status" {
		b, err := os.ReadFile(filepath.Join(pol.StateDir, portgate.StatusFile))
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(b)
		return err
	}
	if pol.Driver == "ufw" && os.Geteuid() != 0 {
		return errors.New("the ufw driver needs root")
	}
	var driver portgate.Driver
	switch pol.Driver {
	case "ufw":
		driver = &portgate.UFW{Run: portgate.ExecRunner, Bin: "/usr/sbin/ufw"}
	case "dry-run":
		driver = &portgate.UFW{Run: (&portgate.FakeUFW{File: pol.DryRunFile}).Run}
	default:
		driver = portgate.NoDriver{}
	}
	h := &portgate.Helper{
		Policy: pol,
		Driver: driver,
		Run:    portgate.ExecRunner,
		Log:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{})),
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	timeout := time.Minute
	if pol.Verify != nil {
		timeout += time.Duration(pol.Verify.Timeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var st portgate.Status
	switch cmd {
	case "apply", "reconcile":
		st, err = h.Apply(ctx)
	case "close-all":
		st, err = h.CloseAll(ctx)
	default:
		return fmt.Errorf("unknown command %q (apply, reconcile, close-all, status, check-policy)", cmd)
	}
	if err != nil {
		return err
	}
	if cmd == "close-all" { // by hand: show what is left; the timer's runs log only changes
		out, _ := json.MarshalIndent(st, "", "  ")
		fmt.Println(string(out))
	}
	if st.Error != "" {
		return errors.New(st.Error)
	}
	return nil
}
