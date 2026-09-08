package sched

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"usaged/internal/providers"
	"usaged/internal/snapshot"
)

// fakeFetcher is a test double for providers.Fetcher.
type fakeFetcher struct {
	id        string
	fn        func(ctx context.Context, now time.Time) (snapshot.Provider, providers.Outcome)
	callCount atomic.Int32
}

func (f *fakeFetcher) ID() string { return f.id }

func (f *fakeFetcher) Fetch(ctx context.Context, now time.Time) (snapshot.Provider, providers.Outcome) {
	f.callCount.Add(1)
	if f.fn != nil {
		return f.fn(ctx, now)
	}
	return snapshot.Provider{ID: f.id, Status: "ok", FetchedAt: now.Unix()}, providers.Outcome{}
}

func okProvider(id, label, plan string, pct int, r int64, now time.Time) snapshot.Provider {
	return snapshot.Provider{
		ID:        id,
		Label:     label,
		Plan:      plan,
		Status:    "ok",
		Msg:       "",
		FetchedAt: now.Unix(),
		Rows: []snapshot.Row{{
			K:       "5h",
			Label:   "CLAUDE 5h",
			Pct:     &pct,
			Txt:     "05:09",
			Tier:    "ok",
			ResetAt: &r,
		}},
	}
}

func testClock() func() time.Time {
	t := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	var i atomic.Int32
	return func() time.Time {
		return t.Add(time.Duration(i.Add(1)) * time.Second)
	}
}

// --- Tests ---

func TestSchedulerFirstPoll(t *testing.T) {
	clock := testClock()
	f := &fakeFetcher{
		id: "claude",
		fn: func(ctx context.Context, now time.Time) (snapshot.Provider, providers.Outcome) {
			pct := 19
			r := int64(1788411000)
			return okProvider("claude", "Claude", "max_20x", pct, r, now), providers.Outcome{}
		},
	}

	s := NewScheduler([]providers.Fetcher{f}, 900*time.Second, "", clock, nil)
	s.PollOnce(context.Background())

	snap := s.Current()
	if snap.Seq != 1 {
		t.Errorf("Seq = %d, want 1", snap.Seq)
	}
	if snap.Rev == "" {
		t.Error("Rev should be set")
	}
	if len(snap.Providers) != 1 {
		t.Errorf("len(Providers) = %d, want 1", len(snap.Providers))
	}
}

func TestSchedulerIdenticalPoll(t *testing.T) {
	clock := testClock()
	pct := 19
	r := int64(1788411000)
	f := &fakeFetcher{
		id: "claude",
		fn: func(ctx context.Context, now time.Time) (snapshot.Provider, providers.Outcome) {
			return okProvider("claude", "Claude", "max_20x", pct, r, now), providers.Outcome{}
		},
	}

	s := NewScheduler([]providers.Fetcher{f}, 900*time.Second, "", clock, nil)

	s.PollOnce(context.Background())
	first := s.Current()

	s.PollOnce(context.Background())
	second := s.Current()

	if second.Seq != first.Seq {
		t.Errorf("Seq = %d, want %d (identical content should not increment seq)", second.Seq, first.Seq)
	}
	if second.CheckedAt == first.CheckedAt {
		t.Error("CheckedAt should have moved")
	}
}

