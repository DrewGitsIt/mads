package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/paircode"
)

// `mads mesh`: the mesh setup flow from a terminal, for a machine without
// a screen (docs/MESH-NETWORK-DESIGN.md, "The command line"). The same
// flow as Settings → Paired devices, through the same Router routes.
// Every prompt has a flag; Enter accepts the suggestion; every command
// exits non-zero with a one-line reason when it can't go on.

const meshUsage = `mads mesh — use mads across your devices (docs/MESH-NETWORK-DESIGN.md)

  mads mesh setup [--network tailscale|lan:IFACE|ID|none] [--yes] [--no-wait]
                  [--level LEVEL] [--check-code DIGITS] [--timeout 10m]
        choose how your devices reach each other, then wait (up to 10 minutes,
        Ctrl-C to stop) for your other devices to ask to join, approving each here
  mads mesh status [--json]                this device's mesh network and its devices
  mads mesh add [NAME] [--check-code DIGITS] [--timeout 15s]
        find mads devices whose setup is open on the mesh network and add one
        (it approves on its own screen; --check-code is the number it showed, compared here)
  mads mesh approve [--level LEVEL] [--yes] [--check-code DIGITS] [--id ID] [--timeout 10m]
        wait for one pairing request and answer it (refuses admin)
  mads mesh code [--json]                  a pairing code for another device (5 minutes, single use)
  mads mesh join [CODE] [--update|--repair] pair with the host whose code this is (reads stdin without CODE)
  mads mesh set DEVICE --level LEVEL       what DEVICE may do with mads here
  mads mesh remove DEVICE [--everywhere] [--also-introduced]
  mads mesh address add DEVICE HOST:PORT   an address this device should try for DEVICE
  mads mesh address remove DEVICE HOST:PORT
  mads mesh discoverable [--off]           open (10 minutes) or close this device's setup window

LEVEL is none, view, chat or control (admin is set afterwards on the host, never by
pairing). A headless machine: run "mads mesh code" on it once, paste the code on your
main device and approve it here at administer (mads mesh set <device> --level admin);
from then on add your other devices to it with Introduce a device… on the main one.
All take -router URL (default http://localhost:17373).
`

// meshFlags is every mads mesh flag; each subcommand reads its own.
type meshFlags struct {
	router                           string
	network, level, checkCode, id    string
	yes, noWait, asJSON, update, off bool
	repair                           bool
	everywhere, alsoIntroduced       bool
	timeout                          time.Duration
}

// meshCLI is one mads mesh run: its terminal, its Router.
type meshCLI struct {
	base   string
	in     *bufio.Reader
	out    io.Writer
	errOut io.Writer
	// interactive: there's someone to ask (a terminal, or a test's input).
	interactive bool
	f           meshFlags
	now         func() time.Time
}

// meshErr is a Router answer that wasn't 2xx, with its code.
type meshErr struct {
	Status int
	Code   string
	Msg    string
	Body   map[string]any
}

func (e *meshErr) Error() string { return e.Msg }

