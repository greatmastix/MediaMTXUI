package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mtxui/internal/audit"
	"mtxui/internal/auth"
	"mtxui/internal/credentials"
	"mtxui/internal/mtxconf"
	"mtxui/internal/setup"
	"mtxui/internal/store"
)

type okChecker struct{}

func (okChecker) Validate(context.Context, []byte) error { return nil }

func newCore(t *testing.T) (Core, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key, _ := credentials.LoadKey(dir)
	_ = os.Mkdir(filepath.Join(dir, "config"), 0o750)
	const authURL = "http://sidecar:9081/internal/auth"
	w := &mtxconf.Writer{
		Path: filepath.Join(dir, "config", "mediamtx.yml"), LockPath: filepath.Join(dir, "lock"),
		Rules: mtxconf.Rules{AuthURL: authURL}, Checker: okChecker{}, Store: st,
	}
	s := &setup.Service{
		Store: st, Hasher: auth.NewHasher(auth.Params{Memory: 8 * 1024, Time: 1, Threads: 1}, 1), Writer: w,
		TokenPath: filepath.Join(dir, "setup-token"),
		Seed: func(in setup.Ingest) ([]byte, error) {
			return mtxconf.Seed(mtxconf.SeedParams{MediaMTXVersion: "1.21.1", AuthURL: authURL, PublicHost: "h", RTSP: in.RTSP})
		},
	}
	return Core{Setup: s, Creds: credentials.New(st, key), Audit: audit.NewRecorder(st, slog.New(slog.NewTextHandler(io.Discard, nil)))}, st
}

func run(f func(context.Context, Core, []string, IO) int, c Core, stdin string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := f(context.Background(), c, args, IO{In: strings.NewReader(stdin), Out: &out, Err: &errb})
	return code, out.String(), errb.String()
}

func TestSetup(t *testing.T) {
	c, st := newCore(t)
	if code, _, _ := run(Setup, c, "correct horse battery\n"); code != exitUsage {
		t.Errorf("no username: %d", code)
	}
	if code, _, _ := run(Setup, c, "correct horse battery\n", "--username", "admin", "--ingest", "rtsp,webrtc"); code != exitUsage {
		t.Errorf("unknown protocol: %d", code)
	}
	if code, _, _ := run(Setup, c, "", "--username", "admin"); code != exitUsage {
		t.Errorf("no password: %d", code)
	}
	if code, _, errs := run(Setup, c, "short\n", "--username", "admin"); code != exitFailed || !strings.Contains(errs, "12 characters") {
		t.Errorf("weak password: %d %q", code, errs)
	}
	code, out, _ := run(Setup, c, "correct horse battery\n", "--username", "admin", "--ingest", "rtsp")
	if code != exitOK || !strings.Contains(out, `admin "admin" created`) {
		t.Fatalf("setup: %d %q", code, out)
	}
	if code, _, _ := run(Setup, c, "correct horse battery\n", "--username", "other"); code != exitAlready {
		t.Errorf("second setup: %d, want %d", code, exitAlready)
	}
	events, _ := st.ListAudit(context.Background(), 5)
	if len(events) != 1 || events[0].Actor != "cli" || events[0].Action != "setup.complete" {
		t.Errorf("audit: %+v", events)
	}
}

func TestCredentials(t *testing.T) {
	c, st := newCore(t)
	code, out, errs := run(Credential, c, "", "add", "--name", "cam1", "--actions", "publish,read", "--paths", "cam1", "--json")
	if code != exitOK {
		t.Fatalf("add: %d %s", code, errs)
	}
	var created credentialJSON
	if err := json.Unmarshal([]byte(out), &created); err != nil || len(created.Secret) != 32 || created.Kind != "password" {
		t.Fatalf("add output %q: %v", out, err)
	}
	if code, _, _ := run(Credential, c, "", "add", "--name", "cam1", "--actions", "read"); code != exitAlready {
		t.Errorf("duplicate: %d, want %d", code, exitAlready)
	}
	if code, out, _ := run(Credential, c, "dev-publisher-secret\n", "add", "--name", "devpub", "--actions", "publish", "--secret-stdin"); code != exitOK || !strings.Contains(out, "dev-publisher-secret") {
		t.Errorf("supplied secret: %d %q", code, out)
	}
	if code, _, _ := run(Credential, c, "", "add", "--name", "x", "--actions", "api"); code != exitFailed {
		t.Errorf("api action: %d", code)
	}
	code, out, _ = run(Credential, c, "", "list")
	if code != exitOK || !strings.Contains(out, "cam1") || !strings.Contains(out, "active") || strings.Contains(out, created.Secret) {
		t.Errorf("list: %d\n%s", code, out)
	}
	if code, _, _ := run(Credential, c, "", "revoke", "--name", "cam1"); code != exitOK {
		t.Errorf("revoke: %d", code)
	}
	if code, _, _ := run(Credential, c, "", "revoke", "--name", "nope"); code != exitFailed {
		t.Errorf("revoke unknown: %d", code)
	}
	code, out, _ = run(Credential, c, "", "list", "--json")
	var list []credentialJSON
	if code != exitOK || json.Unmarshal([]byte(out), &list) != nil || len(list) != 2 || list[0].RevokedAt == nil || list[0].Secret != "" {
		t.Errorf("list --json: %d %s", code, out)
	}
	events, _ := st.ListAudit(context.Background(), 10)
	actions := []string{}
	for _, e := range events {
		actions = append(actions, e.Action+" "+e.Target)
	}
	if strings.Join(actions, ",") != "credential.revoke cam1,credential.add devpub,credential.add cam1" {
		t.Errorf("audit: %v", actions)
	}
	if code, _, _ := run(Credential, c, "", "frobnicate"); code != exitUsage {
		t.Errorf("unknown subcommand: %d", code)
	}
}

func TestResetPassword(t *testing.T) {
	c, st := newCore(t)
	ctx := context.Background()
	var gotBy string
	c.JoinCode = func(_ context.Context, _ *store.Store, _ store.User, by string) (string, time.Time, error) {
		gotBy = by
		return "ABCD-EFGH-JKLM-NPQR", time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC), nil
	}
	if code, _, _ := run(ResetPassword, c, ""); code != exitUsage {
		t.Errorf("no username: %d", code)
	}
	if code, _, errs := run(ResetPassword, c, "", "--username", "nobody"); code != exitFailed || !strings.Contains(errs, "no account") {
		t.Errorf("unknown account: %d %q", code, errs)
	}
	u, err := st.CreateUser(ctx, "admin", "$argon2id$x", "admin")
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ := run(ResetPassword, c, "", "--username", "admin")
	if code != exitOK || !strings.Contains(out, "ABCD-EFGH-JKLM-NPQR") || !strings.Contains(out, "2026-10-06 12:00 UTC") || gotBy != "cli" {
		t.Fatalf("reset: %d %q (by %q)", code, out, gotBy)
	}
	events, _ := st.ListAudit(ctx, 1)
	if len(events) != 1 || events[0].Actor != "cli" || events[0].Action != "user.join-code" || events[0].Target != "admin" ||
		strings.Contains(fmt.Sprint(events[0].Details), "ABCD") {
		t.Errorf("audit %+v", events)
	}
	_ = st.SetUserDisabled(ctx, u.ID, true)
	if code, _, errs := run(ResetPassword, c, "", "--username", "admin"); code != exitFailed || !strings.Contains(errs, "disabled") {
		t.Errorf("disabled account: %d %q", code, errs)
	}
}
