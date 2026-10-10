// Package update tells the operator when a newer LP-700-Server release is
// published on GitHub.
//
// What it does:
//
//   - Exactly one request per check, and no other network traffic:
//     GET https://api.github.com/repos/VU3ESV/LP-700-Server/releases/latest
//     with Accept: application/vnd.github+json and
//     User-Agent: LP-700-Server/<version>, a 10 s timeout and no token.
//     That endpoint never returns drafts or prereleases.
//   - Versions compare numerically: a leading "v" is dropped, both sides
//     are split into integers on any non-digit and compared as tuples,
//     missing parts counting as 0 (v0.2.10 > v0.2.9, 1.0 == 1.0.0).
//   - About a minute after start, then re-evaluated every hour for as long
//     as the server runs. A check goes out only when [updates] check is on
//     (the default), 24 h have passed since the last SUCCESSFUL check, and
//     at least 1 h has passed since a failed one. Success means HTTP 200
//     with JSON carrying a tag_name, newer or not; only a success records
//     the check time. Nothing is written to disk (the systemd unit runs
//     with ProtectSystem=strict and no state directory), so a restart
//     checks again a minute after start.
//   - Failures (offline, timeout, any HTTP error including the 403 rate
//     limit, bad JSON) are silent: one debug log line.
//   - A newer release is logged once per release tag on the notice logger
//     (always printed, whatever the runtime log level) and exposed to the
//     web UI through GET /api/update, a read-only snapshot.
//   - Development builds — a version containing "dev", any case, which is
//     what a plain `go build` gets — never check.
//   - Nothing is downloaded or installed. Updating stays the operator's
//     job (deploy/redeploy.sh or the release binaries).
//
// It runs on its own goroutine with its own http.Client: never on a request
// path, never near the HID owner or the hub.
//
// TEST HOOK (inert unless set): LP700_SERVER_UPDATE_TEST_VERSION replaces
// the running version (and is not subject to the "dev" rule), so the notice
// can be seen against the real latest release.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Repository is the GitHub "owner/name" whose releases are checked.
	Repository = "VU3ESV/LP-700-Server"
	// ProductName is shown in the notice and is the User-Agent product.
	ProductName = "LP-700-Server"

	apiURL      = "https://api.github.com/repos/" + Repository + "/releases/latest"
	releasesURL = "https://github.com/" + Repository + "/releases"

	// TestVersionEnv replaces the running version when set (test hook).
	TestVersionEnv = "LP700_SERVER_UPDATE_TEST_VERSION"

	RequestTimeout  = 10 * time.Second
	FirstCheckDelay = 60 * time.Second // "shortly after start"
	RecheckInterval = time.Hour        // how often the rule is re-evaluated
	MinimumInterval = 24 * time.Hour   // counted from the last successful check
	FailureBackoff  = time.Hour        // no check for this long after a failure

	maxBody = 1 << 20
)

// Errors a check can end with besides network failures.
var (
	ErrNoRelease   = errors.New("GitHub has no published release yet")
	ErrRateLimited = errors.New("GitHub's anonymous rate limit for this network is used up")
	ErrUnreadable  = errors.New("GitHub's answer could not be read")
)

// Release is the part of GitHub's release JSON the checker reads.
type Release struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

// ---------------------------------------------------------------------------
// Pure logic — no I/O, no state.

// DisplayVersion trims surrounding space and one leading "v"/"V".
func DisplayVersion(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "v") || strings.HasPrefix(v, "V") {
		return v[1:]
	}
	return v
}

// VersionComponents: "v1.10.2-rc3" → [1 10 2 3], integers split on any
// non-digit.
func VersionComponents(v string) []int {
	var out []int
	for _, part := range strings.FieldsFunc(DisplayVersion(v), func(r rune) bool {
		return r < '0' || r > '9'
	}) {
		n, err := strconv.Atoi(part)
		if err != nil {
			n = math.MaxInt // absurdly long digit run
		}
		out = append(out, n)
	}
	return out
}

