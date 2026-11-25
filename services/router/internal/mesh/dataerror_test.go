package mesh

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drewkidwell/mads/pkg/dataclient"
)

// A missing row reads as what the route is about: a peer on /peers, a
// device on /devices and pairing (so remove everywhere's "not paired there
// any more" check sees device_not_found).
func TestDataErrorNotFoundCodeFollowsTheRoute(t *testing.T) {
	err := fmt.Errorf("GET /peers/x: %w", dataclient.ErrNotFound)
	for _, tc := range []struct {
		write func(http.ResponseWriter, error)
		want  string
	}{
		{writeDataError, "peer_not_found"},
		{writeDeviceDataError, "device_not_found"},
	} {
		w := httptest.NewRecorder()
		tc.write(w, err)
		var body struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != http.StatusNotFound || body.Code != tc.want {
			t.Fatalf("%d %q, want 404 %q", w.Code, body.Code, tc.want)
		}
	}
}
