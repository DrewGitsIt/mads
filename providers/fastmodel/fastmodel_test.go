package fastmodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/golden"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
)

// classification is a typical structured-job output (task synthesis shape).
type classification struct {
	Kind       string  `json:"kind"`
	Title      string  `json:"title"`
	Confidence float64 `json:"confidence"`
}

func (c *classification) Validate() error {
	var probs []string
	if c.Kind == "" {
		probs = append(probs, "kind is required")
	}
	if c.Confidence < 0 || c.Confidence > 1 {
		probs = append(probs, "confidence must be within [0,1]")
	}
	if len(probs) > 0 {
		return errors.New(strings.Join(probs, "; "))
	}
	return nil
}

func runnerWith(t *testing.T, rules []fake.Rule) *Runner {
	t.Helper()
	f, err := fake.NewWithScenario(fake.Scenario{Name: "fastmodel", Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	return New(f, Config{Model: "fake-fast"})
}

// The golden structured-output path: prose-and-fence-wrapped JSON from the
// fake comes back as a validated struct. The golden pins exactly what a
// synthesis-style caller receives.
func TestStructuredOutputGolden(t *testing.T) {
	r := runnerWith(t, []fake.Rule{{
		Match: "classify",
		Reply: "Sure! Here's the classification you asked for:\n```json\n" +
			`{"kind":"task_candidate","title":"Fix the flaky login test","confidence":0.85}` +
			"\n```\nLet me know if you need anything else.",
	}})
	var out classification
	usage, err := r.Do(context.Background(), Job{
		System: "You classify lab-notebook observations.",
		Prompt: "classify: the login test failed twice, then passed",
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if usage.OutputTokens == 0 {
		t.Error("usage not reported")
	}
	pinned, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "classification.golden.json", append(pinned, '\n'))
}

// A malformed first reply is retried with the failure fed back, and usage
// accumulates across attempts.
func TestRetryWithFeedback(t *testing.T) {
	r := runnerWith(t, []fake.Rule{
		// The feedback prompt embeds the original, so this rule must come
		// first: it matches only retries.
		{Match: "could not be used", Reply: `{"kind":"note","title":"t","confidence":0.5}`},
		{Match: "classify", Reply: "I think it's probably a note? Hard to say."},
	})
	var out classification
	usage, err := r.Do(context.Background(), Job{Prompt: "classify: hmm"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != "note" {
		t.Errorf("parsed: %+v", out)
	}
	single, _ := runnerWith(t, []fake.Rule{
		{Match: "classify", Reply: `{"kind":"note","title":"t","confidence":0.5}`},
	}).Do(context.Background(), Job{Prompt: "classify: hmm"}, &classification{})
	if usage.InputTokens <= single.InputTokens {
		t.Errorf("retry usage %+v should exceed single-attempt usage %+v", usage, single)
	}
}

// Validation failures retry the same way parse failures do.
func TestValidationFailureRetries(t *testing.T) {
	r := runnerWith(t, []fake.Rule{
		{Match: "failed validation", Reply: `{"kind":"decision","title":"t","confidence":1}`},
		{Match: "classify", Reply: `{"kind":"","title":"t","confidence":3}`},
	})
	var out classification
	if _, err := r.Do(context.Background(), Job{Prompt: "classify: x"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Kind != "decision" {
		t.Errorf("parsed: %+v", out)
	}
}

func TestAttemptsExhausted(t *testing.T) {
	r := runnerWith(t, []fake.Rule{{Match: "", Reply: "no json in sight"}})
	var out classification
	_, err := r.Do(context.Background(), Job{Prompt: "classify: x"}, &out)
	if err == nil || !strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("want exhaustion error, got %v", err)
	}
}

// Rate limits bubble immediately — inform and wait (§11.9), never a silent
// retry loop.
func TestRateLimitNotRetried(t *testing.T) {
	r := New(fake.New(), Config{})
	var out classification
	_, err := r.Do(context.Background(), Job{Prompt: "ratelimit"}, &out)
	var rle *provider.RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("want RateLimitError, got %v", err)
	}
	if strings.Contains(err.Error(), "attempts") {
		t.Errorf("rate limit was fed through the retry loop: %v", err)
	}
}

// Configuring a BaseURL flips the transport to the OpenAI-compatible
// endpoint — the caller's Do is unchanged (the §11 optimization contract).
func TestEndpointFlip(t *testing.T) {
	t.Setenv("MADS_TEST_FM_KEY", "sk-test-123")
	var gotAuth, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		_, _ = fmt.Fprint(w, `{"model":"endpoint-fast","choices":[{"message":{"content":"{\"kind\":\"note\",\"title\":\"t\",\"confidence\":0.9}"}}],"usage":{"prompt_tokens":12,"completion_tokens":9}}`)
	}))
	defer srv.Close()

	r := New(nil, Config{Model: "endpoint-fast", BaseURL: srv.URL, APIKeyEnv: "MADS_TEST_FM_KEY"})
	var out classification
	usage, err := r.Do(context.Background(), Job{System: "sys", Prompt: "classify: y"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != "note" || usage.InputTokens != 12 || usage.OutputTokens != 9 || usage.Model != "endpoint-fast" {
		t.Errorf("out %+v usage %+v", out, usage)
	}
	if gotAuth != "Bearer sk-test-123" {
		t.Errorf("credential from named env var: got %q", gotAuth)
	}
	if gotModel != "endpoint-fast" {
		t.Errorf("model: %q", gotModel)
	}
}

func TestEndpointRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	r := New(nil, Config{BaseURL: srv.URL})
	_, _, err := r.DoText(context.Background(), Job{Prompt: "x"})
	var rle *provider.RateLimitError
	if !errors.As(err, &rle) || rle.RetryAfter != 30*time.Second {
		t.Fatalf("want RateLimitError with RetryAfter=30s, got %v", err)
	}
}

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name, in, want string
		wantErr        bool
	}{
		{"bare object", `{"a":1}`, `{"a":1}`, false},
		{"fenced", "```json\n{\"a\":1}\n```", `{"a":1}`, false},
		{"prose around", `Here you go: {"a":{"b":[1,2]}} — enjoy!`, `{"a":{"b":[1,2]}}`, false},
		{"braces inside strings", `{"s":"a } tricky ] one"}`, `{"s":"a } tricky ] one"}`, false},
		{"escaped quote in string", `{"s":"he said \" } "}`, `{"s":"he said \" } "}`, false},
		{"array", `[1,2,3] trailing`, `[1,2,3]`, false},
		{"no json", "sorry, I can't help", "", true},
		{"unbalanced", `{"a": {"b": 1`, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := extractJSON(c.in)
			if c.wantErr != (err != nil) || got != c.want {
				t.Fatalf("got %q, %v; want %q, wantErr=%v", got, err, c.want, c.wantErr)
			}
		})
	}
}
