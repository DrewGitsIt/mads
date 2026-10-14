package changes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		name  string
		f     FileFacts
		tier  string
		scrat string
	}{
		// scratch — only for files created during the session
		{"created outside folder", FileFacts{Rel: "notes.md", Created: true, Outside: true}, contracts.TierScratch, RuleOutside},
		{"created gitignored", FileFacts{Rel: "src/cache.json", Created: true, Ignored: true}, contracts.TierScratch, RuleGitignored},
		{"created scratch dir", FileFacts{Rel: "scratch/a.go", Created: true}, contracts.TierScratch, RuleScratchDir},
		{"created nested tmp dir", FileFacts{Rel: "pkg/tmp/a.go", Created: true}, contracts.TierScratch, RuleScratchDir},
		{"created temp dir", FileFacts{Rel: "temp/x.txt", Created: true}, contracts.TierScratch, RuleScratchDir},
		{"created tmp_ prefix", FileFacts{Rel: "pkg/tmp_x.go", Created: true}, contracts.TierScratch, RuleName},
		{"created repro- prefix", FileFacts{Rel: "repro-1.sh", Created: true}, contracts.TierScratch, RuleName},
		{"created debug. prefix", FileFacts{Rel: "debug.py", Created: true}, contracts.TierScratch, RuleName},
		{"created _debug suffix", FileFacts{Rel: "a/b/parser_debug.go", Created: true}, contracts.TierScratch, RuleName},
		{"created .scratch suffix", FileFacts{Rel: "notes.scratch.md", Created: true}, contracts.TierScratch, RuleName},
		{"created root test_ script", FileFacts{Rel: "test_parse.py", Created: true}, contracts.TierScratch, RuleName},
		{"created root .log", FileFacts{Rel: "run.log", Created: true}, contracts.TierScratch, RuleName},
		{"created root .prof", FileFacts{Rel: "cpu.prof", Created: true}, contracts.TierScratch, RuleName},
		// scratch rules don't fire for pre-existing files
		{"pre-existing tmp_ file", FileFacts{Rel: "tmp_x.go"}, contracts.TierSource, ""},
		{"pre-existing outside", FileFacts{Rel: "notes.md", Outside: true}, contracts.TierSource, ""},
		{"pre-existing ignored", FileFacts{Rel: "x.go", Ignored: true}, contracts.TierSource, ""},
		// name rules that are root-only
		{"nested test_ script is tests", FileFacts{Rel: "pkg/test_parse.py", Created: true}, contracts.TierTests, ""},
		{"nested .log is source", FileFacts{Rel: "pkg/run.log", Created: true}, contracts.TierSource, ""},
		{"pre-existing root test_ is tests", FileFacts{Rel: "test_parse.py"}, contracts.TierTests, ""},
		// generated
		{"linguist-generated", FileFacts{Rel: "api/client.ts", LinguistGenerated: true}, contracts.TierGenerated, ""},
		{"go header", FileFacts{Rel: "x.go", GeneratedHeader: true}, contracts.TierGenerated, ""},
		{"go.sum", FileFacts{Rel: "go.sum"}, contracts.TierGenerated, ""},
		{"nested package-lock", FileFacts{Rel: "ui/package-lock.json"}, contracts.TierGenerated, ""},
		{"uv.lock", FileFacts{Rel: "uv.lock"}, contracts.TierGenerated, ""},
		{"snapshot dir", FileFacts{Rel: "ui/__snapshots__/a.tsx"}, contracts.TierGenerated, ""},
		{"snap file", FileFacts{Rel: "a.test.ts.snap"}, contracts.TierGenerated, ""},
		{".gen.", FileFacts{Rel: "types.gen.ts"}, contracts.TierGenerated, ""},
		{"_gen.go", FileFacts{Rel: "x_gen.go"}, contracts.TierGenerated, ""},
		{".pb.go", FileFacts{Rel: "api/x.pb.go"}, contracts.TierGenerated, ""},
		{"_generated.", FileFacts{Rel: "schema_generated.rs"}, contracts.TierGenerated, ""},
		{"mocks dir", FileFacts{Rel: "internal/mocks/store.go"}, contracts.TierGenerated, ""},
		{"dist dir", FileFacts{Rel: "ui/dist/index.js"}, contracts.TierGenerated, ""},
		{"build dir", FileFacts{Rel: "build/out.txt"}, contracts.TierGenerated, ""},
		{"generated wins over tests", FileFacts{Rel: "mocks/x_test.go"}, contracts.TierGenerated, ""},
		// tests
		{"_test.go", FileFacts{Rel: "pkg/a_test.go"}, contracts.TierTests, ""},
		{".test.", FileFacts{Rel: "ui/a.test.tsx"}, contracts.TierTests, ""},
		{".spec.", FileFacts{Rel: "ui/a.spec.ts"}, contracts.TierTests, ""},
		{"_test.py", FileFacts{Rel: "lib/a_test.py"}, contracts.TierTests, ""},
		{"tests dir", FileFacts{Rel: "tests/helpers.py"}, contracts.TierTests, ""},
		{"__tests__ dir", FileFacts{Rel: "ui/__tests__/x.js"}, contracts.TierTests, ""},
		{"testdata dir", FileFacts{Rel: "pkg/testdata/in.json"}, contracts.TierTests, ""},
		{"fixtures dir", FileFacts{Rel: "fixtures/a.yaml"}, contracts.TierTests, ""},
		{"e2e dir", FileFacts{Rel: "ui/e2e/flow.ts"}, contracts.TierTests, ""},
		{"test dir", FileFacts{Rel: "test/x.c"}, contracts.TierTests, ""},
		// config
		{"go.mod", FileFacts{Rel: "go.mod"}, contracts.TierConfig, ""},
		{"package.json", FileFacts{Rel: "ui/package.json"}, contracts.TierConfig, ""},
		{"tsconfig", FileFacts{Rel: "ui/tsconfig.app.json"}, contracts.TierConfig, ""},
		{"Makefile", FileFacts{Rel: "Makefile"}, contracts.TierConfig, ""},
		{"Dockerfile.dev", FileFacts{Rel: "Dockerfile.dev"}, contracts.TierConfig, ""},
		{"docker-compose", FileFacts{Rel: "docker-compose.override.yml"}, contracts.TierConfig, ""},
		{"toml", FileFacts{Rel: "pyproject.toml"}, contracts.TierConfig, ""},
		{"ini", FileFacts{Rel: "setup.cfg"}, contracts.TierConfig, ""},
		{".env", FileFacts{Rel: ".env.local"}, contracts.TierConfig, ""},
		{".github any", FileFacts{Rel: ".github/CODEOWNERS"}, contracts.TierConfig, ""},
		{"gitlab ci", FileFacts{Rel: ".gitlab-ci.yml"}, contracts.TierConfig, ""},
		{"root yaml", FileFacts{Rel: ".golangci.yml"}, contracts.TierConfig, ""},
		{"deploy yaml", FileFacts{Rel: "deploy/app.yaml"}, contracts.TierConfig, ""},
		{"k8s yaml", FileFacts{Rel: "ops/k8s/svc.yml"}, contracts.TierConfig, ""},
		{"nested yaml is source", FileFacts{Rel: "pkg/data/x.yaml"}, contracts.TierSource, ""},
		// source
		{"plain go", FileFacts{Rel: "pkg/changes/build.go"}, contracts.TierSource, ""},
		{"readme", FileFacts{Rel: "README.md"}, contracts.TierSource, ""},
		{"testing.go isn't a test", FileFacts{Rel: "pkg/testing.go"}, contracts.TierSource, ""},
		{"contest dir isn't tests", FileFacts{Rel: "contest/x.go"}, contracts.TierSource, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tier, rule := Classify(tc.f)
			if tier != tc.tier || rule != tc.scrat {
				t.Errorf("Classify(%+v) = (%q, %q), want (%q, %q)", tc.f, tier, rule, tc.tier, tc.scrat)
			}
		})
	}
}

func TestHasGeneratedHeader(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, tc := range []struct {
		name, content string
		want          bool
	}{
		{"gen.go", "// Code generated by protoc-gen-go. DO NOT EDIT.\npackage x\n", true},
		{"crlf.go", "// Code generated by x. DO NOT EDIT.\r\npackage x\n", true},
		{"plain.go", "package x\n// Code generated by x. DO NOT EDIT.\n", false},
		{"nearly.go", "// Code generated by x. Do not edit.\npackage x\n", false},
		{"gen.txt", "// Code generated by x. DO NOT EDIT.\n", false},
	} {
		if got := HasGeneratedHeader(write(tc.name, tc.content)); got != tc.want {
			t.Errorf("%s: HasGeneratedHeader = %v, want %v", tc.name, got, tc.want)
		}
	}
	if HasGeneratedHeader(filepath.Join(dir, "missing.go")) {
		t.Error("missing file reported generated")
	}
}
