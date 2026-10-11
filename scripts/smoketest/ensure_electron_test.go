package smoketest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The darwin branches of scripts/ensure-electron.sh, exercised on any host:
// `uname` is a PATH stub, and ui/node_modules/electron is a fake tree next to
// a copy of the script (it resolves the package from its own location).
// node, ditto and unshare are stubs too, each logging its argv so the test
// can see which path the script took.

const fakeElectronVersion = "44.5.0"

type electronFixture struct {
	root, pkg, log string
	env            []string
}

func newElectronFixture(t *testing.T, osName string, installJSFails bool) *electronFixture {
	t.Helper()
	root := t.TempDir()
	f := &electronFixture{
		root: root,
		pkg:  filepath.Join(root, "ui", "node_modules", "electron"),
		log:  filepath.Join(root, "calls.log"),
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(installSh), "ensure-electron.sh"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "scripts", "ensure-electron.sh"), string(src), 0o755)
	writeFile(t, filepath.Join(f.pkg, "package.json"), `{"version":"`+fakeElectronVersion+`"}`, 0o644)
	writeFile(t, filepath.Join(f.pkg, "install.js"), "", 0o644)

	installExit := "0"
	if installJSFails {
		installExit = "1"
	}
	bin := filepath.Join(root, "stubbin")
	stub := func(name, body string) {
		writeFile(t, filepath.Join(bin, name),
			"#!/bin/sh\necho \""+name+" $*\" >> '"+f.log+"'\n"+body, 0o755)
	}
	stub("uname", "echo "+osName+"\n")
	stub("unshare", "exit 0\n")
	stub("node", `case "$1" in
  -p) echo `+fakeElectronVersion+` ;;
  -e) echo /cache/electron-v`+fakeElectronVersion+`-darwin-arm64.zip ;;
  *)  exit `+installExit+` ;;
esac
`)
	// ditto -x -k <zip> <dest>: lay down the bundle a real extraction would.
	// ditto <src> <dst> (mads_brand_app's copy): copy the tree.
	stub("ditto", `if [ "$1" != -x ]; then cp -R "$1" "$2"; exit; fi
mkdir -p "$4/Electron.app/Contents/MacOS"
touch "$4/Electron.app/Contents/MacOS/Electron"
echo v`+fakeElectronVersion+` > "$4/version"
`)
	stub("plutil", "exit 0\n")
	stub("codesign", `exit "${FAKE_CODESIGN_EXIT:-0}"`+"\n")
	f.env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}

// installDarwinTree writes the healthy end state of a darwin install.
func (f *electronFixture) installDarwinTree(t *testing.T) {
	t.Helper()
	writeFile(t, filepath.Join(f.pkg, "path.txt"), "Electron.app/Contents/MacOS/Electron", 0o644)
	writeFile(t, filepath.Join(f.pkg, "dist", "version"), "v"+fakeElectronVersion, 0o644)
	writeFile(t, filepath.Join(f.pkg, "dist", "Electron.app", "Contents", "MacOS", "Electron"), "", 0o755)
}

// source runs snippet in bash after sourcing the fixture's ensure-electron.sh.
func (f *electronFixture) source(t *testing.T, snippet string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", "-c", `. "$1"; `+snippet, "bash",
		filepath.Join(f.root, "scripts", "ensure-electron.sh"))
	cmd.Env = f.env
	out, err := cmd.CombinedOutput()
	return string(out), exitCode(t, err, out)
}

func (f *electronFixture) standalone(t *testing.T) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(f.root, "scripts", "ensure-electron.sh"))
	cmd.Env = f.env
	out, err := cmd.CombinedOutput()
	return string(out), exitCode(t, err, out)
}

