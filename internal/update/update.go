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
//   - Versions compare as vX.Y.Z tuples (leading "v" optional, missing
//     parts count as 0: v0.2.10 > v0.2.9, 1.0 == 1.0.0). A pre-release
//     suffix sorts below its release (v1.0.0-rc.1 < v1.0.0) and a
//     `git describe` build sorts above the tag it follows
//     (v0.2.1 < v0.2.1-3-g92ace11 < v0.2.2). See ParseVersion.
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
//   - A newer release is logged once per release tag at Options.NoticeLevel
//     (main passes a level above anything the runtime filter can hide) and
//     exposed to the web UI through GET /api/update, a read-only snapshot.
//   - Development builds — a version containing "dev", any case, which is
//     what a plain `go build` gets — and versions without a numeric core
//     (nothing to compare against) never check.
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
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"lp700-server/internal/web"
)

const (
	// Repository is the GitHub "owner/name" whose releases are checked.
	Repository = "VU3ESV/LP-700-Server"
	// ProductName is shown in the notice and is the User-Agent product.
	ProductName = "LP-700-Server"

	apiURL       = "https://api.github.com/repos/" + Repository + "/releases/latest"
	releasesPath = "/" + Repository + "/releases"
	releasesURL  = "https://github.com" + releasesPath

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

// Version is a parsed version string: the dotted numeric core, the
// pre-release identifiers (nil for a release) and, for a `git describe`
// build, how many commits it is past its tag.
type Version struct {
	Core  []int
	Pre   []string
	Ahead int
}

// describeTail is the "-<commits>-g<hash>" that `git describe` appends
// after a tag (a trailing "-dirty" is stripped before matching).
var describeTail = regexp.MustCompile(`-(\d+)-g[0-9a-fA-F]{4,}$`)

// ParseVersion reads every shape this project stamps into a binary:
//
//	v1.2.3                     a release
//	v1.2.3-rc.1, v1.2.3-beta   a pre-release, below the bare v1.2.3
//	v1.2.3-5-gabc1234[-dirty]  git describe, 5 commits past the tag: above
//	                           the bare v1.2.3, below v1.2.4
//	v1.2.3-rc.1-5-gabc1234     5 commits past a pre-release tag
//
// Core is nil when the string does not start with a dotted number
// ("dev", "test", ""): such a version cannot be compared at all.
func ParseVersion(v string) Version {
	s := strings.TrimSuffix(DisplayVersion(v), "-dirty")
	var out Version
	if m := describeTail.FindStringSubmatch(s); m != nil {
		out.Ahead, _ = digits(m[1])
		s = strings.TrimSuffix(s, m[0])
	}
	core, pre, hasPre := strings.Cut(s, "-")
	for _, part := range strings.Split(core, ".") {
		n, ok := digits(part)
		if !ok {
			return Version{}
		}
		out.Core = append(out.Core, n)
	}
	if hasPre {
		out.Pre = strings.Split(pre, ".")
	}
	return out
}

// Compare orders two versions: -1 when a is older than b, 0 when equal,
// +1 when newer. Cores compare as integer tuples with missing parts
// counting as 0. At an equal core a pre-release sits below the release,
// and pre-release identifiers follow semver precedence (numeric before
// alphanumeric, numeric by value, alphanumeric by ASCII, more identifiers
// win a tie). Last, a describe build sits above the tag it follows,
// ordered by commit count.
func Compare(a, b Version) int {
	for i := 0; i < len(a.Core) || i < len(b.Core); i++ {
		if c := cmp.Compare(at(a.Core, i), at(b.Core, i)); c != 0 {
			return c
		}
	}
	if (a.Pre == nil) != (b.Pre == nil) {
		if a.Pre == nil {
			return 1
		}
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		if c := comparePreID(a.Pre[i], b.Pre[i]); c != 0 {
			return c
		}
	}
	if c := cmp.Compare(len(a.Pre), len(b.Pre)); c != 0 {
		return c
	}
	return cmp.Compare(a.Ahead, b.Ahead)
}

// at is xs[i], or 0 past the end.
func at(xs []int, i int) int {
	if i < len(xs) {
		return xs[i]
	}
	return 0
}

func comparePreID(x, y string) int {
	nx, xNum := digits(x)
	ny, yNum := digits(y)
	switch {
	case xNum && yNum:
		return cmp.Compare(nx, ny)
	case xNum:
		return -1
	case yNum:
		return 1
	}
	return strings.Compare(x, y)
}

// digits parses an all-digit string; an absurdly long run saturates.
func digits(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return math.MaxInt, true
	}
	return n, true
}