func (c *meshCLI) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := routerClient().Do(req)
	if err != nil {
		return fmt.Errorf("reach the Router at %s (is mads running?): %w", c.base, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := &meshErr{Status: resp.StatusCode, Msg: fmt.Sprintf("%s %s: %s", method, path, resp.Status)}
		if json.Unmarshal(raw, &e.Body) == nil {
			if s, _ := e.Body["code"].(string); s != "" {
				e.Code = s
			}
			if s, _ := e.Body["error"].(string); s != "" {
				e.Msg = paircode.CleanText(s)
			}
		}
		return e
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (c *meshCLI) printf(format string, args ...any) { _, _ = fmt.Fprintf(c.out, format, args...) }

// ask prompts and reads one line; "" (Enter) is the suggestion's to pick.
func (c *meshCLI) ask(prompt string) (string, error) {
	if !c.interactive {
		return "", errNoTerminal
	}
	c.printf("%s", prompt)
	line, err := c.in.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		if errors.Is(err, io.EOF) {
			// Input ended (cron, /dev/null): nobody to ask after all.
			c.printf("\n")
			return "", errNoTerminal
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}

var errNoTerminal = errors.New("no terminal to ask")

// yesNo asks a [y/N] question (Enter is no).
func (c *meshCLI) yesNo(prompt string) (bool, error) {
	a, err := c.ask(prompt + " [y/N] ")
	if err != nil {
		return false, err
	}
	return strings.EqualFold(a, "y") || strings.EqualFold(a, "yes"), nil
}

// cmdMesh runs `mads mesh <subcommand>`.
func cmdMesh(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		_, _ = fmt.Fprint(stdout, meshUsage)
		if len(args) == 0 {
			return errors.New("mesh: name a subcommand (mads mesh --help)")
		}
		return nil
	}
	sub, rest := args[0], args[1:]
	if sub == "address" {
		if len(rest) == 0 {
			return errors.New("mesh address: add or remove (mads mesh --help)")
		}
		sub, rest = "address "+rest[0], rest[1:]
	}
	fs := flag.NewFlagSet("mesh "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprint(stderr, meshUsage) }
	var f meshFlags
	fs.StringVar(&f.router, "router", envOr("MADS_ROUTER_URL", defaultRouterURL), "Router base URL")
	fs.StringVar(&f.network, "network", "", "setup: the mesh network (tailscale, lan:IFACE, an id, or none)")
	fs.StringVar(&f.level, "level", "", "what the device may do here: none, view, chat or control")
	fs.StringVar(&f.checkCode, "check-code", "", "approve, setup: the six digits the joining device shows; add: the six digits the host shows (compared with this device's)")
	fs.StringVar(&f.id, "id", "", "approve: this request only (idempotent)")
	fs.BoolVar(&f.yes, "yes", false, "don't ask to confirm")
	fs.BoolVar(&f.noWait, "no-wait", false, "setup: choose the network and stop (don't wait for devices)")
	fs.BoolVar(&f.asJSON, "json", false, "print JSON")
	fs.BoolVar(&f.update, "update", false, "join: update an already-paired host's addresses from its code")
	fs.BoolVar(&f.repair, "repair", false, "join: pair again with a host paired here that no longer has this device paired")
	fs.BoolVar(&f.off, "off", false, "discoverable: close the window")
	fs.BoolVar(&f.everywhere, "everywhere", false, "remove: also on every host where this device administers")
	fs.BoolVar(&f.alsoIntroduced, "also-introduced", false, "remove: also the devices it introduced")
	fs.DurationVar(&f.timeout, "timeout", 0, "how long to wait (setup and approve: 10m; add: 15s)")
	var pos []string
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		rest = fs.Args()
		if len(rest) > 0 {
			pos = append(pos, rest[0])
			rest = rest[1:]
		}
	}
	switch f.level {
	case "", "none", "view", "chat", "control":
	case "admin":
		if sub != "set" {
			return fmt.Errorf("mesh %s: admin is never granted by pairing; once paired, mads mesh set <device> --level admin here", sub)
		}
	default:
		return fmt.Errorf("mesh %s: --level none, view, chat or control", sub)
	}
	if f.checkCode != "" {
		if d := strings.ReplaceAll(f.checkCode, " ", ""); len(d) != 6 || strings.Trim(d, "0123456789") != "" {
			return fmt.Errorf("mesh %s: --check-code is six digits", sub)
		}
	}
	c := &meshCLI{base: strings.TrimRight(f.router, "/"), in: bufio.NewReader(stdin), out: stdout, errOut: stderr, f: f, now: time.Now, interactive: true}
	if file, ok := stdin.(*os.File); ok {
		c.interactive = isTerminal(file)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch sub {
	case "setup":
		return c.setup(ctx)
	case "status":
		return c.status(ctx)
	case "add":
		if len(pos) > 1 {
			return errors.New("mesh add: at most one device name")
		}
		name := ""
		if len(pos) == 1 {
			name = pos[0]
		}
		return c.add(ctx, name)
	case "approve":
		return c.approve(ctx)
	case "code":
		return c.code(ctx)
	case "join":
		if len(pos) > 1 {
			return errors.New("mesh join: one code")
		}
		code := ""
		if len(pos) == 1 && pos[0] != "-" {
			code = pos[0]
		}
		return c.join(ctx, code)
	case "set":
		if len(pos) != 1 {
			return errors.New("mesh set: name one device (mads mesh set DEVICE --level LEVEL)")
		}
		return c.set(ctx, pos[0])
	case "remove":
		if len(pos) != 1 {
			return errors.New("mesh remove: name one device")
		}
		args := []string{"remove", pos[0], "-router", c.base}
		if f.everywhere {
			args = append(args, "--everywhere")
		}
		if f.alsoIntroduced {
			args = append(args, "--also-introduced")
		}
		if f.asJSON {
			args = append(args, "--json")
		}
		err := cmdPairSub(args, stdin, stdout)
		if err != nil {
			return errors.New(strings.Replace(err.Error(), "pair remove:", "mesh remove:", 1))
		}
		return nil
	case "address add", "address remove":
		if len(pos) != 2 {
			return fmt.Errorf("mesh %s: give DEVICE and HOST:PORT", sub)
		}
		return c.address(ctx, sub == "address add", pos[0], pos[1])
	case "discoverable":
		return c.discoverable(ctx)
	}
	return fmt.Errorf("mesh: unknown subcommand %q (mads mesh --help)", sub)
}

// --- Shapes the Router answers ------------------------------------------------------

type meshSelf struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Short       string `json:"short"`
	Platform    string `json:"platform"`
	Mesh        struct {
		Configured         bool                   `json:"configured"`
		Networks           []meshNetView          `json:"networks"`
		Port               int                    `json:"port"`
		TailnetCheck       bool                   `json:"tailnet_check"`
		TailnetCheckStatus string                 `json:"tailnet_check_status"`
		TailnetCheckLocked bool                   `json:"tailnet_check_locked"`
		Public             bool                   `json:"public"`
		DiscoverableUntil  *time.Time             `json:"discoverable_until"`
		Listening          []contracts.NetAddress `json:"listening"`
		ServiceSeen        bool                   `json:"service_seen"`
		ServiceAnswering   *bool                  `json:"service_answering"`
		Tailscale          *struct {
			Present      bool       `json:"present"`
			NeedsLogin   bool       `json:"needs_login"`
			AuthURL      string     `json:"auth_url"`
			KeyExpiry    *time.Time `json:"key_expiry"`
			KeyExpiryOff bool       `json:"key_expiry_off"`
			AdminURL     string     `json:"admin_url"`
		} `json:"tailscale"`
	} `json:"mesh"`
}

type meshNetView struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Iface       string `json:"iface"`
	Meaning     string `json:"meaning"`
	WhoCanKnock string `json:"who_can_knock"`
	Recommended bool   `json:"recommended"`
	Shared      bool   `json:"shared"`
	Chosen      bool   `json:"chosen"`
	Available   bool   `json:"available"`
	Listening   bool   `json:"listening"`
	Error       string `json:"error"`
}

func (n meshNetView) meaningLine() string {
	switch n.Meaning {
	case "vpn_anywhere":
		return "works from anywhere your devices have internet"
	case "local_only":
		if n.Shared {
			return "a shared network: anyone on it can try to connect"
		}
		return "only while both devices are on this network"
	}
	return "anyone on the internet can try to connect"
}

type meshJoin struct {
	ID             string                             `json:"id"`
	RequestID      string                             `json:"request_id"`
	State          contracts.PairState                `json:"state"`
	Level          string                             `json:"level"`
	Host           struct{ Name, Fingerprint string } `json:"host"`
	PeerID         string                             `json:"peer_id"`
	Error          string                             `json:"error"`
	Self           string                             `json:"self"`
	CheckCode      string                             `json:"check_code"`
	Vouched        bool                               `json:"vouched"`
	Confirm        string                             `json:"confirm"`
	Network        string                             `json:"network"`
	AdoptedNetwork *struct {
		ID, Label string
	} `json:"adopted_network"`
	NotAdopted string `json:"not_adopted"`
}

// --- setup ----------------------------------------------------------------------------

func (c *meshCLI) self(ctx context.Context) (meshSelf, error) {
	var s meshSelf
	err := c.call(ctx, http.MethodGet, "/devices/self", nil, &s)
	return s, err
}

