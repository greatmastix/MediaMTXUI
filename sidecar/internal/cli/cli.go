// Package cli implements the sidecar's administrative subcommands. They run inside the container (for example with
// `docker compose exec sidecar /mtxui credential list`), next to the running server, on the same database:
//
//	mtxui setup --username NAME [--ingest rtsp,rtmp,srt]      first-run setup; the password is read from stdin
//	mtxui credential add --name NAME --actions publish,read [--paths cam1,~^live/] [--sources 10.0.0.0/8]
//	                     [--expires 720h] [--kind password|token] [--secret-stdin] [--json]
//	mtxui credential list [--json]
//	mtxui credential revoke --name NAME
//
// Exit codes: 0 success, 1 failure, 2 usage error, 3 already done (setup completed, credential exists).
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"mtxui/internal/audit"
	"mtxui/internal/credentials"
	"mtxui/internal/setup"
	"mtxui/internal/store"
)

// Core is what the subcommands work with.
type Core struct {
	Setup *setup.Service
	Creds *credentials.Service
	Audit *audit.Recorder
}

// IO are the command's streams.
type IO struct {
	In       io.Reader
	Out, Err io.Writer
}

const (
	exitOK      = 0
	exitFailed  = 1
	exitUsage   = 2
	exitAlready = 3
)

