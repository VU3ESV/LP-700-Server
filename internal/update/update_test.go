package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// The test hook must not leak in from the developer's shell: it would
// replace every version under test.
func TestMain(m *testing.M) {
	os.Unsetenv(TestVersionEnv)
	os.Exit(m.Run())
}

func TestParseVersion(t *testing.T) {
	cases := map[string]Version{
		"v1.10.2":                 {Core: []int{1, 10, 2}},
		" v0.2.1 ":                {Core: []int{0, 2, 1}},
		"V3":                      {Core: []int{3}},
		"v1.0.0-rc.1":             {Core: []int{1, 0, 0}, Pre: []string{"rc", "1"}},
		"v0.2.1-3-g92ace11":       {Core: []int{0, 2, 1}, Ahead: 3},
		"v0.2.1-3-g92ace11-dirty": {Core: []int{0, 2, 1}, Ahead: 3},
		"v0.2.1-dirty":            {Core: []int{0, 2, 1}},
		"v1.0.0-rc.1-3-g92ace11":  {Core: []int{1, 0, 0}, Pre: []string{"rc", "1"}, Ahead: 3},
		"dev":                     {},
		"test-1-gabc1234":         {},
		"1.2.x":                   {},
		"":                        {},
	}
	for in, want := range cases {
		if got := ParseVersion(in); !reflect.DeepEqual(got, want) {
			t.Errorf("ParseVersion(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		candidate, current string
		want               bool
	}{
		{"v0.2.2", "v0.2.1", true},
		{"v0.3.0", "0.2.9", true},
		{"v0.2.10", "v0.2.9", true}, // numeric, not lexical
		{"1.10", "1.2", true},
		{"v0.2.1", "v0.2.1", false},
		{"v0.2.1", "0.2.1", false},
		{"2.0", "2.0.0", false}, // missing parts count as 0
		{"v0.2.0", "v0.2.1", false},
		// A build stamped by `git describe` after a tag sits above that tag.
		{"v0.2.1", "v0.2.1-3-g92ace11", false},
		{"v0.2.2", "v0.2.1-3-g92ace11", true},
		{"v0.2.1-4-gabc1234", "v0.2.1-3-g92ace11", true},
		{"v0.2.1", "v0.2.1-dirty", false},
		// A pre-release sits below its release and above the one before.
		{"v1.0.0", "v1.0.0-rc.1", true},
		{"v1.0.0-rc.1", "v1.0.0", false},
		{"v1.0.0-rc.1", "v0.9.9", true},
		{"v1.0.0-rc.2", "v1.0.0-rc.1", true},
		{"v1.0.0-rc.10", "v1.0.0-rc.9", true}, // numeric identifiers by value
		{"v1.0.0-rc.1", "v1.0.0-beta", true},  // alphanumeric ones by ASCII
		{"v1.0.0-rc.1", "v1.0.0-rc", true},    // more identifiers win a tie
		{"v1.0.0", "v1.0.0-rc.1-3-g92ace11", true},
		// Nothing without a numeric core is older, or newer, than anything.
		{"v0.2.1", "test-1-gabc1234", false},
		{"v0.2.1", "dev", false},
		{"latest", "v0.2.1", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.candidate, c.current); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.candidate, c.current, got, c.want)
		}
	}
}