// pickNetwork resolves --network exactly: a network's id ("tailscale",
// "lan:192.168.1.0/24/192.168.1.1"), or lan:IFACE / vpn:IFACE for the
// local network or VPN on that interface. Never another kind than asked.
func pickNetwork(nets []meshNetView, want string) (meshNetView, bool) {
	for _, n := range nets {
		if n.ID == want {
			return n, true
		}
	}
	kind, iface, ok := strings.Cut(want, ":")
	if !ok {
		return meshNetView{}, false
	}
	k := map[string]string{"lan": "lan", "vpn": "overlay"}[kind]
	for _, n := range nets {
		if k != "" && n.Kind == k && n.Iface == iface && n.ID != "tailscale" {
			return n, true
		}
	}
	return meshNetView{}, false
}

const noSharedTips = `mads connects devices over a network they both share. It doesn't make one.
  Devices in the same place: join the same Wi-Fi network name on both (wired and
    Wi-Fi on the same router usually works); avoid a guest network, which usually
    keeps devices apart; some routers have client isolation turned on.
  Devices in different places: install the same VPN on each and sign in to the same
    account, then run this again:
    Tailscale  https://tailscale.com/kb/1017/install
    ZeroTier   https://docs.zerotier.com/start
    NetBird    https://docs.netbird.io/how-to/getting-started
`

func (c *meshCLI) setup(ctx context.Context) error {
	self, err := c.self(ctx)
	if err != nil {
		return fmt.Errorf("mesh setup: %w", err)
	}
	var offer []meshNetView
	for _, n := range self.Mesh.Networks {
		if n.Available && n.Kind != "public" {
			offer = append(offer, n)
		}
	}
	var chosen *meshNetView
	switch want := c.f.network; {
	case want == "none":
		var out meshSelf
		if err := c.call(ctx, http.MethodPut, "/devices/settings", map[string]any{"networks": []string{}}, &out); err != nil {
			return fmt.Errorf("mesh setup: %w", err)
		}
		c.printf("This %s listens on no network now: no other device can reach it.\n", self.Name)
		return nil
	case strings.HasPrefix(want, "public"):
		return errors.New("mesh setup: a public address isn't offered as the mesh network (Advanced options: Also reachable from the internet)")
	case want != "":
		n, ok := pickNetwork(offer, want)
		if !ok {
			var ids []string
			for _, o := range offer {
				ids = append(ids, o.ID)
			}
			return fmt.Errorf("mesh setup: this %s isn't on a network %q now; it's on: %s", self.Name, want, firstNonEmptyStr(strings.Join(ids, ", "), "none it can share"))
		}
		chosen = &n
	default:
		if len(offer) == 0 {
			c.printf("%s", noSharedTips)
			return fmt.Errorf("mesh setup: this %s isn't on any network it can share. See: mads mesh setup --help", self.Name)
		}
		c.printf("How will your devices reach each other?\n")
		suggested := 1
		w := tabwriter.NewWriter(c.out, 0, 2, 2, ' ', 0)
		for i, n := range offer {
			tag := ""
			if n.Recommended {
				tag = "(recommended)"
				if suggested == 1 && i > 0 && !offer[0].Recommended {
					suggested = i + 1
				}
			}
			title := n.Title
			if n.Iface != "" && n.Kind == "lan" && !strings.Contains(title, n.Iface) {
				title += " (" + n.Iface + ")"
			}
			_, _ = fmt.Fprintf(w, "  %d) %s\t%s\t%s\n", i+1, title, tag, n.meaningLine())
		}
		_ = w.Flush()
		c.printf("  %d) None of these\n", len(offer)+1)
		for {
			a, err := c.ask(fmt.Sprintf("Choose [%d]: ", suggested))
			if errors.Is(err, errNoTerminal) {
				return errors.New("mesh setup: choose a network with --network (tailscale, lan:IFACE or none): there's no terminal to ask")
			}
			if err != nil {
				return fmt.Errorf("mesh setup: %w", err)
			}
			k := suggested
			if a != "" {
				if k, err = strconv.Atoi(a); err != nil || k < 1 || k > len(offer)+1 {
					c.printf("Choose 1 to %d.\n", len(offer)+1)
					continue
				}
			}
			if k == len(offer)+1 {
				c.printf("\n%s", noSharedTips)
				return errors.New("mesh setup: no network chosen; give your devices a network in common, then run mads mesh setup again")
			}
			chosen = &offer[k-1]
			break
		}
	}
	if chosen.Shared && !c.f.yes {
		ok, err := c.yesNo("! " + chosen.Title + " looks like a shared network: anyone on it can try to connect (only devices you add get in, but a VPN such as Tailscale is safer). Use it?")
		if err != nil && !errors.Is(err, errNoTerminal) {
			return fmt.Errorf("mesh setup: %w", err)
		}
		if !ok {
			return errors.New("mesh setup: not using a shared network (pass --yes to anyway)")
		}
	}
	var after meshSelf
	if err := c.call(ctx, http.MethodPut, "/devices/settings", map[string]any{"networks": []string{chosen.ID}}, &after); err != nil {
		return fmt.Errorf("mesh setup: %w", err)
	}
	// Say it's reachable only once the Mesh Service listens there.
	after, listening := c.settle(ctx, chosen.ID, after)
	switch {
	case listening:
		c.printf("✓ This %s is reachable on %s. Only devices you add get in.\n", self.Name, chosen.Title)
	case !after.Mesh.ServiceSeen:
		c.printf("! %s is this %s's mesh network, but its Mesh Service hasn't reported: start it (bin/mesh, or its systemd unit), and it listens there.\n", chosen.Title, self.Name)
	case after.Mesh.ServiceAnswering != nil && !*after.Mesh.ServiceAnswering:
		// It reported once, but doesn't answer now (round-2 T5).
		c.printf("! %s is this %s's mesh network, but the Mesh Service isn't answering there (is it running? bin/mesh, or its systemd unit).\n", chosen.Title, self.Name)
	default:
		why := " yet"
		for _, n := range after.Mesh.Networks {
			if n.ID == chosen.ID && n.Error != "" {
				why = ": " + n.Error // the real reason (T7)
			}
		}
		c.printf("! %s is this %s's mesh network, but its Mesh Service isn't listening there%s.\n", chosen.Title, self.Name, why)
	}
	if chosen.ID == "tailscale" {
		c.tailscaleLines(after)
	}
	if c.f.noWait {
		return nil
	}
	c.printf("\nWaiting for your other devices (up to %s; Ctrl-C to stop).\nOn a device with mads open: Settings → Paired devices → Add devices.\n", waitFor(c.f.timeout))
	return c.waitAndApprove(ctx, self, false)
}

