package runner

import (
	"context"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// TestAutoReviewStartsAtEffectiveTrust: the fake declares no reviewer
// (BehaviorTrustAutoReview unsupported), so an auto_review session's
// provider starts at limited — provider.EffectiveTrust, applied by the
// Runner itself rather than trusted to every adapter's trust switch —
// while the record keeps the level the user asked for.
func TestAutoReviewStartsAtEffectiveTrust(t *testing.T) {
	cases := []struct {
		trust contracts.SessionTrust
		want  provider.Trust
	}{
		{contracts.TrustAutoReview, provider.TrustLimited},
		{contracts.TrustLimited, provider.TrustLimited},
		{contracts.TrustFull, provider.TrustFull},
		{contracts.TrustReadOnly, provider.TrustReadOnly},
	}
	for _, c := range cases {
		t.Run(string(c.trust), func(t *testing.T) {
			ctx := context.Background()
			base := datatest.Start(t)
			bootstrap := dataclient.New(base, "test-bootstrap")
			sess := contracts.Session{Name: "test", Provider: "fake", Trust: c.trust}
			if err := bootstrap.CreateSession(ctx, &sess); err != nil {
				t.Fatalf("create session: %v", err)
			}
			data := dataclient.New(base, sess.ID)
			reg := provider.NewRegistry()
			fp := fake.New()
			if err := reg.Register(fp); err != nil {
				t.Fatalf("register fake: %v", err)
			}
			r := New(sess.ID, data, reg)
			if err := r.Start(ctx); err != nil {
				t.Fatalf("start: %v", err)
			}
			t.Cleanup(func() { _ = r.Shutdown(context.Background()) })
			calls := fp.StartCalls()
			if len(calls) != 1 || calls[0].Trust != c.want {
				t.Fatalf("start calls = %+v, want one at trust %q", calls, c.want)
			}
			got, err := data.Session(ctx, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Trust != c.trust {
				t.Errorf("recorded trust = %q, want the requested %q kept", got.Trust, c.trust)
			}
		})
	}
}
