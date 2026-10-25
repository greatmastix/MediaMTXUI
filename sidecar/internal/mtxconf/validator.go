package mtxconf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"mtxui/internal/yamledit"
)

// Validator runs the pinned MediaMTX binary's --validate-conf, so candidate configs are checked by MediaMTX's own
// parser, at exactly the version that will load them.
type Validator struct {
	Bin string
	// StandIn, when set, may name a file to check in place of a holding clip (an alwaysAvailableFile), or return ""
	// to check the clip itself. MediaMTX reads a clip's every sample to validate it, about 5 s an hour of video, and
	// a check gives up after 15 s; the sidecar checks its own uploads with the same reader when they arrive, so the
	// stand-in is a small clip of its own, for clips that are there (a missing one is still reported as missing).
	StandIn func(file string) string
}

// Validate reports why MediaMTX would reject content, or nil.
func (v Validator) Validate(ctx context.Context, content []byte) error {
	if v.StandIn != nil {
		var err error
		if content, err = standIns(content, v.StandIn); err != nil {
			return &InvalidError{Err: fmt.Errorf("the config cannot be read: %w", err)}
		}
	}
	dir, err := os.MkdirTemp("", "mtxui-validate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "mediamtx.yml")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, v.Bin, "--validate-conf="+path) //nolint:gosec // Bin comes from settings, not requests
	cmd.Dir = dir
	cmd.Env = []string{} // MediaMTX applies MTX_* environment overrides; validation must see the file alone
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return fmt.Errorf("running %s: %w", v.Bin, err)
	}
	msg := strings.TrimSpace(strings.ReplaceAll(out.String(), path, "mediamtx.yml"))
	msg = strings.TrimPrefix(msg, "configuration file: mediamtx.yml")
	return &InvalidError{Err: fmt.Errorf("MediaMTX rejects the config: %s", strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(msg), "ERR:")))}
}

// standIns puts each holding clip's stand-in (standIn) in its place, in pathDefaults and every path.
func standIns(content []byte, standIn func(string) string) ([]byte, error) {
	type holding struct {
		AlwaysAvailableFile string `yaml:"alwaysAvailableFile"`
	}
	var doc struct {
		PathDefaults holding            `yaml:"pathDefaults"`
		Paths        map[string]holding `yaml:"paths"`
	}
	if yaml.Unmarshal(content, &doc) != nil {
		return content, nil //nolint:nilerr // MediaMTX says what is wrong with the content
	}
	var at [][]string
	var to []string
	if f := doc.PathDefaults.AlwaysAvailableFile; f != "" {
		if s := standIn(f); s != "" {
			at, to = append(at, []string{"pathDefaults", "alwaysAvailableFile"}), append(to, s)
		}
	}
	for name, p := range doc.Paths {
		if p.AlwaysAvailableFile == "" {
			continue
		}
		if s := standIn(p.AlwaysAvailableFile); s != "" {
			at, to = append(at, []string{"paths", name, "alwaysAvailableFile"}), append(to, s)
		}
	}
	if len(at) == 0 {
		return content, nil
	}
	d, err := yamledit.New(content)
	if err != nil {
		return nil, err
	}
	for i := range at {
		if err := d.Set(at[i], to[i]); err != nil {
			return nil, err
		}
	}
	return d.Bytes(), nil
}

// Version returns the binary's version, e.g. "v1.21.1".
func (v Validator) Version(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, v.Bin, "--version") //nolint:gosec // Bin comes from settings
	cmd.Env = []string{}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("running %s --version: %w", v.Bin, err)
	}
	return strings.TrimSpace(string(out)), nil
}