func TestSchedulerStaleOnFetchError(t *testing.T) {
	clock := testClock()
	pct := 19
	r := int64(1788411000)
	var callNum atomic.Int32
	f := &fakeFetcher{
		id: "claude",
		fn: func(ctx context.Context, now time.Time) (snapshot.Provider, providers.Outcome) {
			if callNum.Add(1) == 1 {
				return okProvider("claude", "Claude", "max_20x", pct, r, now), providers.Outcome{}
			}
			// Second call: error
			return snapshot.Provider{
				ID:        "claude",
				Label:     "Claude",
				Plan:      "max_20x",
				Status:    "error",
				Msg:       "api unreachable",
				FetchedAt: now.Unix(),
			}, providers.Outcome{}
		},
	}

	s := NewScheduler([]providers.Fetcher{f}, 900*time.Second, "", clock, nil)

	// First poll: ok
	s.PollOnce(context.Background())
	first := s.Current()
	if first.Seq != 1 {
		t.Fatalf("after first poll Seq = %d, want 1", first.Seq)
	}

	// Second poll: fetcher returns error, LastGood should be served as stale
	s.PollOnce(context.Background())
	second := s.Current()

	if second.Seq != first.Seq+1 {
		t.Errorf("Seq = %d, want %d (status change should increment seq)", second.Seq, first.Seq+1)
	}
	if len(second.Providers) != 1 {
		t.Fatalf("len(Providers) = %d, want 1", len(second.Providers))
	}
	p := second.Providers[0]
	if p.Status != "stale" {
		t.Errorf("Status = %q, want stale", p.Status)
	}
	if p.Msg != "api unreachable" {
		t.Errorf("Msg = %q, want api unreachable", p.Msg)
	}
	// Rows should be kept from last-good
	if len(p.Rows) != 1 {
		t.Fatalf("len(Rows) = %d, want 1", len(p.Rows))
	}
	if p.Rows[0].K != "5h" {
		t.Errorf("Row K = %q, want 5h", p.Rows[0].K)
	}
	if *p.Rows[0].Pct != 19 {
		t.Errorf("Row Pct = %d, want 19", *p.Rows[0].Pct)
	}
}

func TestSchedulerCooldownRespected(t *testing.T) {
	clock := testClock()
	f := &fakeFetcher{
		id: "claude",
		fn: func(ctx context.Context, now time.Time) (snapshot.Provider, providers.Outcome) {
			return snapshot.Provider{
					ID:        "claude",
					Label:     "Claude",
					Plan:      "max_20x",
					Status:    "error",
					Msg:       "api unreachable",
					FetchedAt: now.Unix(),
				}, providers.Outcome{
					CooldownUntil: now.Add(300 * time.Second),
				}
		},
	}

	s := NewScheduler([]providers.Fetcher{f}, 900*time.Second, "", clock, nil)

	// First poll: sets cooldown
	s.PollOnce(context.Background())
	if f.callCount.Load() != 1 {
		t.Errorf("callCount = %d, want 1 after first poll", f.callCount.Load())
	}

	// Second poll: cooldown active, fetcher should NOT be called
	s.PollOnce(context.Background())
	if f.callCount.Load() != 1 {
		t.Errorf("callCount = %d, want 1 (cooldown should prevent fetch)", f.callCount.Load())
	}

	snap := s.Current()
	if len(snap.Providers) != 1 {
		t.Fatalf("len(Providers) = %d, want 1", len(snap.Providers))
	}
	if snap.Providers[0].Status != "stale" {
		t.Errorf("Status = %q, want stale", snap.Providers[0].Status)
	}
}

func TestSchedulerRefreshCoalescing(t *testing.T) {
	clock := testClock()
	release := make(chan struct{})
	started := make(chan struct{})
	pct := 19
	r := int64(1788411000)

	f := &fakeFetcher{
		id: "claude",
		fn: func(ctx context.Context, now time.Time) (snapshot.Provider, providers.Outcome) {
			close(started)
			<-release // block until test releases
			return snapshot.Provider{
				ID:        "claude",
				Status:    "ok",
				Msg:       "",
				Plan:      "max_20x",
				Label:     "Claude",
				FetchedAt: now.Unix(),
				Rows:      []snapshot.Row{{K: "5h", Label: "CLAUDE 5h", Pct: &pct, Txt: "05:09", Tier: "ok", ResetAt: &r}},
			}, providers.Outcome{}
		},
	}

	s := NewScheduler([]providers.Fetcher{f}, 900*time.Second, "", clock, nil)

	pollDone := make(chan struct{})
	go func() {
		s.PollOnce(context.Background())
		close(pollDone)
	}()

	<-started // PollOnce has acquired pollMu and the fetcher is running

	// Refresh should be coalesced (poll is in progress)
	s.Refresh()

	if f.callCount.Load() != 1 {
		t.Errorf("callCount = %d, want 1 (Refresh should be coalesced)", f.callCount.Load())
	}

	close(release)
	<-pollDone // PollOnce finished

	// callCount should still be 1
	if f.callCount.Load() != 1 {
		t.Errorf("callCount = %d, want 1 after poll completes", f.callCount.Load())
	}
}

