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
	"strings"
	"text/tabwriter"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/paircode"
)

// Pairing by code and device management through the Router
// (docs/MESH-AUTH-DESIGN.md D6, D8): `mads pair <subcommand>`. The flag
// forms (--accept, --list, --revoke) stay as they were, straight to the
// Data Service.

const pairCodeUsage = `mads pair — pair devices by code, and manage them (docs/MESH-AUTH-DESIGN.md D6)

On the host (the device the other one should reach):
  mads pair code [--json]                   make a single-use pairing code (5 minutes)
  mads pair requests [--json]               devices that presented a code, waiting
  mads pair approve ID --grant LEVEL [--name NAME]
                                            let one in: LEVEL is none, view, chat or
                                            control (admin is set afterwards, here only)
  mads pair decline ID

On the joiner (the device that reaches the host):
  mads pair join [CODE] [--wait] [--json]   present a host's code; without CODE (or
                                            with -), read it from stdin, which keeps it
                                            out of the process list

Either:
  mads pair remove NAME|FINGERPRINT [--everywhere] [--also-introduced]
                                            remove a device here; --everywhere also on
                                            every host where this device administers;
                                            --also-introduced also the devices it
                                            introduced

All take -router URL (default http://localhost:17373). The pin/accept forms:
  mads pair --accept | --list | --revoke    (mads pair --help)
`

func isPairSubcommand(s string) bool {
	switch s {
	case "code", "join", "requests", "approve", "decline", "remove":
		return true
	}
	return false
}

