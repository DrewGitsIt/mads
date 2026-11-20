// Command perfseed writes perf-harness workloads (docs/PERF-HARNESS-PLAN.md).
//
//	perfseed seed -db <fresh.db> -profile <profile.json> -repo <dir> [-manifest <out.json>]
//	perfseed calibrate -db <existing.db> -out <profile.json> [-name L]
//	perfseed scale -in <profile.json> -out <profile.json> -name XL -events 1000000 [-records 1.5]
//
// seed writes a fresh database before any Data Service starts, through the
// store's own write paths (record stores and Log.WithTx / Tx.Append), so
// rows and events agree. calibrate opens a database strictly read-only and
// writes only per-type counts and payload sizes, never content.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "seed":
		err = runSeed(os.Args[2:])
	case "calibrate":
		err = runCalibrate(os.Args[2:])
	case "scale":
		err = runScale(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "perfseed:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: perfseed seed|calibrate|scale [flags]")
	os.Exit(2)
}

func runSeed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	db := fs.String("db", "", "path of the fresh database to write (must not exist)")
	prof := fs.String("profile", "", "workload profile JSON")
	repo := fs.String("repo", "", "workspace repo directory (every session's cwd)")
	manifest := fs.String("manifest", "", "where to write the manifest JSON (default: stdout)")
	now := fs.String("now", "", "RFC 3339 anchor for event timestamps (default: now)")
	_ = fs.Parse(args)
	if *db == "" || *prof == "" || *repo == "" {
		return fmt.Errorf("seed: -db, -profile and -repo are required")
	}
	p, err := loadProfile(*prof)
	if err != nil {
		return err
	}
	opt := SeedOptions{RepoDir: *repo}
	if *now != "" {
		if opt.Now, err = time.Parse(time.RFC3339, *now); err != nil {
			return fmt.Errorf("seed: -now: %w", err)
		}
	}
	m, err := Seed(context.Background(), *db, p, opt)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "perfseed: %s: %d events, %.1f MB payload, %d sessions, in %.1fs\n",
		p.Name, m.TotalEvents, float64(m.TotalBytes)/1e6, len(m.Sessions), float64(m.ElapsedMS)/1000)
	for _, s := range m.Skipped {
		fmt.Fprintln(os.Stderr, "perfseed: skipped", s)
	}
	if *manifest == "" {
		return writeJSONTo(os.Stdout, m)
	}
	return writeJSON(*manifest, m)
}

func runCalibrate(args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ExitOnError)
	dbPath := fs.String("db", "", "database to read (opened read-only)")
	out := fs.String("out", "", "profile JSON to write")
	name := fs.String("name", "L", "profile name")
	_ = fs.Parse(args)
	if *dbPath == "" || *out == "" {
		return fmt.Errorf("calibrate: -db and -out are required")
	}
	abs, _ := filepath.Abs(*out)
	if dbAbs, _ := filepath.Abs(*dbPath); filepath.Dir(abs) == filepath.Dir(dbAbs) {
		return fmt.Errorf("calibrate: refusing to write next to the database it reads")
	}
	db, err := openReadOnly(*dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	p, err := calibrate(context.Background(), db, *name)
	if err != nil {
		return err
	}
	return p.write(*out)
}

func runScale(args []string) error {
	fs := flag.NewFlagSet("scale", flag.ExitOnError)
	in := fs.String("in", "", "profile to scale")
	out := fs.String("out", "", "profile to write")
	name := fs.String("name", "", "new profile name")
	events := fs.Int("events", 0, "target total event count")
	records := fs.Float64("records", 0, "record-count factor (default: the event factor's square root)")
	_ = fs.Parse(args)
	if *in == "" || *out == "" || *name == "" || *events <= 0 {
		return fmt.Errorf("scale: -in, -out, -name and -events are required")
	}
	p, err := loadProfile(*in)
	if err != nil {
		return err
	}
	f := float64(*events) / float64(p.TotalEvents())
	rf := *records
	if rf == 0 {
		rf = math.Sqrt(f)
	}
	q := p.Scaled(*name, f, rf)
	q.Description = fmt.Sprintf("%s scaled ×%.3f (events) ×%.3f (records)", p.Name, f, rf)
	if err := q.validate(); err != nil {
		return err
	}
	return q.write(*out)
}

func writeJSONTo(f *os.File, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}
