package pipelinerun

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/inhibit"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/services/data/datatest"
)

func TestSleepInhibitDisabled(t *testing.T) {
	for _, tc := range []struct {
		allow, wantDisabled bool
	}{
		{allow: false, wantDisabled: false}, // default: prevent sleep
		{allow: true, wantDisabled: true},
	} {
		cfg := contracts.DefaultConfig()
		cfg.AllowSleepDuringPipelines = tc.allow
		if got := SleepInhibitDisabled(cfg); got != tc.wantDisabled {
			t.Errorf("AllowSleepDuringPipelines=%v: SleepInhibitDisabled = %v, want %v",
				tc.allow, got, tc.wantDisabled)
		}
	}
}

// A stored config that predates allow_sleep_during_pipelines must resolve
// to "prevent sleep" with no migration — the whole point of the field's
// negative name.
func TestSleepInhibitDisabledPreFieldConfig(t *testing.T) {
	const preFieldJSON = `{"synthesis_mode":"light","synthesis_auto_approve":false,` +
		`"concurrency_limit":3,"pipeline_stage_timeout_minutes":30}`
	var cfg contracts.Config
	if err := json.Unmarshal([]byte(preFieldJSON), &cfg); err != nil {
		t.Fatal(err)
	}
	if SleepInhibitDisabled(cfg) {
		t.Fatal("pre-field stored config resolved to allow sleep, want prevent sleep")
	}
}

type countingLocker struct {
	mu       sync.Mutex
	acquired int
}

func (c *countingLocker) Acquire(string) inhibit.Handle {
	c.mu.Lock()
	c.acquired++
	c.mu.Unlock()
	return inhibit.HandleFunc(func() {})
}

func (c *countingLocker) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.acquired
}

// The resolved setting reaches the engine through Options: the inhibitor is
// acquired for a prevent-sleep config and never for an allow-sleep one.
func TestRunHonorsSleepSetting(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "pipeline")
	tpl := loadDefaultTemplate(t, data)

	for _, allow := range []bool{false, true} {
		sess := contracts.Session{Name: "sleep setting", Workspace: "test"}
		if err := data.CreateSession(context.Background(), &sess); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		cfg := contracts.DefaultConfig()
		cfg.AllowSleepDuringPipelines = allow
		locker := &countingLocker{}
		if _, err := Run(context.Background(), Options{
			Data: data, Provider: fake.New(), SessionID: sess.ID,
			Template: tpl, Cwd: newTestWorkspace(t),
			Concurrency: 2, StageTimeout: 30 * time.Second,
			DisableSleepInhibit: SleepInhibitDisabled(cfg),
			SleepInhibitor:      locker,
		}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		want := 1
		if allow {
			want = 0
		}
		if got := locker.count(); got != want {
			t.Errorf("allow_sleep_during_pipelines=%v: Acquire called %d times, want %d", allow, got, want)
		}
	}
}