// Setup runs first-run setup without the setup token: whoever can run commands in the container is already root on
// the host.
func Setup(ctx context.Context, c Core, args []string, io IO) int {
	fs := flag.NewFlagSet("mtxui setup", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	username := fs.String("username", "", "the first admin's username")
	ingest := fs.String("ingest", "", "protocols to switch on: rtsp, rtmp, srt (comma-separated)")
	if err := fs.Parse(args); err != nil || *username == "" || fs.NArg() > 0 {
		fmt.Fprintln(io.Err, "usage: mtxui setup --username NAME [--ingest rtsp,rtmp,srt] < password")
		return exitUsage
	}
	var in setup.Ingest
	for _, p := range split(*ingest) {
		switch p {
		case "rtsp":
			in.RTSP = true
		case "rtmp":
			in.RTMP = true
		case "srt":
			in.SRT = true
		default:
			fmt.Fprintf(io.Err, "unknown protocol %q (rtsp, rtmp, srt)\n", p)
			return exitUsage
		}
	}
	password, err := readLine(io.In)
	if err != nil || password == "" {
		fmt.Fprintln(io.Err, "mtxui setup reads the password from stdin")
		return exitUsage
	}
	user, warning, err := c.Setup.Complete(ctx, setup.Request{Username: *username, Password: password, Ingest: in})
	var verr setup.ValidationError
	switch {
	case errors.Is(err, store.ErrSetupDone):
		fmt.Fprintln(io.Err, "setup has already been completed")
		return exitAlready
	case errors.As(err, &verr):
		fmt.Fprintln(io.Err, verr.Msg)
		return exitFailed
	case err != nil:
		fmt.Fprintln(io.Err, "setup failed:", err)
		return exitFailed
	}
	c.Audit.Record(ctx, store.AuditEvent{
		Actor: "cli", Action: "setup.complete", Target: user.Username,
		Details: map[string]any{"ingest": in, "via": "command line"},
	})
	fmt.Fprintf(io.Out, "Setup complete: admin %q created.\n", user.Username)
	if warning != "" {
		fmt.Fprintln(io.Err, warning)
	}
	return exitOK
}

// Credential manages stream credentials.
func Credential(ctx context.Context, c Core, args []string, io IO) int {
	if len(args) == 0 {
		fmt.Fprintln(io.Err, "usage: mtxui credential add|list|revoke ...")
		return exitUsage
	}
	switch args[0] {
	case "add":
		return credentialAdd(ctx, c, args[1:], io)
	case "list":
		return credentialList(ctx, c, args[1:], io)
	case "revoke":
		return credentialRevoke(ctx, c, args[1:], io)
	default:
		fmt.Fprintf(io.Err, "unknown subcommand %q: use add, list or revoke\n", args[0])
		return exitUsage
	}
}

type credentialJSON struct {
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Secret     string     `json:"secret,omitempty"`
	Actions    []string   `json:"actions"`
	Paths      []string   `json:"paths"`
	Sources    []string   `json:"sources"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	CreatedBy  string     `json:"createdBy"`
}

func toJSON(cr store.Credential, secret string) credentialJSON {
	return credentialJSON{
		Name: cr.Name, Kind: cr.Kind, Secret: secret, Actions: cr.Actions, Paths: cr.Paths,
		Sources: cr.SourceCIDRs, ExpiresAt: cr.ExpiresAt, RevokedAt: cr.RevokedAt, LastUsedAt: cr.LastUsedAt, CreatedBy: cr.CreatedBy,
	}
}

func credentialAdd(ctx context.Context, c Core, args []string, io IO) int {
	fs := flag.NewFlagSet("mtxui credential add", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	name := fs.String("name", "", "name; clients present it as the username (kind password)")
	kind := fs.String("kind", credentials.KindPassword, "password (name and secret) or token (bearer)")
	actions := fs.String("actions", "", "comma-separated: "+strings.Join(credentials.Actions, ", "))
	paths := fs.String("paths", "", "comma-separated path names or ~regex; empty means any path")
	sources := fs.String("sources", "", "comma-separated IPs or CIDRs; empty means anywhere")
	expires := fs.Duration("expires", 0, "lifetime, e.g. 720h; 0 means no expiry")
	secretStdin := fs.Bool("secret-stdin", false, "read the secret from stdin instead of generating one")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil || *name == "" || fs.NArg() > 0 {
		fmt.Fprintln(io.Err, "usage: mtxui credential add --name NAME --actions publish,read [--paths ...] [--sources ...] [--expires 720h] [--kind password|token] [--secret-stdin] [--json]")
		return exitUsage
	}
	spec := credentials.Spec{
		Name: *name, Kind: *kind, Actions: split(*actions), Paths: split(*paths),
		SourceCIDRs: split(*sources), TTL: *expires, CreatedBy: "cli",
	}
	if *secretStdin {
		s, err := readLine(io.In)
		if err != nil || s == "" {
			fmt.Fprintln(io.Err, "--secret-stdin: no secret on stdin")
			return exitUsage
		}
		spec.Secret = s
	}
	secret, cr, err := c.Creds.Add(ctx, spec)
	switch {
	case errors.Is(err, store.ErrExists):
		fmt.Fprintln(io.Err, err)
		return exitAlready
	case err != nil:
		fmt.Fprintln(io.Err, err)
		return exitFailed
	}
	c.Audit.Record(ctx, store.AuditEvent{
		Actor: "cli", Action: "credential.add", Target: cr.Name,
		Details: map[string]any{"kind": cr.Kind, "actions": cr.Actions, "paths": cr.Paths, "sources": cr.SourceCIDRs, "expiresAt": cr.ExpiresAt},
	})
	if *asJSON {
		return writeJSON(io.Out, toJSON(cr, secret))
	}
	fmt.Fprintf(io.Out, "Created %s credential %q. Its secret is shown only now:\n%s\n", cr.Kind, cr.Name, secret)
	return exitOK
}

func credentialList(ctx context.Context, c Core, args []string, io IO) int {
	fs := flag.NewFlagSet("mtxui credential list", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return exitUsage
	}
	list, err := c.Creds.List(ctx)
	if err != nil {
		fmt.Fprintln(io.Err, err)
		return exitFailed
	}
	if *asJSON {
		out := make([]credentialJSON, 0, len(list))
		for _, cr := range list {
			out = append(out, toJSON(cr, ""))
		}
		return writeJSON(io.Out, out)
	}
	tw := tabwriter.NewWriter(io.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tACTIONS\tPATHS\tSOURCES\tEXPIRES\tSTATE\tLAST USED")
	for _, cr := range list {
		state := "active"
		if cr.RevokedAt != nil {
			state = "revoked"
		} else if cr.ExpiresAt != nil && !cr.ExpiresAt.After(time.Now()) {
			state = "expired"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", cr.Name, cr.Kind, strings.Join(cr.Actions, ","),
			orAny(cr.Paths), orAny(cr.SourceCIDRs), when(cr.ExpiresAt, "never"), state, when(cr.LastUsedAt, "never"))
	}
	_ = tw.Flush()
	return exitOK
}

func credentialRevoke(ctx context.Context, c Core, args []string, io IO) int {
	fs := flag.NewFlagSet("mtxui credential revoke", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	name := fs.String("name", "", "the credential to revoke")
	if err := fs.Parse(args); err != nil || *name == "" || fs.NArg() > 0 {
		fmt.Fprintln(io.Err, "usage: mtxui credential revoke --name NAME")
		return exitUsage
	}
	if err := c.Creds.Revoke(ctx, *name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(io.Err, "no credential named %q\n", *name)
		} else {
			fmt.Fprintln(io.Err, err)
		}
		return exitFailed
	}
	c.Audit.Record(ctx, store.AuditEvent{Actor: "cli", Action: "credential.revoke", Target: *name})
	fmt.Fprintf(io.Out, "Revoked %q. New connections with it are refused; sessions already open stay until they end.\n", *name)
	return exitOK
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func writeJSON(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return exitFailed
	}
	return exitOK
}

func orAny(list []string) string {
	if len(list) == 0 {
		return "any"
	}
	return strings.Join(list, ",")
}

func when(t *time.Time, none string) string {
	if t == nil {
		return none
	}
	return t.UTC().Format(time.RFC3339)
}
