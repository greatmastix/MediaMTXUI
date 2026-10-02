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
)

// Validator runs the pinned MediaMTX binary's --validate-conf, so candidate configs are checked by MediaMTX's own
// parser, at exactly the version that will load them.
type Validator struct {
	Bin string
}

// Validate reports why MediaMTX would reject content, or nil.
func (v Validator) Validate(ctx context.Context, content []byte) error {
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