func (f *electronFixture) calls(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func exitCode(t *testing.T, err error, out []byte) int {
	t.Helper()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	t.Fatalf("bash: %v\n%s", err, out)
	return -1
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureElectronBinPerOS(t *testing.T) {
	for osName, rel := range map[string]string{
		"Linux":  "dist/electron",
		"Darwin": "dist/Electron.app/Contents/MacOS/Electron",
	} {
		f := newElectronFixture(t, osName, false)
		out, code := f.source(t, "mads_electron_bin")
		if want := filepath.Join(f.pkg, rel); code != 0 || strings.TrimSpace(out) != want {
			t.Errorf("%s: mads_electron_bin = %q (exit %d), want %q", osName, out, code, want)
		}
	}
}

func TestEnsureElectronDarwinHealthy(t *testing.T) {
	f := newElectronFixture(t, "Darwin", false)
	f.installDarwinTree(t)

	if out, code := f.source(t, "mads_electron_ok"); code != 0 {
		t.Fatalf("mads_electron_ok on a healthy Electron.app tree: exit %d\n%s", code, out)
	}
	if out, code := f.source(t, "mads_electron_needs_no_sandbox"); code != 1 {
		t.Fatalf("mads_electron_needs_no_sandbox on darwin: exit %d, want 1 (seatbelt)\n%s", code, out)
	}
	out, code := f.standalone(t)
	if code != 0 || strings.Contains(out, "installer") {
		t.Fatalf("standalone run on a healthy darwin tree: exit %d, want 0 with no repair\n%s", code, out)
	}
	calls := f.calls(t)
	for _, never := range []string{"unshare", "install.js", "ditto"} {
		if strings.Contains(calls, never) {
			t.Errorf("healthy darwin run invoked %q:\n%s", never, calls)
		}
	}
}

func TestEnsureElectronDarwinExtractsWithDitto(t *testing.T) {
	f := newElectronFixture(t, "Darwin", true) // install.js fails: no dist/ at all

	out, code := f.standalone(t)
	if code != 0 {
		t.Fatalf("standalone run with a failing install.js: exit %d, want 0\n%s", code, out)
	}
	calls := f.calls(t)
	wantDitto := "ditto -x -k /cache/electron-v" + fakeElectronVersion + "-darwin-arm64.zip " + filepath.Join(f.pkg, "dist")
	if !strings.Contains(calls, wantDitto) {
		t.Errorf("extractor: want %q in calls:\n%s", wantDitto, calls)
	}
	if strings.Contains(calls, "unshare") {
		t.Errorf("darwin run probed userns:\n%s", calls)
	}
	pathTxt, err := os.ReadFile(filepath.Join(f.pkg, "path.txt"))
	if err != nil || string(pathTxt) != "Electron.app/Contents/MacOS/Electron" {
		t.Errorf("path.txt = %q (%v)", pathTxt, err)
	}
}

// brandFixture is a healthy darwin install plus what mads_brand_app reads: the
// committed icon and one helper app inside Electron.app.
func brandFixture(t *testing.T) (f *electronFixture, app, stamp, icns string) {
	t.Helper()
	f = newElectronFixture(t, "Darwin", false)
	f.installDarwinTree(t)
	helper := filepath.Join(f.pkg, "dist", "Electron.app", "Contents", "Frameworks",
		"Electron Helper (Renderer).app", "Contents", "Info.plist")
	writeFile(t, helper, "", 0o644)
	writeFile(t, filepath.Join(f.pkg, "dist", "Electron.app", "Contents", "Resources", "electron.icns"), "stock", 0o644)
	icns = filepath.Join(f.root, "ui", "assets", "icon", "mads.icns")
	writeFile(t, icns, "copper m", 0o644)
	return f, filepath.Join(f.pkg, "dist", "mads.app"), filepath.Join(f.pkg, "dist", "mads.app.stamp"), icns
}

func TestEnsureElectronBrandsDarwinBundle(t *testing.T) {
	f, app, stamp, icns := brandFixture(t)

	if out, code := f.source(t, "mads_brand_app"); code != 0 {
		t.Fatalf("mads_brand_app: exit %d\n%s", code, out)
	}
	calls := f.calls(t)
	plist := filepath.Join(app, "Contents", "Info.plist")
	for _, want := range []string{
		"ditto " + filepath.Join(f.pkg, "dist", "Electron.app") + " " + app,
		"plutil -replace CFBundleName -string mads " + plist,
		"plutil -replace CFBundleDisplayName -string mads " + plist,
		"plutil -replace CFBundleIdentifier -string dev.mads.app " + plist,
		"plutil -replace CFBundleIdentifier -string dev.mads.app.helper " +
			filepath.Join(app, "Contents", "Frameworks", "Electron Helper (Renderer).app", "Contents", "Info.plist"),
		"codesign --force --deep --sign - " + app,
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("want %q in calls:\n%s", want, calls)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(app, "Contents", "Resources", "electron.icns")); string(b) != "copper m" {
		t.Errorf("mads.app icon = %q, want the committed mads.icns", b)
	}
	if out, _ := f.source(t, "mads_app_bin"); strings.TrimSpace(out) != filepath.Join(app, "Contents", "MacOS", "Electron") {
		t.Errorf("mads_app_bin = %q", out)
	}
	if _, err := os.Stat(stamp); err != nil {
		t.Fatalf("no stamp after a successful build: %v", err)
	}

	// Up to date: no copy, no signing.
	before := f.calls(t)
	out, code := f.source(t, "mads_brand_app")
	if again := f.calls(t)[len(before):]; code != 0 || strings.Contains(again, "ditto") || strings.Contains(again, "codesign") {
		t.Fatalf("second mads_brand_app rebuilt (exit %d)\n%s\ncalls:\n%s", code, out, again)
	}
	before = f.calls(t)

	// A new icon (or Electron version) makes the stamp stale: rebuild.
	writeFile(t, icns, "a new m", 0o644)
	if out, code := f.source(t, "mads_brand_app"); code != 0 || !strings.Contains(f.calls(t)[len(before):], "codesign") {
		t.Fatalf("mads_brand_app after an icon change did not rebuild (exit %d)\n%s", code, out)
	}
}

func TestEnsureElectronBrandSigningFailureLeavesNoBundle(t *testing.T) {
	f, app, stamp, _ := brandFixture(t)
	f.env = append(f.env, "FAKE_CODESIGN_EXIT=1")

	if out, code := f.source(t, "mads_brand_app"); code == 0 {
		t.Fatalf("mads_brand_app with codesign failing: exit 0, want failure\n%s", out)
	}
	for _, p := range []string{app, stamp} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s left behind after a failed build (%v)", p, err)
		}
	}
}

func TestEnsureElectronBrandIsNoOpOnLinux(t *testing.T) {
	f := newElectronFixture(t, "Linux", false)
	out, code := f.source(t, "mads_brand_app && mads_app_bin")
	if want := filepath.Join(f.pkg, "dist", "electron"); code != 0 || strings.TrimSpace(out) != want {
		t.Errorf("linux: mads_brand_app && mads_app_bin = %q (exit %d), want %q", out, code, want)
	}
	if calls := f.calls(t); strings.Contains(calls, "ditto") || strings.Contains(calls, "codesign") {
		t.Errorf("linux branding touched a bundle:\n%s", calls)
	}
}
