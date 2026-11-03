package control

import (
	"encoding/json"
	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"testing"
)

func TestPolicyZeroValuesSurviveWebResponse(t *testing.T) {
	b, err := protobuf(&pb.Config{KeepAwake: &pb.KeepAwake{Enabled: true, IntervalMinS: 30, IntervalMaxS: 60}})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Policy map[string]any `json:"keep_awake"`
	}
	if err = json.Unmarshal(b, &response); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stop_after_s", "host_active_within_s"} {
		if value, ok := response.Policy[name]; !ok || value != float64(0) {
			t.Fatalf("unlimited setting omitted from UI response: %s: %s", name, b)
		}
	}
}
