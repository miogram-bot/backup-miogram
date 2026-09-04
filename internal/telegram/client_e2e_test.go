package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// scriptRoundTripper replays scripted API responses in order and records the
// request path of every call, simulating Telegram without any real network.
type scriptRoundTripper struct {
	mu        sync.Mutex
	responses []string // raw JSON bodies, last one repeats forever
	paths     []string
}

func (s *scriptRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paths = append(s.paths, r.URL.Path)
	body := s.responses[len(s.responses)-1]
	if len(s.paths) < len(s.responses) {
		body = s.responses[len(s.paths)-1]
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

func (s *scriptRoundTripper) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.paths)
}

func newE2EClient(t *testing.T, rt *scriptRoundTripper, th Throttler) (*Client, *queueHarness) {
	t.Helper()
	client, err := NewClient("test-token", 5*time.Second, "", "test", th)
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = "https://api.telegram.local/bottest-token/"
	client.httpClient = &http.Client{Transport: rt}

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return client, &queueHarness{rdb: rdb, mr: mr}
}

type queueHarness struct {
	rdb *redis.Client
	mr  *miniredis.Miniredis
}

// TestOutboundQueueDeliversThroughRealPipeline runs the full loop:
// Call() -> Redis outbound:test queue -> runOutboundQueue consumer ->
// mock HTTP -> Redis response key -> Call() returns.
func TestOutboundQueueDeliversThroughRealPipeline(t *testing.T) {
	rt := &scriptRoundTripper{responses: []string{`{"ok":true,"result":{"message_id":7}}`}}
	client, h := newE2EClient(t, rt, &recordingThrottler{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.StartOutboundQueue(ctx, h.rdb)

	resp, err := client.Call(ctx, "sendMessage", map[string]any{"chat_id": "1", "text": "hi"})
	if err != nil {
		t.Fatalf("call via pipeline: %v", err)
	}
	if !resp.Ok {
		t.Fatalf("response not ok: %+v", resp)
	}
	var sent struct {
		MessageID int `json:"message_id"`
	}
	if err := json.Unmarshal(resp.Result, &sent); err != nil || sent.MessageID != 7 {
		t.Fatalf("result payload = %s (err %v)", resp.Result, err)
	}
}

// TestOutboundQueueRetriesAfterFloodWait scripts a 429 with retry_after=0s
// window followed by success and verifies:
//   - Penalize(retry_after) is invoked on the throttler (AIMD multiplicative decrease),
//   - the request is retried and eventually succeeds,
//   - no message is lost between attempts.
func TestOutboundQueueRetriesAfterFloodWait(t *testing.T) {
	rt := &scriptRoundTripper{responses: []string{
		`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`,
		`{"ok":true,"result":{"message_id":9}}`,
	}}
	th := &recordingThrottler{}
	client, h := newE2EClient(t, rt, th)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.StartOutboundQueue(ctx, h.rdb)

	done := make(chan struct{})
	var resp APIResponse
	var callErr error
	go func() {
		resp, callErr = client.Call(ctx, "sendMessage", map[string]any{"chat_id": "2"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("call did not complete; flood retry stalled")
	}
	if callErr != nil {
		t.Fatalf("call after flood retry failed: %v", callErr)
	}
	if !resp.Ok || rt.calls() != 2 {
		t.Fatalf("resp ok=%v after %d HTTP calls, want success on 2nd attempt", resp.Ok, rt.calls())
	}
	if th.penal != time.Second {
		t.Fatalf("throttler penalized with %v, want exact retry_after 1s", th.penal)
	}
	if th.acquired < 2 {
		t.Fatalf("limiter gate ran %d times, want >= 2 (once per attempt)", th.acquired)
	}
}

// TestCallDirectParsesFloodResponse pins response parsing without any queue:
// error_code, description and parameters.retry_after must survive decoding so
// classification and throttling decisions see accurate data.
func TestCallDirectParsesFloodResponse(t *testing.T) {
	rt := &scriptRoundTripper{responses: []string{
		`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 3","parameters":{"retry_after":3}}`,
	}}
	client, _ := newE2EClient(t, rt, nil)

	resp, err := client.callDirect(context.Background(), "sendMessage", map[string]any{"chat_id": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Ok || resp.ErrorCode != 429 || resp.Parameters.RetryAfter != 3 {
		t.Fatalf("parsed response = %+v", resp)
	}
	if strings.TrimSpace(resp.Description) == "" {
		t.Fatal("description lost in parsing")
	}
}

// TestNetworkErrorSurfacesAsResult verifies transport failures do not panic or
// hang the caller; they surface as an error from callDirect.
func TestNetworkErrorSurfacesAsResult(t *testing.T) {
	client, err := NewClient("t", time.Second, "", "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient = &http.Client{Transport: failingRT{}}
	if _, err := client.callDirect(context.Background(), "getMe", nil); err == nil {
		t.Fatal("transport failure must return an error")
	}
}

type failingRT struct{}

func (failingRT) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}
