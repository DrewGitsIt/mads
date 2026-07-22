package agentenv

import (
	"reflect"
	"testing"
)

func TestBuild_InheritIsByteIdenticalToLegacyAppend(t *testing.T) {
	parent := []string{"PATH=/bin", "SECRET_TOKEN=leak-me", "HOME=/home/x"}
	credentials := []string{"MADS_CRED=abc"}
	extra := []string{"CAP_TOKEN=def"}

	got := Build(ModeInherit, parent, credentials, nil, extra)

	// Legacy behavior: append(os.Environ(), cfg.Env...) then append(env, extra...).
	want := append([]string{}, parent...)
	want = append(want, credentials...)
	want = append(want, extra...)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inherit mode diverged from legacy append semantics:\ngot:  %v\nwant: %v", got, want)
	}
}

func TestBuild_ZeroValueModeBehavesAsInherit(t *testing.T) {
	parent := []string{"PATH=/bin", "SECRET_TOKEN=leak-me"}

	got := Build("", parent, nil, nil, nil)

	if !reflect.DeepEqual(got, parent) {
		t.Fatalf("zero-value mode did not behave as inherit: got %v", got)
	}
}

func TestBuild_IsolatedExcludesPlantedSecret(t *testing.T) {
	parent := []string{"PATH=/bin", "HOME=/home/x", "AWS_SECRET_ACCESS_KEY=super-secret"}

	got := Build(ModeIsolated, parent, nil, nil, nil)

	for _, kv := range got {
		if kv == "AWS_SECRET_ACCESS_KEY=super-secret" {
			t.Fatalf("isolated mode leaked a planted secret global: %v", got)
		}
	}
	if !contains(got, "PATH=/bin") || !contains(got, "HOME=/home/x") {
		t.Fatalf("isolated mode dropped a base allowlist var: %v", got)
	}
}

func TestBuild_IsolatedIncludesOnlyAllowlistCredentialsAndPassthrough(t *testing.T) {
	parent := []string{
		"PATH=/bin",
		"HOME=/home/x",
		"USER=x",
		"LOGNAME=x",
		"SHELL=/bin/zsh",
		"TERM=xterm",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"TZ=UTC",
		"TMPDIR=/tmp",
		"XDG_RUNTIME_DIR=/run/user/1000",
		"AWS_PROFILE=work",
		"SECRET_TOKEN=leak-me",
	}
	credentials := []string{"MADS_CRED=abc"}
	extra := []string{"CAP_TOKEN=def"}

	got := Build(ModeIsolated, parent, credentials, []string{"AWS_PROFILE"}, extra)

	want := []string{
		"PATH=/bin", "HOME=/home/x", "USER=x", "LOGNAME=x", "SHELL=/bin/zsh",
		"TERM=xterm", "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8", "TZ=UTC",
		"TMPDIR=/tmp", "XDG_RUNTIME_DIR=/run/user/1000", "AWS_PROFILE=work",
		"MADS_CRED=abc", "CAP_TOKEN=def",
	}

	if !sameSet(got, want) {
		t.Fatalf("isolated env mismatch:\ngot:  %v\nwant: %v", got, want)
	}
}

func TestBuild_IsolatedPassesThroughLCPrefixedVars(t *testing.T) {
	parent := []string{"PATH=/bin", "LC_TIME=en_GB.UTF-8", "LC_MONETARY=en_GB.UTF-8"}

	got := Build(ModeIsolated, parent, nil, nil, nil)

	if !contains(got, "LC_TIME=en_GB.UTF-8") || !contains(got, "LC_MONETARY=en_GB.UTF-8") {
		t.Fatalf("isolated mode dropped LC_* vars: %v", got)
	}
}

func TestBuild_IsolatedWithoutPassthroughOmitsUnlistedVar(t *testing.T) {
	parent := []string{"PATH=/bin", "AWS_PROFILE=work"}

	got := Build(ModeIsolated, parent, nil, nil, nil)

	if contains(got, "AWS_PROFILE=work") {
		t.Fatalf("isolated mode included a var not on the allowlist or passthrough: %v", got)
	}
}

func TestSelect_ExtractsOnlyNamedVarsInOrder(t *testing.T) {
	env := []string{"PATH=/bin", "MADS_CRED_A=aaa", "SECRET_TOKEN=leak-me", "MADS_CRED_B=bbb"}

	got := Select(env, []string{"MADS_CRED_B", "MADS_CRED_A"})

	want := []string{"MADS_CRED_B=bbb", "MADS_CRED_A=aaa"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Select mismatch:\ngot:  %v\nwant: %v", got, want)
	}
}

func TestSelect_SkipsNamesNotPresent(t *testing.T) {
	env := []string{"PATH=/bin"}

	got := Select(env, []string{"MISSING_CRED"})

	if len(got) != 0 {
		t.Fatalf("expected no result for missing name, got %v", got)
	}
}

func TestSelect_EmptyNamesReturnsNil(t *testing.T) {
	got := Select([]string{"PATH=/bin"}, nil)

	if got != nil {
		t.Fatalf("expected nil for empty names, got %v", got)
	}
}

func contains(env []string, kv string) bool {
	for _, v := range env {
		if v == kv {
			return true
		}
	}
	return false
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, v := range a {
		seen[v]++
	}
	for _, v := range b {
		seen[v]--
	}
	for _, count := range seen {
		if count != 0 {
			return false
		}
	}
	return true
}
