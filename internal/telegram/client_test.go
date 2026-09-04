package telegram

import (
	"context"
	"testing"
	"time"
)

func TestOutboundJobPreservesParametersAndLocalFiles(t *testing.T) {
	job, err := newOutboundJob("sendPhoto", map[string]any{
		"chat_id": int64(123),
		"caption": "guide",
		"photo":   LocalFile{Path: "/tmp/guide.jpg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	params, err := job.decodedParams()
	if err != nil {
		t.Fatal(err)
	}
	file, ok := params["photo"].(LocalFile)
	if !ok || file.Path != "/tmp/guide.jpg" {
		t.Fatalf("local file was not preserved: %#v", params["photo"])
	}
	if params["caption"] != "guide" {
		t.Fatalf("caption = %#v", params["caption"])
	}
}

type recordingThrottler struct {
	acquired int
	penal    time.Duration
}

func (r *recordingThrottler) Acquire(context.Context) error { r.acquired++; return nil }
func (r *recordingThrottler) Penalize(d time.Duration) time.Duration {
	r.penal += d
	return d
}

func TestClientUsesThrottlerInterface(t *testing.T) {
	th := &recordingThrottler{}
	client, err := NewClient("test-token", time.Second, "", "main", th)
	if err != nil {
		t.Fatal(err)
	}
	if client.throttler != th {
		t.Fatal("throttler was not stored")
	}
}

func TestAPIResponseClassification(t *testing.T) {
	cases := []struct {
		name       string
		resp       APIResponse
		needsStart bool
		dropped    bool
	}{
		{"ok response", APIResponse{Ok: true}, false, false},
		{"never started", APIResponse{ErrorCode: 403, Description: "Forbidden: bot can't initiate conversation with a user"}, true, false},
		{"chat missing", APIResponse{ErrorCode: 400, Description: "Bad Request: chat not found"}, true, false},
		{"user blocked bot", APIResponse{ErrorCode: 403, Description: "Forbidden: bot was blocked by the user"}, false, true},
		{"deactivated", APIResponse{ErrorCode: 403, Description: "Forbidden: user is deactivated"}, false, true},
		{"other error", APIResponse{ErrorCode: 500, Description: "Internal server error"}, false, false},
	}
	for _, tc := range cases {
		if got := tc.resp.NeedsStart(); got != tc.needsStart {
			t.Errorf("%s: NeedsStart() = %v, want %v", tc.name, got, tc.needsStart)
		}
		if got := tc.resp.PermanentlyUndeliverable(); got != tc.dropped {
			t.Errorf("%s: PermanentlyUndeliverable() = %v, want %v", tc.name, got, tc.dropped)
		}
	}
}