func TestIsDevVersion(t *testing.T) {
	for v, want := range map[string]bool{
		"dev": true, "0.3.0-DEV": true, "v0.2.1-dev.1": true,
		"v0.2.1": false, "v0.2.1-3-g92ace11": false, "": false,
	} {
		if got := IsDevVersion(v); got != want {
			t.Errorf("IsDevVersion(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestShouldCheck(t *testing.T) {
	never := time.Time{}
	cases := []struct {
		name                     string
		lastSuccess, lastFailure time.Time
		want                     bool
	}{
		{"never checked", never, never, true},
		{"23 h after a success", t0.Add(-23 * time.Hour), never, false},
		{"24 h after a success", t0.Add(-24 * time.Hour), never, true},
		{"success time in the future", t0.Add(time.Hour), never, true},
		{"30 min after a failure", never, t0.Add(-30 * time.Minute), false},
		{"1 h after a failure", never, t0.Add(-time.Hour), true},
		{"failure time in the future", never, t0.Add(time.Hour), true},
		{"due by success, held by failure", t0.Add(-25 * time.Hour), t0.Add(-10 * time.Minute), false},
	}
	for _, c := range cases {
		if got := ShouldCheck(c.lastSuccess, c.lastFailure, t0); got != c.want {
			t.Errorf("%s: ShouldCheck = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseRelease(t *testing.T) {
	ok := []byte(`{"tag_name":"v0.2.1","html_url":"https://github.com/VU3ESV/LP-700-Server/releases/tag/v0.2.1"}`)
	rel, err := ParseRelease(200, http.Header{}, ok)
	if err != nil || rel.TagName != "v0.2.1" {
		t.Fatalf("ParseRelease(200) = %+v, %v", rel, err)
	}

	limited := http.Header{}
	limited.Set("X-RateLimit-Remaining", "0")
	failures := []struct {
		status int
		header http.Header
		body   string
		want   error
	}{
		{200, http.Header{}, `{"name":"no tag"}`, ErrUnreadable},
		{200, http.Header{}, `{"tag_name":"  "}`, ErrUnreadable},
		{200, http.Header{}, `not json`, ErrUnreadable},
		{404, http.Header{}, ``, ErrNoRelease},
		{403, limited, ``, ErrRateLimited},
		{429, http.Header{}, ``, ErrRateLimited},
		{403, http.Header{}, ``, nil}, // a plain HTTP error
		{500, http.Header{}, ``, nil},
	}
	for _, f := range failures {
		_, err := ParseRelease(f.status, f.header, []byte(f.body))
		if err == nil {
			t.Errorf("ParseRelease(%d, %q) succeeded, want an error", f.status, f.body)
			continue
		}
		if f.want != nil && !errors.Is(err, f.want) {
			t.Errorf("ParseRelease(%d, %q) = %v, want %v", f.status, f.body, err, f.want)
		}
	}
}

func TestReleasePageStaysOnThisRepo(t *testing.T) {
	good := "https://github.com/VU3ESV/LP-700-Server/releases/tag/v0.2.2"
	if got := releasePage(good); got != good {
		t.Errorf("releasePage(%q) = %q", good, got)
	}
	for _, bad := range []string{"", "https://example.com/x", "javascript:alert(1)",
		"https://github.com/someone-else/LP-700-Server/releases/tag/v9",
		"https://github.com/VU3ESV/LP-700-Server/releases/../../evil/repo",
		"https://github.com/VU3ESV/LP-700-Server/releases/tag/../../../evil",
		"http://github.com/VU3ESV/LP-700-Server/releases/tag/v9",
		"https://user@github.com/VU3ESV/LP-700-Server/releases/tag/v9",
		"https://github.com@evil.example/VU3ESV/LP-700-Server/releases/tag/v9"} {
		if got := releasePage(bad); got != releasesURL {
			t.Errorf("releasePage(%q) = %q, want %q", bad, got, releasesURL)
		}
	}
}

// checker builds a Checker around a fake fetch and a fixed clock. Its
// log output (info and above) is returned for inspection.
func checker(t *testing.T, version string, fetch func(ctx context.Context) (Release, error)) (*Checker, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	c := New(Options{
		Enabled: true,
		Version: version,
		Logger:  slog.New(slog.NewTextHandler(&logs, nil)),
		fetch:   fetch,
		now:     func() time.Time { return t0 },
	})
	return c, &logs
}

func TestOnlySuccessRecordsTheCheckTime(t *testing.T) {
	c, _ := checker(t, "v0.2.1", nil)

	c.record(Release{}, errors.New("offline"), t0)
	if s := c.Status(); s.CheckedAt != nil {
		t.Fatalf("a failure recorded checked_at = %v", s.CheckedAt)
	}
	if !c.lastSuccess.IsZero() || !c.lastFailure.Equal(t0) {
		t.Fatalf("after a failure: lastSuccess=%v lastFailure=%v", c.lastSuccess, c.lastFailure)
	}
	if ShouldCheck(c.lastSuccess, c.lastFailure, t0.Add(30*time.Minute)) {
		t.Fatal("checked again inside the 1 h back-off")
	}

	later := t0.Add(time.Hour)
	c.record(Release{TagName: "v0.2.1"}, nil, later)
	s := c.Status()
	if s.CheckedAt == nil || !s.CheckedAt.Equal(later) {
		t.Fatalf("success did not record checked_at: %v", s.CheckedAt)
	}
	if !c.lastFailure.IsZero() {
		t.Fatal("success did not end the back-off")
	}
	if s.UpdateAvailable {
		t.Fatal("same version reported as an update")
	}
}

func TestNewerReleaseLoggedOncePerTag(t *testing.T) {
	c, notices := checker(t, "v0.2.1", nil)
	page := "https://github.com/VU3ESV/LP-700-Server/releases/tag/v0.2.2"

	c.record(Release{TagName: "v0.2.2", HTMLURL: page}, nil, t0)
	c.record(Release{TagName: "v0.2.2", HTMLURL: page}, nil, t0.Add(24*time.Hour))
	if n := strings.Count(notices.String(), "is available"); n != 1 {
		t.Fatalf("v0.2.2 logged %d times, want once:\n%s", n, notices)
	}
	if !strings.Contains(notices.String(), "LP-700-Server v0.2.2 is available") ||
		!strings.Contains(notices.String(), page) {
		t.Fatalf("notice line = %q", notices)
	}
	s := c.Status()
	if !s.UpdateAvailable || s.Latest != "v0.2.2" || s.HTMLURL != page {
		t.Fatalf("status = %+v", s)
	}

	c.record(Release{TagName: "v0.2.3", HTMLURL: "https://evil.example/"}, nil, t0.Add(48*time.Hour))
	if n := strings.Count(notices.String(), "is available"); n != 2 {
		t.Fatalf("a new tag was not logged:\n%s", notices)
	}
	if s := c.Status(); s.HTMLURL != releasesURL {
		t.Fatalf("foreign html_url passed through: %q", s.HTMLURL)
	}

	// A failure keeps the previous result.
	c.record(Release{}, errors.New("timeout"), t0.Add(72*time.Hour))
	if s := c.Status(); !s.UpdateAvailable || s.Latest != "v0.2.3" {
		t.Fatalf("failure dropped the known update: %+v", s)
	}
}

// The notice line must get past a logger filtered at error level when
// main hands over a level above it.
func TestNoticeLevelPassesTheRuntimeFilter(t *testing.T) {
	var out bytes.Buffer
	quiet := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelError}))
	c := New(Options{Enabled: true, Version: "v0.2.1", Logger: quiet, NoticeLevel: slog.LevelError + 4})
	c.record(Release{TagName: "v0.2.2"}, nil, t0)
	if !strings.Contains(out.String(), "is available") {
		t.Fatalf("notice hidden by an error-level logger:\n%s", out.String())
	}
}

func TestDevBuildNeverChecks(t *testing.T) {
	var calls int
	fetch := func(ctx context.Context) (Release, error) { calls++; return Release{TagName: "v9.9.9"}, nil }

	for _, v := range []string{"dev", "0.3.0-DEV", "", "test-1-gabc1234"} {
		c, _ := checker(t, v, fetch)
		done := make(chan struct{})
		go func() { c.Run(context.Background()); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("Run(%q) did not return for a development build", v)
		}
		if s := c.Status(); s.Automatic {
			t.Errorf("version %q: status says automatic", v)
		}
	}
	if calls != 0 {
		t.Fatalf("a development build made %d request(s)", calls)
	}
}

func TestTestHookOverridesVersionAndDevRule(t *testing.T) {
	t.Setenv(TestVersionEnv, "0.0.1")
	c, _ := checker(t, "dev", nil)
	if s := c.Status(); !s.Automatic || s.Current != "0.0.1" {
		t.Fatalf("test hook ignored: %+v", s)
	}
}

func TestSettingOffNeverChecks(t *testing.T) {
	var calls int
	c := New(Options{Enabled: false, Version: "v0.2.1",
		fetch: func(ctx context.Context) (Release, error) { calls++; return Release{}, nil }})
	c.Run(context.Background()) // returns at once
	if calls != 0 || c.Status().Automatic {
		t.Fatalf("setting off: calls=%d status=%+v", calls, c.Status())
	}
}

// Run, with tiny delays: the first tick checks, later ticks are held back
// by the 24 h gate after a success and by the 1 h back-off after a failure.
func TestRunChecksOnceThenWaits(t *testing.T) {
	for _, fail := range []bool{false, true} {
		var mu sync.Mutex
		calls := 0
		c := New(Options{
			Enabled: true, Version: "v0.2.1",
			FirstDelay: time.Millisecond, Interval: 5 * time.Millisecond,
			fetch: func(ctx context.Context) (Release, error) {
				mu.Lock()
				calls++
				mu.Unlock()
				if fail {
					return Release{}, errors.New("offline")
				}
				return Release{TagName: "v0.2.1"}, nil
			},
		})
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		c.Run(ctx)
		cancel()
		mu.Lock()
		if calls != 1 {
			t.Errorf("fail=%v: %d requests in 100 ms of 5 ms ticks, want 1", fail, calls)
		}
		mu.Unlock()
	}
}

// The real request path against a stand-in for GitHub: headers, parsing,
// and the status endpoint.
func TestFetchAndStatusEndpoint(t *testing.T) {
	var gotAccept, gotUA string
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept, gotUA = r.Header.Get("Accept"), r.Header.Get("User-Agent")
		if r.Header.Get("Authorization") != "" {
			t.Error("request carried an Authorization header")
		}
		_, _ = w.Write([]byte(`{"tag_name":"v0.2.2","html_url":"https://github.com/VU3ESV/LP-700-Server/releases/tag/v0.2.2","body":"notes"}`))
	}))
	defer gh.Close()

	c := New(Options{Enabled: true, Version: "v0.2.1", apiURL: gh.URL})
	c.checkIfDue(context.Background())
	if gotAccept != "application/vnd.github+json" || gotUA != "LP-700-Server/0.2.1" {
		t.Fatalf("headers: Accept=%q User-Agent=%q", gotAccept, gotUA)
	}

	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/update", nil))
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET /api/update: %d %v", rec.Code, rec.Header())
	}
	var s Status
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if !s.Enabled || !s.Automatic || s.Current != "v0.2.1" || !s.UpdateAvailable ||
		s.Latest != "v0.2.2" || s.CheckedAt == nil {
		t.Fatalf("status = %+v", s)
	}

	rec = httptest.NewRecorder()
	c.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("POST /api/update = %d %s, want 405 application/json", rec.Code, rec.Header().Get("Content-Type"))
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error != "method not allowed" {
		t.Fatalf("405 body = %q (%v)", rec.Body.String(), err)
	}
}