func (c *meshCLI) tailscaleLines(s meshSelf) {
	t := s.Mesh.Tailscale
	if t == nil || !t.Present {
		return
	}
	switch {
	case t.NeedsLogin:
		where := "run: sudo tailscale up"
		if t.AuthURL != "" {
			where = t.AuthURL
		}
		c.printf("! Tailscale needs this %s to sign in again: %s\n", s.Name, where)
	case t.KeyExpiryOff:
		c.printf("✓ Tailscale key expiry is off for this %s.\n", s.Name)
	case t.KeyExpiry != nil:
		c.printf("! Tailscale signs this %s out on %s. For a machine you reach\n  remotely, turn off key expiry: %s\n", s.Name, t.KeyExpiry.Local().Format("Jan 2"), firstNonEmptyStr(t.AdminURL, "https://login.tailscale.com/admin/machines"))
	}
}

// joinWait bounds how long add and join wait for the host's answer.
var joinWait = 6 * time.Minute

// setupSettle is how long setup waits for the Mesh Service to listen on
// the network just chosen before saying where things stand.
var setupSettle = 12 * time.Second

// settle polls this device until its Mesh Service listens on network id
// (true), or setupSettle passes.
func (c *meshCLI) settle(ctx context.Context, id string, s meshSelf) (meshSelf, bool) {
	deadline := c.now().Add(setupSettle)
	for {
		for _, n := range s.Mesh.Networks {
			if n.ID == id && n.Listening {
				return s, true
			}
		}
		if !c.now().Before(deadline) {
			return s, false
		}
		select {
		case <-ctx.Done():
			return s, false
		case <-time.After(500 * time.Millisecond):
		}
		if next, err := c.self(ctx); err == nil {
			s = next
		}
	}
}

// waitFor is how long a wait runs (the --timeout, else 10 minutes), in
// words.
func waitFor(d time.Duration) string {
	if d <= 0 {
		d = 10 * time.Minute
	}
	switch {
	case d == time.Minute:
		return "1 minute"
	case d%time.Minute == 0:
		return strconv.Itoa(int(d/time.Minute)) + " minutes"
	}
	return d.Round(100 * time.Millisecond).String()
}

// waitAndApprove opens this device's setup window (renewed every minute)
// and answers requests as they come, until Ctrl-C, the timeout (10
// minutes), or with one, the first answered. A window that was already
// open (the dialog in Settings, another command) is left to whoever
// opened it: neither renewed nor closed here.
func (c *meshCLI) waitAndApprove(ctx context.Context, self meshSelf, one bool) error {
	timeout := c.f.timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	open := func(on, renew bool) error {
		octx, ocancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ocancel()
		return c.call(octx, http.MethodPost, "/devices/discoverable", map[string]bool{"on": on, "renew": renew}, nil)
	}
	ours := false
	if cur, err := c.self(ctx); err != nil || cur.Mesh.DiscoverableUntil == nil || !cur.Mesh.DiscoverableUntil.After(c.now()) {
		if err := open(true, false); err != nil {
			return fmt.Errorf("open the setup window: %w", err)
		}
		ours = true
		defer func() { _ = open(false, false) }()
	}
	renew := time.NewTicker(time.Minute)
	defer renew.Stop()
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	handled := map[string]bool{}
	for {
		var reqs []contracts.PairRequest
		if err := c.call(ctx, http.MethodGet, "/devices/pair-requests", nil, &reqs); err != nil && ctx.Err() == nil {
			return err
		}
		for _, r := range reqs {
			if r.State != contracts.PairPending || handled[r.ID] || (c.f.id != "" && r.ID != c.f.id) {
				continue
			}
			// With --check-code, the request showing those digits: another
			// one ahead of it (a spoof) is left alone, not declined (R5).
			if one && c.f.checkCode != "" && r.Mode == contracts.PairModeDiscovered && r.CheckCode != strings.ReplaceAll(c.f.checkCode, " ", "") {
				continue
			}
			handled[r.ID] = true
			declined, err := c.answer(ctx, self, r)
			if err != nil {
				return err
			}
			if one {
				if declined != "" {
					return fmt.Errorf("mesh approve: declined %s: %s", paircode.CleanText(r.Name), declined)
				}
				return nil
			}
			c.printf("\nWaiting for more devices… (Ctrl-C when done)\n")
		}
		select {
		case <-ctx.Done():
			if one {
				return errors.New("mesh approve: no request came in time")
			}
			c.printf("\nDone. `mads mesh status` shows your devices.\n")
			return nil
		case <-renew.C:
			if ours {
				_ = open(true, true)
			}
		case <-poll.C:
		}
	}
}

func platformWord(p string) string {
	switch p {
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	case "ios":
		return "iPhone"
	case "android":
		return "Android"
	case "":
		return "unknown platform"
	}
	return p
}

// suggestedLevel is control for a computer, view for a phone; never admin.
func suggestedLevel(platform string) string {
	if platform == "ios" || platform == "android" {
		return "view"
	}
	return "control"
}

var levelKeys = map[string]string{"n": "none", "v": "view", "c": "chat", "o": "control"}

func levelPrompt(suggested string) string {
	parts := []string{"[n]ot use", "[v]iew", "[c]hat with", "c[o]ntrol"}
	for i, l := range []string{"none", "view", "chat", "control"} {
		if l == suggested {
			parts[i] += " (suggested)"
		}
	}
	return "  " + strings.Join(parts, "  ")
}

func keyOf(level string) string {
	for k, l := range levelKeys {
		if l == level {
			return k
		}
	}
	return "o"
}

