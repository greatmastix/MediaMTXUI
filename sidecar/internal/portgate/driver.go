package portgate

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// CommentPrefix marks the firewall rules portgate owns; it adds and deletes only rules that carry it.
const CommentPrefix = "mtx-portgate:"

// Rule is one firewall rule portgate owns. Source is a canonical prefix, or empty for anywhere.
type Rule struct {
	ID     string
	Proto  string
	Port   int
	Source string
}

func (r Rule) String() string {
	src := r.Source
	if src == "" {
		src = "anywhere"
	}
	return fmt.Sprintf("%s %d/%s from %s", r.ID, r.Port, r.Proto, src)
}

// Driver changes the firewall. Rules lists only the rules portgate owns.
type Driver interface {
	Name() string
	Rules(ctx context.Context) ([]Rule, error)
	Add(ctx context.Context, r Rule) error
	Delete(ctx context.Context, r Rule) error
}

// Runner runs a command and returns its combined output.
type Runner func(ctx context.Context, argv ...string) (string, error)

// ExecRunner runs commands for real.
func ExecRunner(ctx context.Context, argv ...string) (string, error) {
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput() //nolint:gosec // fixed programs from the policy or this package
	if err != nil {
		return string(out), fmt.Errorf("%s: %w: %s", argv[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// UFW drives ufw: `ufw allow proto P from S to any port N comment 'mtx-portgate:<id>'`, listed back through
// `ufw show added`. A cloud firewall synced from ufw then follows them by itself.
type UFW struct {
	Run Runner
	Bin string // "ufw" unless set
}

// Name implements Driver.
func (u *UFW) Name() string { return "ufw" }

func (u *UFW) run(ctx context.Context, args ...string) (string, error) {
	bin := u.Bin
	if bin == "" {
		bin = "ufw"
	}
	return u.Run(ctx, append([]string{bin}, args...)...)
}

// Rules implements Driver.
func (u *UFW) Rules(ctx context.Context) ([]Rule, error) {
	out, err := u.run(ctx, "show", "added")
	if err != nil {
		return nil, err
	}
	var rules []Rule
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "'"+CommentPrefix) {
			continue
		}
		r, err := parseUFWRule(line)
		if err != nil {
			return nil, fmt.Errorf("a rule marked %s that portgate cannot read: %q: %w", CommentPrefix, line, err)
		}
		rules = append(rules, r)
	}
	return rules, nil
}

func (u *UFW) spec(r Rule) []string {
	src := r.Source
	if src == "" {
		src = "any"
	}
	return []string{"proto", r.Proto, "from", src, "to", "any", "port", strconv.Itoa(r.Port), "comment", CommentPrefix + r.ID}
}

// Add implements Driver.
func (u *UFW) Add(ctx context.Context, r Rule) error {
	_, err := u.run(ctx, append([]string{"allow"}, u.spec(r)...)...)
	return err
}

// Delete implements Driver.
func (u *UFW) Delete(ctx context.Context, r Rule) error {
	_, err := u.run(ctx, append([]string{"delete", "allow"}, u.spec(r)...)...)
	return err
}

// parseUFWRule reads a `ufw show added` line in either form ufw prints:
//
//	ufw allow from 192.0.2.1 to any port 8554 proto tcp comment 'mtx-portgate:rtsp'
//	ufw allow 8189/udp comment 'mtx-portgate:webrtc'
func parseUFWRule(line string) (Rule, error) {
	head, comment, ok := strings.Cut(strings.TrimSpace(line), " comment '")
	if !ok || !strings.HasSuffix(comment, "'") {
		return Rule{}, errors.New("no comment")
	}
	r := Rule{ID: strings.TrimSuffix(strings.TrimPrefix(comment, CommentPrefix), "'")}
	f := strings.Fields(head)
	if len(f) < 3 || f[0] != "ufw" || f[1] != "allow" {
		return Rule{}, errors.New("not an allow rule")
	}
	f = f[2:]
	if len(f) == 1 { // 8189/udp: from anywhere
		port, proto, ok := strings.Cut(f[0], "/")
		if !ok {
			return Rule{}, errors.New("a port without protocol")
		}
		n, err := strconv.Atoi(port)
		if err != nil {
			return Rule{}, err
		}
		r.Port, r.Proto = n, proto
		return r, r.valid()
	}
	for i := 0; i+1 < len(f); i += 2 {
		switch v := f[i+1]; f[i] {
		case "from":
			if v != "any" {
				p, err := ParseSource(v)
				if err != nil {
					return Rule{}, err
				}
				r.Source = p.String()
			}
		case "to":
			if v != "any" {
				return Rule{}, errors.New("a destination address")
			}
		case "port":
			n, err := strconv.Atoi(v)
			if err != nil {
				return Rule{}, err
			}
			r.Port = n
		case "proto":
			r.Proto = v
		default:
			return Rule{}, fmt.Errorf("unexpected %q", f[i])
		}
	}
	if len(f)%2 != 0 {
		return Rule{}, errors.New("an odd number of words")
	}
	return r, r.valid()
}

func (r Rule) valid() error {
	if !idPattern.MatchString(r.ID) || (r.Proto != "tcp" && r.Proto != "udp") || r.Port < 1 || r.Port > 65535 {
		return errors.New("not a rule portgate writes")
	}
	return nil
}

// FakeUFW is a stand-in for the ufw command that keeps its rules in a text file, one `ufw show added` line each, and
// prints them back the way ufw does. With it the dry-run driver exercises the real UFW driver's commands and parsing,
// and the file holds the full rule list, other people's rules included, for tests to compare before and after.
type FakeUFW struct{ File string }

// Run implements Runner.
func (f *FakeUFW) Run(_ context.Context, argv ...string) (string, error) {
	b, err := os.ReadFile(f.File)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	args := argv[1:]
	switch {
	case len(args) == 2 && args[0] == "show" && args[1] == "added":
		return "Added user rules (see 'ufw status' for running firewall):\n" + strings.Join(lines, "\n") + "\n", nil
	case len(args) == 11 && args[0] == "allow":
		line, err := fakeLine(args[1:])
		if err != nil {
			return "", err
		}
		if slices.Contains(lines, line) {
			return "Skipping adding existing rule\n", nil
		}
		lines = append(lines, line)
	case len(args) == 12 && args[0] == "delete" && args[1] == "allow":
		line, err := fakeLine(args[2:])
		if err != nil {
			return "", err
		}
		i := slices.Index(lines, line)
		if i < 0 {
			return "Could not delete non-existent rule\n", nil
		}
		lines = slices.Delete(lines, i, i+1)
	default:
		return "", fmt.Errorf("fake ufw: unsupported %q", args)
	}
	return "Rule updated\n", os.WriteFile(f.File, []byte(strings.Join(lines, "\n")+"\n"), 0o600) //nolint:gosec // the test file named in the policy
}

// fakeLine formats `proto P from S to any port N comment C` the way `ufw show added` prints the rule.
func fakeLine(spec []string) (string, error) {
	if len(spec) != 10 || spec[0] != "proto" || spec[2] != "from" || spec[4] != "to" || spec[5] != "any" || spec[6] != "port" || spec[8] != "comment" {
		return "", fmt.Errorf("fake ufw: unsupported rule %q", spec)
	}
	proto, src, port, comment := spec[1], spec[3], spec[7], spec[9]
	if src == "any" {
		return fmt.Sprintf("ufw allow %s/%s comment '%s'", port, proto, comment), nil
	}
	p, err := netip.ParsePrefix(src)
	if err != nil {
		return "", err
	}
	if p.IsSingleIP() {
		src = p.Addr().String() // ufw prints a single address without its /32
	}
	return fmt.Sprintf("ufw allow from %s to any port %s proto %s comment '%s'", src, port, proto, comment), nil
}

// NoDriver is the driver for installs without a firewall portgate may change: nothing is ever opened.
type NoDriver struct{}

// Name implements Driver.
func (NoDriver) Name() string { return "none" }

// Rules implements Driver.
func (NoDriver) Rules(context.Context) ([]Rule, error) { return nil, nil }

// Add implements Driver.
func (NoDriver) Add(context.Context, Rule) error {
	return errors.New("the firewall driver is none: open the port by hand")
}

// Delete implements Driver.
func (NoDriver) Delete(context.Context, Rule) error { return nil }