func TestSchedulerStateFile(t *testing.T) {
	clock := testClock()
	pct := 19
	r := int64(1788411000)
	f := &fakeFetcher{
		id: "claude",
		fn: func(ctx context.Context, now time.Time) (snapshot.Provider, providers.Outcome) {
			return okProvider("claude", "Claude", "max_20x", pct, r, now), providers.Outcome{}
		},
	}

	statePath := t.TempDir() + "/state.json"
	s := NewScheduler([]providers.Fetcher{f}, 900*time.Second, statePath, clock, nil)

	s.PollOnce(context.Background())

	loaded, err := snapshot.Load(statePath)
	if err != nil {
		t.Fatalf("Load state: %v", err)
	}
	if loaded.Snapshot.Seq != 1 {
		t.Errorf("loaded Seq = %d, want 1", loaded.Snapshot.Seq)
	}
	if len(loaded.Snapshot.Providers) != 1 {
		t.Errorf("loaded len(Providers) = %d, want 1", len(loaded.Snapshot.Providers))
	}
}

// TestSchedulerSetFetchers verifies that replacing the fetcher list drops
// removed providers from the snapshot and changes the rev (AUDIT #20).
func TestSchedulerSetFetchers(t *testing.T) {
	clock := testClock()
	pct := 19
	r := int64(1788411000)

	claude := &fakeFetcher{
		id: "claude",
		fn: func(ctx context.Context, now time.Time) (snapshot.Provider, providers.Outcome) {
			return okProvider("claude", "Claude", "max_20x", pct, r, now), providers.Outcome{}
		},
	}
	codex := &fakeFetcher{
		id: "codex",
		fn: func(ctx context.Context, now time.Time) (snapshot.Provider, providers.Outcome) {
			return snapshot.Provider{
				ID:        "codex",
				Label:     "ChatGPT",
				Plan:      "plus",
				Status:    "ok",
				FetchedAt: now.Unix(),
				Rows:      []snapshot.Row{{K: "5h", Label: "GPT 5h", Pct: &pct, Txt: "23:13", Tier: "ok", ResetAt: &r}},
			}, providers.Outcome{}
		},
	}

	s := NewScheduler([]providers.Fetcher{claude, codex}, 900*time.Second, "", clock, nil)
	s.PollOnce(context.Background())
	first := s.Current()
	if len(first.Providers) != 2 {
		t.Fatalf("first poll: len(Providers) = %d, want 2", len(first.Providers))
	}

	// Remove codex, keep claude.
	s.SetFetchers([]providers.Fetcher{claude})
	s.PollOnce(context.Background())
	second := s.Current()

	if len(second.Providers) != 1 {
		t.Fatalf("after SetFetchers: len(Providers) = %d, want 1", len(second.Providers))
	}
	if second.Providers[0].ID != "claude" {
		t.Errorf("providers[0].id = %q, want claude", second.Providers[0].ID)
	}
	if second.Rev == first.Rev {
		t.Errorf("rev unchanged after fetcher removal: %s", second.Rev)
	}
	if second.Seq != first.Seq+1 {
		t.Errorf("Seq = %d, want %d", second.Seq, first.Seq+1)
	}
}