// cmdPairSub runs `mads pair <subcommand>`.
func cmdPairSub(args []string, stdin io.Reader, stdout io.Writer) error {
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("pair "+sub, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, pairCodeUsage) }
	router := fs.String("router", envOr("MADS_ROUTER_URL", defaultRouterURL), "Router base URL")
	asJSON := fs.Bool("json", false, "print JSON")
	wait := fs.Bool("wait", false, "join: wait for the host's answer")
	grant := fs.String("grant", "", "approve: none, view, chat or control")
	name := fs.String("name", "", "approve: the name to pin the device under")
	everywhere := fs.Bool("everywhere", false, "remove: also on every host where this device administers")
	alsoIntroduced := fs.Bool("also-introduced", false, "remove: also the devices it introduced (a compromised admin's)")
	// Flags may come after the positional argument (mads pair approve ID --grant chat).
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
	base := strings.TrimRight(*router, "/")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	call := func(method, path string, in, out any) error { return routerJSON(ctx, method, base+path, in, out) }
	one := func(what string) (string, error) {
		if len(pos) != 1 {
			fs.Usage()
			return "", fmt.Errorf("pair %s: needs %s", sub, what)
		}
		return pos[0], nil
	}

	switch sub {
	case "code":
		var out struct {
			ID        string    `json:"id"`
			Code      string    `json:"code"`
			ExpiresAt time.Time `json:"expires_at"`
			Host      struct {
				Name        string `json:"name"`
				Fingerprint string `json:"fingerprint"`
				MeshURL     string `json:"mesh_url"`
			} `json:"host"`
		}
		if err := call(http.MethodPost, "/devices/pairing-code", struct{}{}, &out); err != nil {
			return fmt.Errorf("pair code: %w", err)
		}
		if *asJSON {
			return printJSON(stdout, out)
		}
		_, err := fmt.Fprintf(stdout, `%s

Works once, until %s (5 minutes). On the other device, paste it:
  mads pair join        (then paste), or Settings → Paired devices → Pair a device → Paste a pairing code.
Then answer it here: mads pair requests, mads pair approve <id> --grant <level>.
This host: %s, %s, fingerprint %s
`, out.Code, out.ExpiresAt.Local().Format("15:04:05"), out.Host.Name, out.Host.MeshURL, devicekey.Fingerprint(out.Host.Fingerprint).Grouped())
		return err

	case "join":
		code := ""
		if len(pos) > 1 {
			return errors.New("pair join: one code")
		}
		if len(pos) == 1 && pos[0] != "-" {
			code = pos[0]
		} else {
			if f, ok := stdin.(*os.File); ok && isTerminal(f) {
				fmt.Fprint(os.Stderr, "Paste the pairing code, then Enter: ")
			}
			line, err := bufio.NewReader(io.LimitReader(stdin, 64<<10)).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("pair join: read the code: %w", err)
			}
			code = line
		}
		var st joinStatus
		if err := call(http.MethodPost, "/devices/pair", map[string]string{"code": strings.TrimSpace(code)}, &st); err != nil {
			return fmt.Errorf("pair join: %w", err)
		}
		if !*wait {
			if *asJSON {
				return printJSON(stdout, st)
			}
			_, err := fmt.Fprintf(stdout, "asked %s to pair with this device (request %s); approve it there if it shows this device as %s.\nTo follow: mads pair join --wait, or Settings → Paired devices.\n", paircode.CleanText(st.Host.Name), st.ID, st.Self)
			return err
		}
		if !*asJSON {
			fmt.Fprintf(os.Stderr, "waiting for %s to approve this device (request %s); its prompt should show this device as %s…\n", paircode.CleanText(st.Host.Name), st.ID, st.Self)
		}
		for st.State == contracts.PairPending {
			select {
			case <-ctx.Done():
				return errors.New("pair join: gave up waiting")
			case <-time.After(2 * time.Second):
			}
			if err := call(http.MethodGet, "/devices/pair/"+url.PathEscape(st.ID), nil, &st); err != nil {
				return fmt.Errorf("pair join: %w", err)
			}
		}
		if *asJSON {
			if err := printJSON(stdout, st); err != nil {
				return err
			}
		}
		switch {
		case st.State == contracts.PairApproved && st.Error != "":
			return errors.New("pair join: " + paircode.CleanText(st.Error))
		case st.State == contracts.PairApproved:
			if !*asJSON {
				lets := "doesn't let this device use mads there yet"
				if st.Level != "none" {
					lets = "lets this device " + verb(st.Level) + " mads there"
				}
				_, _ = fmt.Fprintf(stdout, "paired with %s: it %s.\nThis device lets %s do nothing here until you choose (Settings → Paired devices, or mads pair --accept %s --grant <level>).\n",
					paircode.CleanText(st.Host.Name), lets, paircode.CleanText(st.Host.Name), devicekey.Fingerprint(st.Host.Fingerprint).Grouped())
			}
			return nil
		case st.State == contracts.PairDeclined:
			return fmt.Errorf("pair join: %s declined; nothing was paired", paircode.CleanText(st.Host.Name))
		default:
			return fmt.Errorf("pair join: %s didn't answer in time; nothing was paired. Ask for a new code", paircode.CleanText(st.Host.Name))
		}

	case "requests":
		var list []contracts.PairRequest
		if err := call(http.MethodGet, "/devices/pair-requests", nil, &list); err != nil {
			return fmt.Errorf("pair requests: %w", err)
		}
		if *asJSON {
			if list == nil {
				list = []contracts.PairRequest{}
			}
			return printJSON(stdout, list)
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tNAME\tPLATFORM\tFINGERPRINT\tFROM\tSTATE\tEXPIRES")
		for _, r := range list {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, paircode.CleanText(r.Name), firstNonEmptyStr(r.Platform, "-"),
				devicekey.Fingerprint(r.Fingerprint).Short(), firstNonEmptyStr(r.Remote, "-"), r.State, r.ExpiresAt.Local().Format("15:04:05"))
		}
		return tw.Flush()

	case "approve":
		id, err := one("a request ID (mads pair requests)")
		if err != nil {
			return err
		}
		switch *grant {
		case "none", "view", "chat", "control":
		case "admin":
			return errors.New("pair approve: admin isn't granted by pairing; once paired, mads pair --accept <fingerprint> --grant admin here")
		default:
			return errors.New("pair approve: --grant none, view, chat or control")
		}
		var out contracts.PairRequest
		if err := call(http.MethodPost, "/devices/pair-requests/"+url.PathEscape(id)+"/approve", contracts.PairDecision{Level: contracts.GrantLevel(*grant), Name: *name}, &out); err != nil {
			return fmt.Errorf("pair approve: %w", err)
		}
		if *asJSON {
			return printJSON(stdout, out)
		}
		_, err = fmt.Fprintf(stdout, "paired %s (%s): it may %s mads here\n", out.Name, devicekey.Fingerprint(out.Fingerprint).Short(), verb(*grant))
		return err

	case "decline":
		id, err := one("a request ID (mads pair requests)")
		if err != nil {
			return err
		}
		var out contracts.PairRequest
		if err := call(http.MethodPost, "/devices/pair-requests/"+url.PathEscape(id)+"/decline", struct{}{}, &out); err != nil {
			return fmt.Errorf("pair decline: %w", err)
		}
		_, err = fmt.Fprintf(stdout, "declined %s (%s)\n", out.Name, devicekey.Fingerprint(out.Fingerprint).Short())
		return err

	case "remove":
		key, err := one("a device's name or fingerprint")
		if err != nil {
			return err
		}
		var devices []contracts.DeviceStatus
		if err := call(http.MethodGet, "/devices", nil, &devices); err != nil {
			return fmt.Errorf("pair remove: %w", err)
		}
		var fp string
		for _, d := range devices {
			if d.Name == key || d.Fingerprint == key {
				fp = d.Fingerprint
			}
			if f, err := devicekey.ParseFingerprint(key); err == nil && d.Fingerprint == string(f) {
				fp = d.Fingerprint
			}
		}
		if fp == "" {
			return fmt.Errorf("pair remove: no paired device %q here", key)
		}
		if !*everywhere {
			q := ""
			if *alsoIntroduced {
				q = "?also_introduced=1"
			}
			var out struct {
				IntroducedByThis []struct{ Name string } `json:"introduced_by_this"`
				AlsoRemoved      []struct{ Name string } `json:"also_removed"`
			}
			if err := call(http.MethodDelete, "/devices/"+fp+q, nil, &out); err != nil {
				return fmt.Errorf("pair remove: %w", err)
			}
			_, _ = fmt.Fprintf(stdout, "removed %s here\n", key)
			for _, d := range out.AlsoRemoved {
				_, _ = fmt.Fprintf(stdout, "  and %s, which it introduced\n", d.Name)
			}
			if len(out.IntroducedByThis) > len(out.AlsoRemoved) {
				_, _ = fmt.Fprintf(stdout, "It introduced %d device(s) here; --also-introduced removes them too.\n", len(out.IntroducedByThis))
			}
			return nil
		}
		var raw json.RawMessage
		if err := call(http.MethodPost, "/devices/"+fp+"/remove-everywhere", map[string]bool{"also_introduced": *alsoIntroduced}, &raw); err != nil {
			return fmt.Errorf("pair remove: %w", err)
		}
		if *asJSON {
			// The Router's whole answer: the device, each host with its
			// fingerprint and result, what it can't manage, what it
			// introduced.
			return printJSON(stdout, raw)
		}
		var res struct {
			Here  string `json:"here"`
			Hosts []struct {
				Name   string `json:"name"`
				Result string `json:"result"`
				Error  string `json:"error"`
			} `json:"hosts"`
			CannotManage []struct {
				Name  string `json:"name"`
				Level string `json:"level"`
			} `json:"cannot_manage"`
			AlsoRemoved []struct {
				Name string `json:"name"`
			} `json:"also_removed"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return fmt.Errorf("pair remove: %w", err)
		}
		_, _ = fmt.Fprintf(stdout, "%s: %s here\n", key, res.Here)
		failed := false
		for _, h := range res.Hosts {
			line := "  " + paircode.CleanText(h.Name) + ": " + h.Result
			if h.Error != "" {
				line += " (" + paircode.CleanText(h.Error) + ")"
			}
			if h.Result != "removed" && h.Result != "skipped" {
				failed = true
			}
			_, _ = fmt.Fprintln(stdout, line)
		}
		for _, h := range res.CannotManage {
			_, _ = fmt.Fprintf(stdout, "  %s: not yours to manage; remove it there\n", paircode.CleanText(h.Name))
		}
		for _, d := range res.AlsoRemoved {
			_, _ = fmt.Fprintf(stdout, "  and %s here, which it introduced\n", paircode.CleanText(d.Name))
		}
		_, _ = fmt.Fprintln(stdout, "Devices this one has never seen can't be checked: if it was paired anywhere else, remove it there too.")
		if failed {
			return errors.New("pair remove: not removed everywhere; retry when those hosts are back")
		}
		return nil
	}
	fs.Usage()
	return fmt.Errorf("pair: unknown subcommand %q", sub)
}

type joinStatus struct {
	ID        string              `json:"id"`
	RequestID string              `json:"request_id"`
	State     contracts.PairState `json:"state"`
	Level     string              `json:"level,omitempty"`
	Host      struct {
		Name        string `json:"name"`
		Fingerprint string `json:"fingerprint"`
		MeshURL     string `json:"mesh_url,omitempty"`
	} `json:"host"`
	ExpiresAt time.Time `json:"expires_at"`
	PeerID    string    `json:"peer_id,omitempty"`
	Error     string    `json:"error,omitempty"`
	Self      string    `json:"self"`
}

func verb(level string) string {
	switch level {
	case "none", "":
		return "not use"
	case "chat":
		return "chat with"
	case "admin":
		return "administer"
	}
	return level
}

func firstNonEmptyStr(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// isTerminal reports whether f is a character device (a terminal).
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// routerJSON sends one JSON request to the Router with the install token
// and decodes the answer; a non-2xx answer is an error carrying the
// Router's message.
func routerJSON(ctx context.Context, method, u string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := routerClient().Do(req)
	if err != nil {
		return fmt.Errorf("reach the Router: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("%s %s: %s", method, req.URL.Path, resp.Status)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