// answer is one request's approval, at the console.
// It answers why it declined ("" approved).
func (c *meshCLI) answer(ctx context.Context, self meshSelf, r contracts.PairRequest) (string, error) {
	name := paircode.CleanText(r.Name)
	if r.Repair != nil {
		// A key already paired here (F6 R2): approving replaces its pairing.
		c.printf("\n%s (%s) wants to pair again. It's paired here now (%s); approving replaces that.\n", name, platformWord(r.Platform), levelWordCLI(r.Repair.Level))
	} else {
		c.printf("\n%s (%s) wants to join your mesh.\n", name, platformWord(r.Platform))
	}
	if r.NameCollides {
		c.printf("! A device here is already called %s: this name is its own claim.\n", name)
	}
	decline := func(why string) (string, error) {
		_ = c.call(ctx, http.MethodPost, "/devices/pair-requests/"+url.PathEscape(r.ID)+"/decline", struct{}{}, nil)
		c.printf("Declined %s: %s\n", name, why)
		return why, nil
	}
	confirmed, shownVouched := false, false
	switch {
	case r.Mode == contracts.PairModeDiscovered && r.Vouched:
		shownVouched = true
		c.printf("✓ Tailscale confirms it's signed in to your tailnet as you.\n")
	case r.Mode == contracts.PairModeDiscovered:
		switch r.VouchWithheld {
		case "another_request":
			c.printf("! Tailscale confirms a device of yours is asking, but another request is waiting too: compare the number.\n")
		case "name_collides":
			c.printf("! A device here already has this name: compare the number.\n")
		}
		code := paircode.GroupCheckCode(r.CheckCode)
		if c.f.checkCode != "" {
			if strings.ReplaceAll(c.f.checkCode, " ", "") != r.CheckCode {
				return decline("its check code isn't " + c.f.checkCode + " (another device may be asking)")
			}
			c.printf("✓ Its check code is %s, as given.\n", code)
			confirmed = true
			break
		}
		ok, err := c.yesNo("Is " + code + " on " + name + " too?")
		if errors.Is(err, errNoTerminal) {
			return "", errors.New("mesh approve: a found device's request needs its check code compared: pass --check-code with the digits it shows")
		}
		if err != nil {
			return "", err
		}
		if !ok {
			return decline("its number was different: another device may be asking")
		}
		confirmed = true
	default:
		c.printf("It presented a pairing code from this %s; its fingerprint is %s.\n", self.Name, devicekey.Fingerprint(r.Fingerprint).Grouped())
	}
	level := c.f.level
	if level == "admin" {
		return "", errors.New("mesh approve: admin is never granted by pairing; once paired, mads mesh set <device> --level admin here")
	}
	suggested := suggestedLevel(r.Platform)
	if level == "" {
		c.printf("What may it do with mads on this %s?\n%s\n", self.Name, levelPrompt(suggested))
		for {
			a, err := c.ask("Choose [" + keyOf(suggested) + "]: ")
			if errors.Is(err, errNoTerminal) {
				return "", errors.New("mesh approve: choose a level with --level (none, view, chat or control)")
			}
			if err != nil {
				return "", err
			}
			if a == "" {
				level = suggested
				break
			}
			if l, ok := levelKeys[strings.ToLower(a)]; ok {
				level = l
				break
			}
			c.printf("Choose n, v, c or o.\n")
		}
	}
	switch level {
	case "none", "view", "chat", "control":
	default:
		return "", errors.New("mesh approve: --level none, view, chat or control")
	}
	if !c.f.yes {
		what := map[string]string{
			"none":    "It may do nothing here until you choose.",
			"view":    "View lets it read sessions here.",
			"chat":    "Chat lets it message, interrupt and answer prompts in sessions here.",
			"control": "Control lets it start Full access sessions, run a shell, and commit, land\nand push on this " + self.Name + ".",
		}[level]
		ask := " Add it?"
		if r.Repair != nil {
			ask = " Pair it again?"
		}
		ok, err := c.yesNo(what + ask)
		if errors.Is(err, errNoTerminal) {
			return "", errors.New("mesh approve: confirm with --yes")
		}
		if err != nil {
			return "", err
		}
		if !ok {
			return decline("you chose not to add it")
		}
	}
	var out contracts.PairRequest
	decision := contracts.PairDecision{Level: contracts.GrantLevel(level), CheckCodeConfirmed: confirmed, ShownVouched: shownVouched}
	if err := c.call(ctx, http.MethodPost, "/devices/pair-requests/"+url.PathEscape(r.ID)+"/approve", decision, &out); err != nil {
		return "", fmt.Errorf("mesh approve: %w", err)
	}
	added := "Added"
	if r.Repair != nil {
		added = "Re-paired"
	}
	c.printf("✓ %s %s. It may %s mads on this %s.\n", added, paircode.CleanText(out.Name), verb(level), self.Name)
	return "", nil
}

// --- approve ------------------------------------------------------------------------------

func (c *meshCLI) approve(ctx context.Context) error {
	self, err := c.self(ctx)
	if err != nil {
		return fmt.Errorf("mesh approve: %w", err)
	}
	if c.f.level == "admin" {
		return errors.New("mesh approve: admin is never granted by pairing; once paired, mads mesh set <device> --level admin here")
	}
	if c.f.id != "" {
		// Idempotent: a request already answered as asked is success.
		var reqs []contracts.PairRequest
		if err := c.call(ctx, http.MethodGet, "/devices/pair-requests", nil, &reqs); err != nil {
			return fmt.Errorf("mesh approve: %w", err)
		}
		for _, r := range reqs {
			if r.ID != c.f.id {
				continue
			}
			switch {
			case r.State == contracts.PairApproved && (c.f.level == "" || levelWordCLI(r.Level) == c.f.level):
				c.printf("%s was already approved (it may %s mads here).\n", paircode.CleanText(r.Name), verb(levelWordCLI(r.Level)))
				return nil
			case r.State != contracts.PairPending:
				return fmt.Errorf("mesh approve: request %s was already %s", r.ID, r.State)
			}
		}
	}
	c.printf("Waiting for a device to ask to join (Ctrl-C to stop)…\n")
	return c.waitAndApprove(ctx, self, true)
}

func levelWordCLI(l contracts.GrantLevel) string {
	if l == contracts.GrantNone {
		return "none"
	}
	return string(l)
}

// --- add ---------------------------------------------------------------------------------

type meshFound struct {
	Name         string `json:"name"`
	Platform     string `json:"platform"`
	Fingerprint  string `json:"fingerprint"`
	Short        string `json:"short"`
	NetworkLabel string `json:"network_label"`
	Paired       bool   `json:"paired"`
	Compatible   bool   `json:"compatible"`
}