func TestSchedulerNoGoroutineLeak(t *testing.T) {
	clock := func() time.Time { return time.Now() }
	f := &fakeFetcher{
		id: "claude",
		fn: func(ctx context.Context, now time.Time) (snapshot.Provider, providers.Outcome) {
			return snapshot.Provider{ID: "claude", Status: "ok", FetchedAt: now.Unix()}, providers.Outcome{}
		},
	}

	s := NewScheduler([]providers.Fetcher{f}, 10*time.Millisecond, "", clock, nil)

	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	wg.Wait()

	after := runtime.NumGoroutine()

	if after > before+2 {
		t.Errorf("goroutine leak: before=%d after=%d (diff=%d)", before, after, after-before)
	}
}

// --- Publish tests (task 59) ---

// publishTestServer returns an httptest server that records PUT requests
// and checks the Bearer token. Returns the server, a request-count counter,
// and the last received body.
func publishTestServer(token string, cnt *atomic.Int32, body *atomic.Value) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		cnt.Add(1)
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		body.Store(b)
		w.WriteHeader(http.StatusOK)
	}))
}

func TestPublishOnRevChange(t *testing.T) {
	clock := testClock()
	pct := 19
	r := int64(1788411000)

	f := &fakeFetcher{
		id: "claude",
		fn: func(ctx context.Context, now time.Time) (snapshot.Provider, providers.Outcome) {
			return okProvider("claude", "Claude", "max_20x", pct, r, now), providers.Outcome{}
		},
	}

	var cnt atomic.Int32
	var body atomic.Value
	srv := publishTestServer("secret-token", &cnt, &body)
	defer srv.Close()

	s := NewScheduler([]providers.Fetcher{f}, 900*time.Second, "", clock, nil)
	s.PublishURL = srv.URL
	s.PublishToken = "secret-token"
	s.PublishClient = srv.Client()

	// First poll: rev goes from "" to something → publish fires.
	s.PollOnce(context.Background())
	if cnt.Load() != 1 {
		t.Fatalf("publish count = %d, want 1 after first poll", cnt.Load())
	}
	b := body.Load()
	if b == nil {
		t.Fatal("expected published body, got nil")
	}
	var published snapView
	if err := json.Unmarshal(b.([]byte), &published); err != nil {
		t.Fatalf("published body is not valid JSON snapshot: %v", err)
	}
	if published.Seq != 1 {
		t.Errorf("published seq = %d, want 1", published.Seq)
	}
}

func TestPublishOnlyOnRevChange(t *testing.T) {
	clock := testClock()
	pct := 19
	r := int64(1788411000)

	f := &fakeFetcher{
		id: "claude",
		fn: func(ctx context.Context, now time.Time) (snapshot.Provider, providers.Outcome) {
			return okProvider("claude", "Claude", "max_20x", pct, r, now), providers.Outcome{}
		},
	}

	var cnt atomic.Int32
	var body atomic.Value
	srv := publishTestServer("t", &cnt, &body)
	defer srv.Close()

	s := NewScheduler([]providers.Fetcher{f}, 900*time.Second, "", clock, nil)
	s.PublishURL = srv.URL
	s.PublishToken = "t"
	s.PublishClient = srv.Client()

	// First poll: rev changes → publish.
	s.PollOnce(context.Background())
	if cnt.Load() != 1 {
		t.Fatalf("after first poll: publish count = %d, want 1", cnt.Load())
	}

	// Second poll: identical content, rev unchanged → NO publish.
	s.PollOnce(context.Background())
	if cnt.Load() != 1 {
		t.Errorf("after second identical poll: publish count = %d, want 1 (no publish on unchanged rev)", cnt.Load())
	}
}

// snapView is a minimal view of Snapshot for test assertions.
type snapView struct {
	V         int    `json:"v"`
	Seq       int64  `json:"seq"`
	Rev       string `json:"rev"`
	Providers []struct {
		ID string `json:"id"`
	} `json:"providers"`
}
