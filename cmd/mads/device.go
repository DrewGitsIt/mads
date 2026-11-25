package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/localauth"
)

const deviceUsage = `mads device — this device's identity (docs/MESH-AUTH-DESIGN.md D1)

  mads device [--json]

Prints this device's name and fingerprint: the device ID another host pins
when it pairs this one (mads pair --accept on that host). The key pair is
$MADS_HOME/device.key, created on first use and never replaced; the private
key is never printed. --json adds the public key.
`

// deviceName is this machine's short hostname, the name mesh mode gives
// its own sessions (the Router's -mesh-name default).
func deviceName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "this device"
	}
	h, _, _ = strings.Cut(h, ".")
	return h
}

// cmdDevice is `mads device` (A1).
func cmdDevice(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("device", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, deviceUsage) }
	asJSON := fs.Bool("json", false, "print JSON, with the public key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("device: unexpected argument %s", fs.Arg(0))
	}
	id, err := devicekey.LoadOrCreate(localauth.DefaultHome())
	if err != nil {
		return fmt.Errorf("device: %w", err)
	}
	fp := id.Fingerprint()
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]string{
			"name":        deviceName(),
			"fingerprint": string(fp),
			"short":       fp.Short(),
			"public_key":  devicekey.EncodePublicKey(id.PublicKey()),
		})
	}
	_, err = fmt.Fprintf(stdout, `name         %s
fingerprint  %s
short        %s

To let this device reach a host, run there:
  mads pair --accept %s --name %s
`, deviceName(), fp.Grouped(), fp.Short(), fp.Grouped(), deviceName())
	return err
}

const pairUsage = `mads pair — the devices paired with this host (docs/MESH-AUTH-DESIGN.md D3, D6)

  mads pair --accept FINGERPRINT [--name NAME] [--grant view|chat|control|admin|none]
            [--view [--mesh-url URL]] [-data URL]
  mads pair --list [--json] [-data URL]
  mads pair --revoke FINGERPRINT|NAME [-data URL]
  mads pair code | join | requests | approve | decline | remove ...
                       pairing by code, through the Router (mads pair code --help)

--accept pins a device by its fingerprint (mads device on that device prints
it), like a line in authorized_keys. Run it on this host as the user mads runs
as; it is scriptable. A new device gets --grant view unless told otherwise.
Accepting a fingerprint already paired updates it: --name renames, --grant
changes what it may do here. --name naming a device this host views but hasn't
paired pairs that row. admin is only granted this way, or from this host's own
app: never remotely (Q2).

--view also makes this host view the device (its sessions in All devices); a
device not yet viewed needs --mesh-url, its Mesh Service (http://host:17380;
always dialled with TLS, pinned to the fingerprint). To only view a device,
not let it in here, add --grant none.

--revoke unpairs a device. A device this host also views stays viewed; one it
doesn't is removed.

Grants are enforced: this host's Mesh Service admits a device only by the key
pinned here (mutual TLS), at its grant, and by default only from this node's
own tailnet devices too (-require-tailnet). A revoke or grant change ends
that device's open streams.
`

