package vault

// The scripted stand-in binaries: when MADS_FAKE_OP=1 or MADS_FAKE_AWS=1,
// the test binary re-execs as a mock `op` or `aws` CLI (the standard os/exec
// testing trick, same pattern as providers/claudecode's fakeclaude_test.go).
// Every Source path — LookPath guard, version probe, resolve — gets real
// subprocess coverage in CI with neither CLI installed.

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	switch {
	case os.Getenv("MADS_FAKE_OP") == "1":
		fakeOpMain()
	case os.Getenv("MADS_FAKE_AWS") == "1":
		fakeAWSMain()
	default:
		os.Exit(m.Run())
	}
}

// fakeVaultData reads the ref->value map tests set per-run via t.Setenv, so
// tests never share state across parallel fake-binary invocations.
func fakeVaultData() map[string]string {
	data := map[string]string{}
	raw := os.Getenv("MADS_FAKE_VAULT_DATA")
	if raw == "" {
		return data
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		fmt.Fprintf(os.Stderr, "fake vault: parse MADS_FAKE_VAULT_DATA: %v\n", err)
		os.Exit(1)
	}
	return data
}

func fakeOpMain() {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("2.0.0-fake")
		os.Exit(0)
	}
	if len(args) == 2 && args[0] == "read" {
		value, ok := fakeVaultData()[args[1]]
		if !ok {
			fmt.Fprintf(os.Stderr, "[ERROR] 2023/01/01 00:00:00 could not read secret %q\n", args[1])
			os.Exit(1)
		}
		fmt.Println(value)
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "fake op: unrecognized args %v\n", args)
	os.Exit(1)
}

func fakeAWSMain() {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("aws-cli/2.0.0-fake")
		os.Exit(0)
	}
	if len(args) == 8 && args[0] == "secretsmanager" && args[1] == "get-secret-value" &&
		args[2] == "--secret-id" && args[4] == "--query" && args[5] == "SecretString" {
		value, ok := fakeVaultData()[args[3]]
		if !ok {
			fmt.Fprintf(os.Stderr, "An error occurred (ResourceNotFoundException) when calling the "+
				"GetSecretValue operation: Secrets Manager can't find the specified secret.\n")
			os.Exit(1)
		}
		fmt.Println(value)
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "fake aws: unrecognized args %v\n", args)
	os.Exit(1)
}

// testFakeSource builds a real Source (either OnePasswordSource or
// AWSSecretsManagerSource, chosen by fakeEnvFlag) wired to this test binary
// re-exec'd as the scripted stand-in, with data as its ref->value map.
func testOnePasswordSource(t *testing.T, data map[string]string) *OnePasswordSource {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return NewOnePassword(OnePasswordConfig{
		Bin: exe,
		Env: []string{"MADS_FAKE_OP=1", "MADS_FAKE_VAULT_DATA=" + string(raw)},
	})
}

func testAWSSecretsManagerSource(t *testing.T, data map[string]string) *AWSSecretsManagerSource {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return NewAWSSecretsManager(AWSSecretsManagerConfig{
		Bin: exe,
		Env: []string{"MADS_FAKE_AWS=1", "MADS_FAKE_VAULT_DATA=" + string(raw)},
	})
}