// IsNewer reports whether candidate is strictly greater than current,
// compared as integer tuples with missing components counting as 0.
func IsNewer(candidate, current string) bool {
	a, b := VersionComponents(candidate), VersionComponents(current)
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// IsDevVersion reports whether v marks a development build: it contains
// "dev" in any case ("dev", "0.3.0-DEV").
func IsDevVersion(v string) bool {
	return strings.Contains(strings.ToLower(v), "dev")
}

// ShouldCheck is the rule for every automatic attempt: the setting is on,
// 24 h have passed since the last successful check (or there has been
// none, or its time lies in the future because the clock was wrong), and
// the 1 h back-off after a failed check is over (a failure time in the
// future does not hold it).
func ShouldCheck(enabled bool, lastSuccess, lastFailure, now time.Time) bool {
	if !enabled {
		return false
	}
	if !lastSuccess.IsZero() {
		if elapsed := now.Sub(lastSuccess); elapsed >= 0 && elapsed < MinimumInterval {
			return false
		}
	}
	if !lastFailure.IsZero() {
		if elapsed := now.Sub(lastFailure); elapsed >= 0 && elapsed < FailureBackoff {
			return false
		}
	}
	return true
}

// ParseRelease turns GitHub's answer into a Release. Only HTTP 200 whose
// JSON carries a non-empty tag_name is a success.
func ParseRelease(status int, header http.Header, body []byte) (Release, error) {
	switch {
	case status == http.StatusOK:
	case status == http.StatusNotFound:
		return Release{}, ErrNoRelease
	case status == http.StatusTooManyRequests,
		status == http.StatusForbidden && header.Get("X-RateLimit-Remaining") == "0":
		return Release{}, ErrRateLimited
	default:
		return Release{}, fmt.Errorf("GitHub answered with HTTP status %d", status)
	}
	var rel Release
	if err := json.Unmarshal(body, &rel); err != nil {
		return Release{}, ErrUnreadable
	}
	rel.TagName = strings.TrimSpace(rel.TagName)
	if rel.TagName == "" {
		return Release{}, ErrUnreadable
	}
	return rel, nil
}

// releasePage only ever links the operator to this repository's release
// pages: GitHub's html_url when it is one of them, else the releases list.
func releasePage(htmlURL string) string {
	if strings.HasPrefix(htmlURL, releasesURL+"/") {
		return htmlURL
	}
	return releasesURL
}

// ---------------------------------------------------------------------------
// The checker.

// Options configures a Checker. Zero delays mean the package defaults.
type Options struct {
	// Enabled is the [updates] check setting.
	Enabled bool
	// Version is the running version (main.version, stamped by -ldflags).
	Version string
	// Logger gets the routine lines (start, debug on failure).
	Logger *slog.Logger
	// Notice gets the one line per newer release; it should print whatever
	// the runtime log level is. Defaults to Logger.
	Notice *slog.Logger

	FirstDelay time.Duration
	Interval   time.Duration

	// For tests: the request target, and a replacement for the request.
	apiURL string
	fetch  func(ctx context.Context) (Release, error)
	now    func() time.Time
}

// Checker runs the background check and keeps its last result.
type Checker struct {
	enabled    bool
	current    string // what is compared (the test hook's value when set)
	automatic  bool   // false for a development build
	testHook   bool
	logger     *slog.Logger
	notice     *slog.Logger
	firstDelay time.Duration
	interval   time.Duration
	fetch      func(ctx context.Context) (Release, error)
	now        func() time.Time

	mu          sync.Mutex
	lastSuccess time.Time
	lastFailure time.Time
	latest      *Release // set only while a newer release is known
	loggedTag   string
}

// New builds a Checker; call Run on its own goroutine.
func New(opts Options) *Checker {
	c := &Checker{
		enabled:    opts.Enabled,
		current:    strings.TrimSpace(opts.Version),
		logger:     opts.Logger,
		notice:     opts.Notice,
		firstDelay: opts.FirstDelay,
		interval:   opts.Interval,
		fetch:      opts.fetch,
		now:        opts.now,
	}
	if c.logger == nil {
		c.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if c.notice == nil {
		c.notice = c.logger
	}
	if c.firstDelay <= 0 {
		c.firstDelay = FirstCheckDelay
	}
	if c.interval <= 0 {
		c.interval = RecheckInterval
	}
	if c.now == nil {
		c.now = time.Now
	}
	if test := strings.TrimSpace(os.Getenv(TestVersionEnv)); test != "" {
		c.current, c.testHook = test, true
	}
	c.automatic = c.testHook || (c.current != "" && !IsDevVersion(c.current))
	if c.fetch == nil {
		target := opts.apiURL
		if target == "" {
			target = apiURL
		}
		client := &http.Client{Timeout: RequestTimeout}
		version := c.current
		c.fetch = func(ctx context.Context) (Release, error) {
			return fetchLatest(ctx, client, target, version)
		}
	}
	return c
}

// Run makes the first check about a minute after start and then
// re-evaluates ShouldCheck every hour until ctx is done. It returns at once
// when the setting is off or this is a development build.
func (c *Checker) Run(ctx context.Context) {
	if !c.enabled {
		c.logger.Info("update check off ([updates] check = false)")
		return
	}
	if !c.automatic {
		c.logger.Info("update check skipped: development build",
			"version", c.current, "hint", TestVersionEnv+"=<version> still checks")
		return
	}
	c.logger.Info("update check on", "repo", Repository, "version", c.current,
		"test_hook", c.testHook, "first_in", c.firstDelay,
		"min_interval", MinimumInterval, "failure_backoff", FailureBackoff)

	timer := time.NewTimer(c.firstDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		c.checkIfDue(ctx)
		timer.Reset(c.interval)
	}
}

// checkIfDue makes one attempt when ShouldCheck allows it.
func (c *Checker) checkIfDue(ctx context.Context) {
	c.mu.Lock()
	due := ShouldCheck(c.enabled, c.lastSuccess, c.lastFailure, c.now())
	c.mu.Unlock()
	if !due {
		return
	}
	reqCtx, cancel := context.WithTimeout(ctx, RequestTimeout)
	rel, err := c.fetch(reqCtx)
	cancel()
	if ctx.Err() != nil {
		return // shutting down: neither a success nor a failure
	}
	c.record(rel, err, c.now())
}

// record stores what a finished check means. Only a success records the
// check time (and ends any back-off); a failure records only the back-off
// start and keeps the previous result.
func (c *Checker) record(rel Release, err error, now time.Time) {
	if err != nil {
		c.mu.Lock()
		c.lastFailure = now
		c.mu.Unlock()
		c.logger.Debug("update check failed (ignored, retried in an hour at the earliest)", "err", err)
		return
	}
	newer := IsNewer(rel.TagName, c.current)
	logIt := false
	c.mu.Lock()
	c.lastSuccess = now
	c.lastFailure = time.Time{}
	if newer {
		c.latest = &Release{TagName: rel.TagName, HTMLURL: releasePage(rel.HTMLURL)}
		if rel.TagName != c.loggedTag {
			c.loggedTag, logIt = rel.TagName, true
		}
	} else {
		c.latest = nil
	}
	c.mu.Unlock()

	if logIt {
		c.notice.Info(fmt.Sprintf("%s %s is available", ProductName, rel.TagName),
			"running", c.current, "release_notes", releasePage(rel.HTMLURL))
	} else if !newer {
		c.logger.Debug("update check: up to date", "running", c.current, "latest", rel.TagName)
	}
}

// Status is the read-only snapshot served at GET /api/update.
type Status struct {
	// Enabled is the [updates] check setting.
	Enabled bool `json:"enabled"`
	// Automatic is false when checks never run (setting off, or a
	// development build).
	Automatic       bool       `json:"automatic"`
	Current         string     `json:"current"`
	UpdateAvailable bool       `json:"update_available"`
	Latest          string     `json:"latest,omitempty"`
	HTMLURL         string     `json:"html_url,omitempty"`
	CheckedAt       *time.Time `json:"checked_at"` // last SUCCESSFUL check
}

// Status returns the cached result. Cheap; never does I/O.
func (c *Checker) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Status{
		Enabled:   c.enabled,
		Automatic: c.enabled && c.automatic,
		Current:   c.current,
	}
	if c.latest != nil {
		s.UpdateAvailable = true
		s.Latest = c.latest.TagName
		s.HTMLURL = c.latest.HTMLURL
	}
	if !c.lastSuccess.IsZero() {
		t := c.lastSuccess.UTC()
		s.CheckedAt = &t
	}
	return s
}

// ServeHTTP serves GET /api/update.
func (c *Checker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	_ = json.NewEncoder(w).Encode(c.Status())
}

// fetchLatest makes the one request.
func fetchLatest(ctx context.Context, client *http.Client, target, version string) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", ProductName+"/"+DisplayVersion(version))
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Release{}, err
	}
	return ParseRelease(resp.StatusCode, resp.Header, body)
}