// IsNewer reports whether candidate is strictly newer than current. Either
// side without a numeric core cannot be compared, so the answer is false:
// no notice is ever shown for a version the checker does not understand.
func IsNewer(candidate, current string) bool {
	a, b := ParseVersion(candidate), ParseVersion(current)
	if a.Core == nil || b.Core == nil {
		return false
	}
	return Compare(a, b) > 0
}

// IsDevVersion reports whether v marks a development build: it contains
// "dev" in any case ("dev", "0.3.0-DEV").
func IsDevVersion(v string) bool {
	return strings.Contains(strings.ToLower(v), "dev")
}

// ShouldCheck is the rule for every automatic attempt (Run has already
// settled that the setting is on): 24 h have passed since the last
// successful check (or there has been none, or its time lies in the
// future because the clock was wrong), and the 1 h back-off after a
// failed check is over (a failure time in the future does not hold it).
func ShouldCheck(lastSuccess, lastFailure, now time.Time) bool {
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
// pages: GitHub's html_url when, after path normalisation, it is one of
// them; else the releases list.
func releasePage(htmlURL string) string {
	u, err := url.Parse(htmlURL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil ||
		!strings.HasPrefix(path.Clean(u.Path), releasesPath+"/") {
		return releasesURL
	}
	return htmlURL
}

// ---------------------------------------------------------------------------
// The checker.

// Options configures a Checker. Zero delays mean the package defaults.
type Options struct {
	// Enabled is the [updates] check setting.
	Enabled bool
	// Version is the running version (main.version, stamped by -ldflags).
	Version string
	// Logger gets the routine lines (start, debug on failure) and the one
	// line per newer release, which is logged at NoticeLevel.
	Logger *slog.Logger
	// NoticeLevel is the level of the "newer release" line. main passes a
	// level above anything /api/log-level can select, so the line is
	// printed whatever the runtime level is. Zero means slog.LevelInfo.
	NoticeLevel slog.Level

	FirstDelay time.Duration
	Interval   time.Duration

	// For tests: the request target, and a replacement for the request.
	apiURL string
	fetch  func(ctx context.Context) (Release, error)
	now    func() time.Time
}

// Checker runs the background check and keeps its last result.
type Checker struct {
	enabled     bool
	current     string // what is compared (the test hook's value when set)
	automatic   bool   // false for a development or unversioned build
	testHook    bool
	logger      *slog.Logger
	noticeLevel slog.Level
	firstDelay  time.Duration
	interval    time.Duration
	fetch       func(ctx context.Context) (Release, error)
	now         func() time.Time

	mu          sync.Mutex
	lastSuccess time.Time
	lastFailure time.Time
	latest      *Release // set only while a newer release is known
	loggedTag   string
}

// New builds a Checker; call Run on its own goroutine.
func New(opts Options) *Checker {
	c := &Checker{
		enabled:     opts.Enabled,
		current:     strings.TrimSpace(opts.Version),
		logger:      opts.Logger,
		noticeLevel: opts.NoticeLevel,
		firstDelay:  opts.FirstDelay,
		interval:    opts.Interval,
		fetch:       opts.fetch,
		now:         opts.now,
	}
	if c.logger == nil {
		c.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
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
	c.automatic = c.testHook || (!IsDevVersion(c.current) && ParseVersion(c.current).Core != nil)
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
// when the setting is off or this is a development or unversioned build.
func (c *Checker) Run(ctx context.Context) {
	if !c.enabled {
		c.logger.Info("update check off ([updates] check = false)")
		return
	}
	if !c.automatic {
		c.logger.Info("update check skipped: development or unversioned build",
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
	due := ShouldCheck(c.lastSuccess, c.lastFailure, c.now())
	c.mu.Unlock()
	if !due {
		return
	}
	// The http.Client's Timeout bounds the request; ctx only cuts it short
	// on shutdown.
	rel, err := c.fetch(ctx)
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
		c.logger.Log(context.Background(), c.noticeLevel,
			fmt.Sprintf("%s %s is available", ProductName, rel.TagName),
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
		web.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
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