// cmdPair is `mads pair` (A3; Q2's scripted accept).
func cmdPair(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, pairUsage) }
	accept := fs.String("accept", "", "pin the device with this fingerprint")
	list := fs.Bool("list", false, "list this host's devices")
	revoke := fs.String("revoke", "", "unpair the device with this fingerprint or name")
	name := fs.String("name", "", "the device's name here (default: from its fingerprint)")
	grant := fs.String("grant", "", "what it may do here: view, chat, control, admin or none (new devices: view)")
	view := fs.Bool("view", false, "also view the device from this host")
	meshURL := fs.String("mesh-url", "", "the device's Mesh Service, with --view")
	asJSON := fs.Bool("json", false, "with --list, print JSON")
	dataURL := fs.String("data", envOr("MADS_DATA_URL", "http://localhost:17374"), "Data Service URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	modes := 0
	for _, on := range []bool{*accept != "", *list, *revoke != ""} {
		if on {
			modes++
		}
	}
	if modes != 1 || fs.NArg() > 0 {
		fs.Usage()
		return errors.New("pair: one of --accept, --list or --revoke")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data := dataClient(*dataURL, "user")
	peers, err := data.Peers(ctx)
	if err != nil {
		return fmt.Errorf("pair: list devices: %w", err)
	}

	switch {
	case *list:
		return printPeers(stdout, peers, *asJSON)

	case *revoke != "":
		p, ok := findPeer(peers, *revoke)
		if !ok || !p.IsPaired() {
			return fmt.Errorf("pair: no paired device %q (mads pair --list shows them)", *revoke)
		}
		short := devicekey.Fingerprint(p.Fingerprint).Short()
		// Conditional on the row as listed: a change made meanwhile (the
		// app, another mads pair) fails the write instead of being undone.
		if p.IsViewed() {
			p.Fingerprint, p.PublicKey, p.Grant, p.Pairing = "", "", nil, nil
			err = data.UpdatePeerIfUnchanged(ctx, &p)
		} else {
			err = data.DeletePeerIfUnchanged(ctx, p)
		}
		if err != nil {
			return fmt.Errorf("pair: revoke %s: %w", p.Name, err)
		}
		_, err = fmt.Fprintf(stdout, "unpaired %s (%s)\n", p.Name, short)
		return err
	}

	fp, err := devicekey.ParseFingerprint(*accept)
	if err != nil {
		return fmt.Errorf("pair: --accept: %w", err)
	}
	var level contracts.GrantLevel
	setGrant := *grant != ""
	if setGrant && *grant != "none" {
		if level = contracts.GrantLevel(*grant); !level.Valid() {
			return fmt.Errorf("pair: --grant %q: must be view, chat, control, admin or none", *grant)
		}
	}
	if *meshURL != "" && !*view {
		return errors.New("pair: --mesh-url goes with --view")
	}
	// This host's own key never needs pinning here; accepting it would let
	// anything holding this install's key in as a remote device. No key
	// yet can't match; a key file that can't be read (loose mode, wrong
	// owner) fails the accept rather than skipping the check.
	switch self, err := devicekey.Load(localauth.DefaultHome()); {
	case errors.Is(err, devicekey.ErrNoKey):
	case err != nil:
		return fmt.Errorf("pair: can't check %s isn't this device's own key: %w", fp.Short(), err)
	case self.Fingerprint() == fp:
		return errors.New("pair: that's this device's own fingerprint; run mads pair on the host the other device should reach")
	}

	var row *contracts.Peer
	for i := range peers {
		if peers[i].Fingerprint == string(fp) {
			row = &peers[i]
		}
	}
	if row == nil && *name != "" {
		if p, ok := findPeer(peers, *name); ok {
			if p.IsPaired() {
				return fmt.Errorf("pair: %q is already paired with another device (%s); pick another --name, or --revoke it first", p.Name, devicekey.Fingerprint(p.Fingerprint).Short())
			}
			row = &p
		}
	}
	created := row == nil
	if created {
		no := false
		row = &contracts.Peer{Name: *name, Viewed: &no}
		if row.Name == "" {
			row.Name = "device-" + strings.ToLower(string(fp[:8]))
		}
	} else if *name != "" {
		row.Name = *name
	}
	if row.Fingerprint != string(fp) {
		row.Fingerprint, row.PublicKey = string(fp), ""
		row.Pairing = &contracts.PeerPairing{How: contracts.PairAccept}
		if !setGrant {
			row.Grant = &contracts.PeerGrant{Level: contracts.GrantView}
		}
	}
	if setGrant {
		switch {
		case level == contracts.GrantNone && (row.Grant == nil || row.Grant.Ceilings == contracts.PeerCeilings{}):
			row.Grant = nil
		case level == contracts.GrantNone:
			row.Grant.Level = level // none keeps its limits for when it's raised again
		case row.Grant == nil:
			row.Grant = &contracts.PeerGrant{Level: level}
		default:
			row.Grant.Level = level
		}
	}
	if *view {
		yes := true
		row.Viewed = &yes
		if *meshURL != "" {
			row.MeshURL = strings.TrimSuffix(*meshURL, "/")
		}
	}
	if created {
		err = data.CreatePeer(ctx, row)
	} else {
		err = data.UpdatePeerIfUnchanged(ctx, row)
	}
	if err != nil {
		return fmt.Errorf("pair: %w", err)
	}
	level = row.Level()
	shown := string(level)
	if level == contracts.GrantNone {
		shown = "none"
	}
	if row.IsViewed() {
		shown += ", viewed"
	}
	_, err = fmt.Fprintf(stdout, "paired %s (%s): %s\n", row.Name, fp.Short(), shown)
	return err
}

// findPeer resolves a fingerprint (any printed form) or a name.
func findPeer(peers []contracts.Peer, key string) (contracts.Peer, bool) {
	if fp, err := devicekey.ParseFingerprint(key); err == nil {
		for _, p := range peers {
			if p.Fingerprint == string(fp) {
				return p, true
			}
		}
	}
	for _, p := range peers {
		if p.Name == key {
			return p, true
		}
	}
	return contracts.Peer{}, false
}

func printPeers(stdout io.Writer, peers []contracts.Peer, asJSON bool) error {
	if asJSON {
		if peers == nil {
			peers = []contracts.Peer{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(peers)
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tFINGERPRINT\tGRANT\tVIEWED\tPAIRED")
	for _, p := range peers {
		fp, level, paired := "-", "-", "-"
		if p.IsPaired() {
			fp, level = devicekey.Fingerprint(p.Fingerprint).Short(), "none"
			if l := p.Level(); l != contracts.GrantNone {
				level = string(l)
			}
			if p.Pairing != nil {
				paired = p.Pairing.PairedAt.Local().Format("2006-01-02 15:04") + " " + string(p.Pairing.How)
			}
		}
		viewed := "no"
		if p.IsViewed() {
			viewed = "yes"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Name, fp, level, viewed, paired)
	}
	return tw.Flush()
}