func (c *meshCLI) add(ctx context.Context, name string) error {
	self, err := c.self(ctx)
	if err != nil {
		return fmt.Errorf("mesh add: %w", err)
	}
	timeout := c.f.timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	var list struct {
		Networks []struct {
			Label string `json:"label"`
		} `json:"networks"`
		Devices     []meshFound `json:"devices"`
		HiddenCount int         `json:"hidden_count"`
		HiddenKnown bool        `json:"hidden_known"`
	}
	var nets []string
	start := c.now()
	// A first round of the search takes a few seconds: give it that before
	// settling on what's found (or as soon as the named device is).
	settle := min(5*time.Second, timeout)
	c.printf("Looking for mads devices whose setup is open…\n")
	var cands []meshFound
	for {
		if err := c.call(ctx, http.MethodGet, "/devices/discovered", nil, &list); err != nil {
			return fmt.Errorf("mesh add: %w", err)
		}
		nets = nets[:0]
		for _, n := range list.Networks {
			nets = append(nets, n.Label)
		}
		cands = cands[:0]
		for _, d := range list.Devices {
			if !d.Paired && (name == "" || strings.EqualFold(d.Name, name)) {
				cands = append(cands, d)
			}
		}
		elapsed := c.now().Sub(start)
		if (len(cands) > 0 && (name != "" || elapsed >= settle)) || elapsed >= timeout {
			break
		}
		select {
		case <-ctx.Done():
			return errors.New("mesh add: stopped")
		case <-time.After(time.Second):
		}
	}
	if len(cands) == 0 {
		where := firstNonEmptyStr(strings.Join(nets, " and "), "this device's mesh network")
		return fmt.Errorf("mesh add: no mads device with its setup open was found on %s: on that device run mads mesh setup (or open Settings → Paired devices → Add devices) on the same network, or use mads mesh code", where)
	}
	slices.SortFunc(cands, func(a, b meshFound) int { return strings.Compare(a.Name, b.Name) })
	pick := cands[0]
	if len(cands) > 1 {
		for i, d := range cands {
			c.printf("  %d) %s (%s) on %s, %s\n", i+1, paircode.CleanText(d.Name), platformWord(d.Platform), d.NetworkLabel, d.Short)
		}
		if list.HiddenKnown && list.HiddenCount > 0 {
			c.printf("  (%d other devices on the network aren't mads devices with setup open, and aren't shown.)\n", list.HiddenCount)
		}
		for {
			a, err := c.ask("Choose [1]: ")
			if errors.Is(err, errNoTerminal) {
				return errors.New("mesh add: several devices found: name one (mads mesh add NAME)")
			}
			if err != nil {
				return err
			}
			k := 1
			if a != "" {
				if k, err = strconv.Atoi(a); err != nil || k < 1 || k > len(cands) {
					c.printf("Choose 1 to %d.\n", len(cands))
					continue
				}
			}
			pick = cands[k-1]
			break
		}
	}
	if !pick.Compatible {
		return fmt.Errorf("mesh add: %s runs a mads this one can't talk to: update mads on both", paircode.CleanText(pick.Name))
	}
	var st meshJoin
	if err := c.call(ctx, http.MethodPost, "/devices/discovered/"+url.PathEscape(pick.Fingerprint)+"/request", struct{}{}, &st); err != nil {
		return fmt.Errorf("mesh add: %w", err)
	}
	host := paircode.CleanText(firstNonEmptyStr(st.Host.Name, pick.Name))
	if st.Vouched {
		c.printf("✓ Tailscale confirms %s is signed in to your tailnet as you.\nApprove this %s on %s.\n", host, self.Name, host)
	} else {
		c.printf("Check that %s shows %s, then approve there.\n", host, paircode.GroupCheckCode(st.CheckCode))
	}
	return c.followJoin(ctx, st)
}

// followJoin waits for a join's answer, asks for the check code's match
// when it's needed, and says how it ended.
func (c *meshCLI) followJoin(ctx context.Context, st meshJoin) error {
	host := paircode.CleanText(st.Host.Name)
	// Never longer than a request lives, whatever the host says (R1).
	ctx, cancel := context.WithTimeout(ctx, joinWait)
	defer cancel()
	for st.State == contracts.PairPending {
		select {
		case <-ctx.Done():
			if st.CheckCode == "" || st.Vouched {
				// The Router keeps waiting on its own: approval there still
				// pins the host here. There's no route to cancel it.
				return fmt.Errorf("mesh: stopped waiting, but the request is still with %s: approved there, it completes on this device; declined or left, nothing changes here", host)
			}
			return errors.New("mesh: stopped waiting; nothing was pinned here")
		case <-time.After(time.Second):
		}
		if err := c.call(ctx, http.MethodGet, "/devices/pair/"+url.PathEscape(st.ID), nil, &st); err != nil {
			return err
		}
	}
	if st.Confirm == "needed" {
		var ok bool
		if c.f.checkCode != "" {
			// The number the host showed, compared here: never taken on trust.
			ok = strings.ReplaceAll(c.f.checkCode, " ", "") == st.CheckCode
			if !ok {
				c.printf("%s showed %s; this device has %s.\n", host, c.f.checkCode, paircode.GroupCheckCode(st.CheckCode))
			}
		} else {
			var err error
			ok, err = c.yesNo(host + " approved. Did it show " + paircode.GroupCheckCode(st.CheckCode) + "?")
			if errors.Is(err, errNoTerminal) {
				return errors.New("mesh add: give the number " + host + " shows: --check-code DIGITS")
			}
			if err != nil {
				return err
			}
		}
		if err := c.call(ctx, http.MethodPost, "/devices/pair/"+url.PathEscape(st.ID)+"/confirm", map[string]bool{"match": ok}, &st); err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("mesh add: nothing was pinned: another device may have answered as %s. Pair with a code instead (mads mesh code on %s)", host, host)
		}
	}
	switch {
	case st.State == contracts.PairApproved && st.Error != "":
		return errors.New("mesh: " + paircode.CleanText(st.Error))
	case st.State == contracts.PairApproved:
		lets := "doesn't let this device use mads there yet"
		if st.Level != "none" && st.Level != "" {
			lets = "lets this device " + verb(st.Level) + " mads there"
		}
		c.printf("✓ Paired with %s: it %s.\nThis device lets %s do nothing here until you choose (mads mesh set %s --level <level>).\n", host, lets, host, host)
		switch {
		case st.AdoptedNetwork != nil:
			c.printf("✓ Your mesh network is now %s.\n", st.AdoptedNetwork.Label)
		case st.NotAdopted == "shared":
			c.printf("! This device's mesh network wasn't set: %s looks shared. Choose it with mads mesh setup if you mean to.\n", firstNonEmptyStr(st.Network, "that network"))
		}
		return nil
	case st.State == contracts.PairDeclined:
		return fmt.Errorf("mesh: %s declined; nothing was paired", host)
	}
	return fmt.Errorf("mesh: %s didn't answer in time; nothing was paired", host)
}

