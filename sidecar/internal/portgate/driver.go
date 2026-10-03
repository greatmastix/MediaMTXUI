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
//
// ufw keeps one rule per port, protocol and source: adding a rule that differs from an existing one only in action
// or comment replaces that one in place ("Rule updated"). So the driver never adds a rule where one of the operator's
// own has the same port, protocol and source (a deny turned into portgate's allow, or a permanent allow portgate
// would delete at expiry); it reports that rule instead.
type UFW struct {
	Run Runner
	Bin string // "ufw" unless set

	others map[Rule]string // the operator's rules a rule of portgate's would replace, as the last listing found them
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
	others := map[Rule]string{}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "'"+CommentPrefix) {
			if l, err := readUFWLine(line); err == nil && l.shaped {
				others[l.rule] = strings.TrimSpace(line)
			}
			continue
		}
		r, err := parseUFWRule(line)
		if err != nil {
			return nil, fmt.Errorf("a rule marked %s that portgate cannot read: %q: %w", CommentPrefix, line, err)
		}
		rules = append(rules, r)
	}
	u.others = others
	return rules, nil
}

func (u *UFW) spec(r Rule) []string {
	src := r.Source
	if src == "" {
		src = "any"
	}
	return []string{"proto", r.Proto, "from", src, "to", "any", "port", strconv.Itoa(r.Port), "comment", CommentPrefix + r.ID}
}

// Add implements Driver. It refuses a rule that would replace one of the operator's (as the last Rules found them;
// it lists them first if Rules has not run).
func (u *UFW) Add(ctx context.Context, r Rule) error {
	if u.others == nil {
		if _, err := u.Rules(ctx); err != nil {
			return err
		}
	}
	key := r
	key.ID = ""
	if line, ok := u.others[key]; ok {
		return fmt.Errorf("a ufw rule of the operator's has this port, protocol and source (%s), and adding "+
			"portgate's would replace it: left alone", line)
	}
	_, err := u.run(ctx, append([]string{"allow"}, u.spec(r)...)...)
	return err
}

// Delete implements Driver. ufw reports a rule it did not find without failing; that is an error here, so a rule it
// keeps is never reported closed.
func (u *UFW) Delete(ctx context.Context, r Rule) error {
	out, err := u.run(ctx, append([]string{"delete", "allow"}, u.spec(r)...)...)
	if err == nil && strings.Contains(out, "Could not delete non-existent rule") && !strings.Contains(out, "Rule deleted") {
		err = errors.New("ufw has no such rule to delete")
	}
	return err
}

// parseUFWRule reads a rule of portgate's from a `ufw show added` line, in either form ufw prints:
//
//	ufw allow from 192.0.2.1 to any port 8554 proto tcp comment 'mtx-portgate:rtsp'
//	ufw allow 8189/udp comment 'mtx-portgate:webrtc'
//
// The source stays as ufw prints it, so deleting the rule names it the way ufw keeps it.
func parseUFWRule(line string) (Rule, error) {
	l, err := readUFWLine(line)
	switch {
	case err != nil:
		return Rule{}, err
	case l.comment == "":
		return Rule{}, errors.New("no comment")
	case l.action != "allow":
		return Rule{}, errors.New("not an allow rule")
	case !l.shaped:
		return Rule{}, errors.New("not a rule portgate writes")
	}
	r := l.rule
	r.ID = strings.TrimPrefix(l.comment, CommentPrefix)
	return r, r.valid()
}

// ufwLine is one `ufw show added` line: its action and comment, and the rule it is in portgate's terms (without an
// ID) when it has the shape of one: incoming, on any interface, from any address or one source, to any address, to one
// port with a protocol. That is everything ufw compares, apart from action and comment, to tell whether two rules
// are the same.
type ufwLine struct {
	action  string // allow, deny, reject or limit
	comment string
	rule    Rule
	shaped  bool
}

// readUFWLine reads a `ufw show added` line. A rule of another shape (outgoing, routed, on an interface, for an
// application, from a source port, to an address or several ports) is read with shaped false.
func readUFWLine(line string) (ufwLine, error) {
	head, comment, ok := strings.Cut(strings.TrimSpace(line), " comment '")
	var l ufwLine
	if ok {
		if !strings.HasSuffix(comment, "'") {
			return l, errors.New("a comment without its closing quote")
		}
		l.comment = strings.TrimSuffix(comment, "'")
	}
	f := strings.Fields(head)
	if len(f) < 3 || f[0] != "ufw" {
		return l, errors.New("not a ufw rule")
	}
	switch f[1] {
	case "allow", "deny", "reject", "limit":
		l.action = f[1]
		l.rule, l.shaped = ufwShape(f[2:])
	case "route": // forwarded traffic: never the same as an incoming rule
	default:
		return l, fmt.Errorf("unexpected %q", f[1])
	}
	return l, nil
}

// ufwShape reads what follows a rule's action as portgate's kind of rule, if it is one.
func ufwShape(f []string) (Rule, bool) {
	if len(f) > 0 && (f[0] == "log" || f[0] == "log-all") {
		f = f[1:] // ufw does not compare logging either
	}
	if len(f) == 1 { // 8189/udp: from anywhere to any address (not: any protocol, several ports, an application)
		port, proto, _ := strings.Cut(f[0], "/")
		n, ok := portNumber(port)
		return Rule{Proto: proto, Port: n}, ok && (proto == "tcp" || proto == "udp")
	}
	if len(f)%2 != 0 {
		return Rule{}, false // in on <interface>, out, an application name with spaces
	}
	r, last, toAny := Rule{}, "", false
	for i := 0; i < len(f); i += 2 {
		switch k, v := f[i], f[i+1]; k {
		case "from":
			if v != "any" {
				p, err := parsePrefix(v)
				if err != nil {
					return Rule{}, false
				}
				r.Source = p.String()
			}
			last = k
		case "to":
			toAny, last = v == "any", k
		case "port":
			n, ok := portNumber(v)
			if last != "to" || !ok {
				return Rule{}, false // a source port, or several ports
			}
			r.Port = n
		case "proto":
			r.Proto = v
		default:
			return Rule{}, false // in on, out on, app
		}
	}
	return r, toAny && r.Port > 0 && (r.Proto == "tcp" || r.Proto == "udp")
}

// portNumber reads one port (not a list or a range).
func portNumber(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0
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
		// Like ufw, a rule that differs from an existing one only in action or comment replaces it in place.
		added, _ := readUFWLine(line)
		i := slices.IndexFunc(lines, func(l string) bool {
			other, err := readUFWLine(l)
			return err == nil && other.shaped && other.rule == added.rule
		})
		if i >= 0 {
			lines[i] = line
			return "Rule updated\n", f.write(lines)
		}
		lines = append(lines, line)
		return "Rule added\n", f.write(lines)
	case len(args) == 12 && args[0] == "delete" && args[1] == "allow":
		line, err := fakeLine(args[2:])
		if err != nil {
			return "", err
		}
		i := slices.Index(lines, line)
		if i < 0 {
			return "Could not delete non-existent rule\n", nil
		}
		return "Rule deleted\n", f.write(slices.Delete(lines, i, i+1))
	}
	return "", fmt.Errorf("fake ufw: unsupported %q", args)
}

func (f *FakeUFW) write(lines []string) error {
	return os.WriteFile(f.File, []byte(strings.Join(lines, "\n")+"\n"), 0o600) //nolint:gosec // the test file named in the policy
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
