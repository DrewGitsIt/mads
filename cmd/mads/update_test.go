package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/update"
)

func TestUpdateCheckPrintsResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"version":"1.0.0"}`))
		case "/update/check":
			_, _ = w.Write([]byte(`{"current_version":"1.0.0","latest_version":"2.0.0","update_available":true,"notes":"release notes here"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	if err := cmdUpdateCheck([]string{"-router", srv.URL}, &out); err != nil {
		t.Fatalf("cmdUpdateCheck: %v", err)
	}
	for _, want := range []string{"current version: 1.0.0", "update available: 2.0.0", "release notes here"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestUpdateCheckTransportErrorFails(t *testing.T) {
	var out bytes.Buffer
	err := cmdUpdateCheck([]string{"-router", "http://127.0.0.1:1"}, &out)
	if err == nil {
		t.Fatal("expected an error when the router is unreachable")
	}
}

func sha256HexOf(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func feedServer(t *testing.T, binaryURL, sha string) *httptest.Server {
	t.Helper()
	body := fmt.Sprintf(`{"version":"9.9.9","notes":"fixture notes","platforms":{%q:{"url":%q,"sha256":%q}}}`,
		update.CurrentPlatform(), binaryURL, sha)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
}

// TestUpdateApplyNoRouterRunning proves the "no Router running" path is a
// no-op, not a failure: a fake MADS_HOME with no router.pid present, real
// good-binary bytes served over HTTP and checksum-verified, swapped and
// health-checked via the CLI's own --version handling.
func TestUpdateApplyNoRouterRunning(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "mads")
	withFakeBinary(t, bin)
	t.Setenv("MADS_HOME", filepath.Join(dir, "home"))

	goodScript := "#!/bin/sh\nexit 0\n"
	binSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(goodScript))
	}))
	defer binSrv.Close()

	feedSrv := feedServer(t, binSrv.URL, sha256HexOf(goodScript))
	defer feedSrv.Close()

	var out bytes.Buffer
	err := cmdUpdateApply([]string{"-feed-url", feedSrv.URL, "-yes"}, strings.NewReader(""), &out)
	if err != nil {
		t.Fatalf("cmdUpdateApply: %v\n%s", err, out.String())
	}
	got, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != goodScript {
		t.Fatal("binary was not swapped to the new binary's bytes")
	}
	if !strings.Contains(out.String(), "no running router detected") {
		t.Errorf("output missing no-router guidance:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "updated to 9.9.9") {
		t.Errorf("output missing success line:\n%s", out.String())
	}
}

// TestUpdateApplyForcedFailureRollsBack is the CLI-level forced-failure
// smoke test: a broken "new" binary that exits non-zero on --version rolls
// back to the original, byte-identical binary and reports the rollback.
func TestUpdateApplyForcedFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "mads")
	withFakeBinary(t, bin)
	t.Setenv("MADS_HOME", filepath.Join(dir, "home"))

	original, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}

	brokenScript := "#!/bin/sh\nexit 1\n"
	binSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(brokenScript))
	}))
	defer binSrv.Close()

	feedSrv := feedServer(t, binSrv.URL, sha256HexOf(brokenScript))
	defer feedSrv.Close()

	var out bytes.Buffer
	err = cmdUpdateApply([]string{"-feed-url", feedSrv.URL, "-yes"}, strings.NewReader(""), &out)
	if err == nil {
		t.Fatal("expected an error from a forced health-check failure")
	}
	if !strings.Contains(out.String(), "rolled back") {
		t.Errorf("output missing rollback notice:\n%s", out.String())
	}
	got, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatal("binary is not byte-identical to the original after rollback")
	}
}

func TestUpdateApplyNoFeedConfiguredFails(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "mads")
	withFakeBinary(t, bin)
	t.Setenv("MADS_HOME", filepath.Join(dir, "home"))

	var out bytes.Buffer
	err := cmdUpdateApply([]string{"-router", "http://127.0.0.1:1", "-yes"}, strings.NewReader(""), &out)
	if err == nil {
		t.Fatal("expected an error resolving the feed URL with an unreachable router and no -feed-url")
	}
}