// --- code and join ------------------------------------------------------------------------

func (c *meshCLI) code(ctx context.Context) error {
	var out struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
		Host      struct {
			Name        string `json:"name"`
			Fingerprint string `json:"fingerprint"`
		} `json:"host"`
		Networks []struct {
			Label string `json:"label"`
		} `json:"networks"`
	}
	err := c.call(ctx, http.MethodPost, "/devices/pairing-code", struct{}{}, &out)
	var me *meshErr
	if errors.As(err, &me) && me.Code == "no_mesh_network" {
		return errors.New("mesh code: this device listens on no network yet, so no other device could reach it: run mads mesh setup first")
	}
	if err != nil {
		return fmt.Errorf("mesh code: %w", err)
	}
	if c.f.asJSON {
		return printJSON(c.out, out)
	}
	var nets []string
	for _, n := range out.Networks {
		nets = append(nets, n.Label)
	}
	c.printf("%s\n\nHow to find this %s (on %s). Works once, until %s (5 minutes).\n", out.Code, out.Host.Name, firstNonEmptyStr(strings.Join(nets, " and "), "its mesh network"), out.ExpiresAt.Local().Format("15:04:05"))
	c.printf("On the other device: mads mesh join <code>, or Settings → Paired devices → I have a pairing code.\n")
	c.printf("Then approve it here: mads mesh approve. This device: %s, fingerprint %s\n", out.Host.Name, devicekey.Fingerprint(out.Host.Fingerprint).Grouped())
	return nil
}

func (c *meshCLI) join(ctx context.Context, code string) error {
	if code == "" {
		if c.interactive {
			c.printf("Paste the pairing code, then Enter: ")
		}
		line, err := c.in.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("mesh join: read the code: %w", err)
		}
		code = strings.TrimSpace(line)
	}
	var st meshJoin
	err := c.call(ctx, http.MethodPost, "/devices/pair", map[string]any{"code": code, "update_addresses": c.f.update, "repair": c.f.repair}, &st)
	var me *meshErr
	if errors.As(err, &me) && me.Code == "repair_offered" {
		// A host paired here that no longer has this device paired (F6 R6).
		name, _ := me.Body["name"].(string)
		a, aerr := c.ask(paircode.CleanText(name) + " is paired here, but it doesn't have this device paired now. [r]e-pair / [c]ancel: ")
		if errors.Is(aerr, errNoTerminal) {
			return errors.New("mesh join: " + me.Msg + " (pass --repair)")
		}
		if aerr != nil || !strings.HasPrefix(strings.ToLower(a), "r") {
			return errors.New("mesh join: nothing changed")
		}
		err = c.call(ctx, http.MethodPost, "/devices/pair", map[string]any{"code": code, "repair": true}, &st)
	}
	if errors.As(err, &me) {
		switch me.Code {
		case "already_paired_update":
			name, _ := me.Body["name"].(string)
			ok := c.f.update
			if !ok {
				a, aerr := c.ask(paircode.CleanText(name) + " is already paired here. Update its addresses from this code? [Y/n] ")
				if errors.Is(aerr, errNoTerminal) {
					return errors.New("mesh join: " + me.Msg + " (pass --update)")
				}
				ok = aerr == nil && !strings.EqualFold(a, "n")
			}
			if !ok {
				return errors.New("mesh join: nothing changed")
			}
			var upd struct {
				Name string `json:"name"`
			}
			if err := c.call(ctx, http.MethodPost, "/devices/pair", map[string]any{"code": code, "update_addresses": true}, &upd); err != nil {
				return fmt.Errorf("mesh join: %w", err)
			}
			c.printf("✓ Updated %s's addresses.\n", paircode.CleanText(upd.Name))
			return nil
		case "no_shared_network":
			c.printf("%s\n\n%s", me.Msg, noSharedTips)
			return errors.New("mesh join: no network in common with that host now; the code wasn't used up")
		}
		if retry, _ := me.Body["retryable"].(bool); retry {
			// The code isn't used up (F6 R7): the same one can be tried again.
			if secs, ok := me.Body["retry_after"].(float64); ok && secs > 0 {
				return fmt.Errorf("mesh join: %s (try the same code again in %ds)", me.Msg, int(secs))
			}
			return fmt.Errorf("mesh join: %s (try the same code again)", me.Msg)
		}
		return fmt.Errorf("mesh join: %s", me.Msg)
	}
	if err != nil {
		return fmt.Errorf("mesh join: %w", err)
	}
	if st.ID == "" { // an address update answer without asking
		c.printf("✓ Updated its addresses.\n")
		return nil
	}
	host := paircode.CleanText(st.Host.Name)
	via := ""
	if st.Network != "" {
		via = " over " + st.Network
	}
	c.printf("Asked %s%s to pair with this device; approve it there (mads mesh approve) if it shows this device as %s.\n", host, via, st.Self)
	return c.followJoin(ctx, st)
}

// --- status, set, address, discoverable ------------------------------------------------------

func (c *meshCLI) status(ctx context.Context) error {
	self, err := c.self(ctx)
	if err != nil {
		return fmt.Errorf("mesh status: %w", err)
	}
	var devices []contracts.DeviceStatus
	if err := c.call(ctx, http.MethodGet, "/devices", nil, &devices); err != nil {
		return fmt.Errorf("mesh status: %w", err)
	}
	if c.f.asJSON {
		return printJSON(c.out, map[string]any{"self": self, "devices": devices})
	}
	c.printf("This device: %s (%s), fingerprint %s\n", self.Name, platformWord(self.Platform), self.Short)
	var chosen []meshNetView
	for _, n := range self.Mesh.Networks {
		if n.Chosen {
			chosen = append(chosen, n)
		}
	}
	switch {
	case len(chosen) == 0:
		c.printf("Mesh network: none yet. Run mads mesh setup.\n")
	default:
		for _, n := range chosen {
			state := "reachable"
			switch {
			case !n.Available:
				state = "this " + self.Name + " isn't on it now"
			case !n.Listening && self.Mesh.ServiceSeen:
				// The real reason when there is one (round-2 T7).
				state = "not listening yet"
				if n.Error != "" {
					state = "not listened on: " + n.Error
				}
			case !self.Mesh.ServiceSeen:
				state = "the Mesh Service hasn't reported (is it running?)"
			}
			c.printf("Mesh network: %s — %s\n", n.Title, state)
			if n.ID == "tailscale" {
				check := "off"
				if self.Mesh.TailnetCheck {
					check = "on"
				}
				if self.Mesh.TailnetCheckStatus == "unavailable" {
					check = "on, but tailscaled can't be asked: every connection on Tailscale is refused"
				}
				c.printf("  Tailscale identity check: %s\n", check)
			}
		}
	}
	if self.Mesh.Public {
		c.printf("  Also reachable from the internet.\n")
	}
	c.tailscaleLines(self)
	if len(devices) == 0 {
		c.printf("No paired devices.\n")
		return nil
	}
	w := tabwriter.NewWriter(c.out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "DEVICE\tMAY HERE\tLETS THIS DEVICE\tSTATUS")
	for _, d := range devices {
		lets := "-"
		if d.Remote != nil && d.Remote.AsOf != nil {
			lets = levelWordCLI(d.Remote.Level)
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", paircode.CleanText(d.Name), levelWordCLI(d.Level()), lets, statusWord(d))
	}
	return w.Flush()
}

// statusWord is a device's status in one phrase, as the UI's Status column.
func statusWord(d contracts.DeviceStatus) string {
	if d.PairingThere == contracts.PairingThereNotPaired {
		// F6 R3: it answered, and doesn't have this device paired.
		return "No longer paired there: pair again (mads mesh join, or Add devices)"
	}
	switch d.Diagnosis {
	case contracts.DiagnosisTailscaleSignedOut:
		return "Probably signed out of Tailscale"
	case contracts.DiagnosisTailscaleKeyExpiring:
		if d.Remote != nil && d.Remote.TailscaleKeyExpiry != nil {
			return "Connected · Tailscale sign-in expires " + d.Remote.TailscaleKeyExpiry.Local().Format("Jan 2")
		}
	}
	switch d.Reachability {
	case contracts.PeerConnected:
		if d.Path != nil {
			s := "Connected via " + firstNonEmptyStr(d.Path.Label, d.Path.Kind)
			if d.Path.RTT > 0 {
				s += " · " + strconv.FormatInt(d.Path.RTT, 10) + " ms"
			}
			return s
		}
		return "Connected"
	case contracts.PeerNoSharedNetwork:
		return "Not reachable from this network"
	case contracts.PeerUnreachable:
		return "Offline"
	case contracts.PeerDenied:
		return "Doesn't let this device use mads"
	case contracts.PeerVersionMismatch:
		return "Runs another mads version"
	case contracts.PeerUnknown:
		return "Not reached yet"
	case "":
		if d.LastUsedAt != nil {
			return "Last used this device " + d.LastUsedAt.Local().Format("Jan 2 15:04")
		}
		return "-"
	}
	return string(d.Reachability)
}

// deviceFP finds a device by name or fingerprint.
func (c *meshCLI) deviceFP(ctx context.Context, key string) (contracts.DeviceStatus, error) {
	var devices []contracts.DeviceStatus
	if err := c.call(ctx, http.MethodGet, "/devices", nil, &devices); err != nil {
		return contracts.DeviceStatus{}, err
	}
	for _, d := range devices {
		if d.Name == key || d.Fingerprint == key {
			return d, nil
		}
		if f, err := devicekey.ParseFingerprint(key); err == nil && d.Fingerprint == string(f) {
			return d, nil
		}
	}
	return contracts.DeviceStatus{}, fmt.Errorf("no paired device %q here (mads mesh status lists them)", key)
}

func (c *meshCLI) set(ctx context.Context, key string) error {
	switch c.f.level {
	case "none", "view", "chat", "control", "admin":
	default:
		return errors.New("mesh set: --level none, view, chat, control or admin")
	}
	d, err := c.deviceFP(ctx, key)
	if err != nil {
		return fmt.Errorf("mesh set: %w", err)
	}
	if err := c.call(ctx, http.MethodPut, "/devices/"+url.PathEscape(d.Fingerprint)+"/grant", map[string]string{"level": c.f.level}, nil); err != nil {
		return fmt.Errorf("mesh set: %w", err)
	}
	c.printf("%s may %s mads here now.\n", paircode.CleanText(d.Name), verb(c.f.level))
	return nil
}

func (c *meshCLI) address(ctx context.Context, add bool, key, addr string) error {
	d, err := c.deviceFP(ctx, key)
	if err != nil {
		return fmt.Errorf("mesh address: %w", err)
	}
	if add {
		if err := c.call(ctx, http.MethodPost, "/devices/"+url.PathEscape(d.Fingerprint)+"/addresses", map[string]string{"addr": addr}, nil); err != nil {
			return fmt.Errorf("mesh address add: %w", err)
		}
		c.printf("This device will also try %s for %s.\n", addr, paircode.CleanText(d.Name))
		return nil
	}
	if err := c.call(ctx, http.MethodDelete, "/devices/"+url.PathEscape(d.Fingerprint)+"/addresses/"+url.PathEscape(addr), nil, nil); err != nil {
		return fmt.Errorf("mesh address remove: %w", err)
	}
	c.printf("Removed %s for %s.\n", addr, paircode.CleanText(d.Name))
	return nil
}

func (c *meshCLI) discoverable(ctx context.Context) error {
	var out struct {
		On    bool      `json:"on"`
		Until time.Time `json:"until"`
	}
	if err := c.call(ctx, http.MethodPost, "/devices/discoverable", map[string]bool{"on": !c.f.off}, &out); err != nil {
		return fmt.Errorf("mesh discoverable: %w", err)
	}
	if !out.On {
		c.printf("This device's setup window is closed.\n")
		return nil
	}
	c.printf("Other mads devices on this device's mesh network can find it until %s (Add devices there). mads mesh approve answers their requests.\n", out.Until.Local().Format("15:04:05"))
	return nil
}
