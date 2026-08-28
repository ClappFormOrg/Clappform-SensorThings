package sulo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ClappFormOrg/Clappform-SensorThings/internal/adapters"
	"github.com/ClappFormOrg/Clappform-SensorThings/internal/canonical"
)

// ---------------------------------------------------------------------------
// fake REEN server
// ---------------------------------------------------------------------------

type routeFn func(q url.Values) (status int, body string)

// fakeREEN is a stand-in for the REEN CMS REST API: it enforces the
// X-Token session scheme so the client's login / re-login behaviour is
// exercised for real, and records every request for assertions.
type fakeREEN struct {
	ts     *httptest.Server
	routes map[string]routeFn

	mu           sync.Mutex
	logins       int
	seen         []string
	customerSeen []string
	token        string
	badCreds     bool
	loginStatus  int
}

func newFakeREEN(t *testing.T) *fakeREEN {
	t.Helper()
	f := &fakeREEN{routes: map[string]routeFn{}, token: "tok-1"}
	f.ts = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.ts.Close)
	return f
}

func (f *fakeREEN) route(path string, fn routeFn) { f.routes[path] = fn }

// static registers a route that always answers 200 with body.
func (f *fakeREEN) static(path, body string) {
	f.route(path, func(url.Values) (int, string) { return 200, body })
}

func (f *fakeREEN) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen = append(f.seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	if c := r.Header.Get("X-Customer"); c != "" {
		f.customerSeen = append(f.customerSeen, c)
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	if r.URL.Path == "/api/3/session" && r.Method == http.MethodPost {
		f.handleLogin(w, r)
		return
	}

	f.mu.Lock()
	want := f.token
	f.mu.Unlock()
	if r.Header.Get("X-Token") != want {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid or expired token"}`)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, APIVersionPath)
	fn, ok := f.routes[path]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"no such method"}`)
		return
	}
	status, body := fn(r.URL.Query())
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	_, _ = io.WriteString(w, body)
}

func (f *fakeREEN) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	f.logins++
	bad := f.badCreds || body.Username != "u" || body.Password != "p"
	status := f.loginStatus
	tok := f.token
	f.mu.Unlock()

	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":"boom"}`)
		return
	}
	if bad {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"authentication failed"}`)
		return
	}
	_, _ = io.WriteString(w, fmt.Sprintf(
		`{"href":"h","scope":"s","version":3,"generated":"2026-01-01T00:00:00Z",`+
			`"session":{"token":%q,"customer":12345,"user":678,"timezone":"Europe/Amsterdam"}}`, tok))
}

// rotateToken invalidates the currently issued token, so the next request
// using it gets a 401 and the client must re-authenticate.
func (f *fakeREEN) rotateToken(newTok string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token = newTok
}

func (f *fakeREEN) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

func (f *fakeREEN) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *fakeREEN) sawPathWith(substr string) bool {
	for _, s := range f.requests() {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestAdapter wires an Adapter at f with a fixed clock.
func newTestAdapter(t *testing.T, f *fakeREEN, mutate func(*Config)) *Adapter {
	t.Helper()
	cfg := Config{
		BaseURL:  f.ts.URL,
		Username: "u",
		Password: "p",
	}
	if mutate != nil {
		mutate(&cfg)
	}
	a, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.now = func() time.Time { return time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC) }
	return a
}

// ---------------------------------------------------------------------------
// construction / config
// ---------------------------------------------------------------------------

func TestNewRejectsIncompleteConfig(t *testing.T) {
	cases := map[string]Config{
		"no base url": {Username: "u", Password: "p"},
		"no username": {BaseURL: "https://api.reen.com", Password: "p"},
		"no password": {BaseURL: "https://api.reen.com", Username: "u"},
		"all missing": {},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(cfg, discardLogger()); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	a, err := New(Config{BaseURL: "https://api.reen.com", Username: "u", Password: "p"}, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.pageLimit != DefaultObservationPageLimit {
		t.Errorf("pageLimit = %d, want %d", a.pageLimit, DefaultObservationPageLimit)
	}
	if a.minConf != DefaultMinConfidence {
		t.Errorf("minConf = %d, want %d", a.minConf, DefaultMinConfidence)
	}
	if a.VendorID() != "sulo" {
		t.Errorf("VendorID = %q, want sulo", a.VendorID())
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://api.reen.com":        "https://api.reen.com/api/3",
		"https://api.reen.com/":       "https://api.reen.com/api/3",
		"https://api.reen.com/api/3":  "https://api.reen.com/api/3",
		"https://api.reen.com/api/3/": "https://api.reen.com/api/3",
		"  https://api.reen.com  ":    "https://api.reen.com/api/3",
		"":                            "",
	}
	for in, want := range cases {
		if got := normalizeBaseURL(in); got != want {
			t.Errorf("normalizeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// lenient scalar decoding (REEN types some numeric fields as strings)
// ---------------------------------------------------------------------------

func TestFlexInt64(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{`123`, 123, false},
		{`"123"`, 123, false},
		{`null`, 0, false},
		{`""`, 0, false},
		{`"abc"`, 0, true},
	}
	for _, c := range cases {
		var v flexInt64
		err := json.Unmarshal([]byte(c.in), &v)
		if c.wantErr {
			if err == nil {
				t.Errorf("Unmarshal(%s): want error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("Unmarshal(%s): %v", c.in, err)
			continue
		}
		if int64(v) != c.want {
			t.Errorf("Unmarshal(%s) = %d, want %d", c.in, int64(v), c.want)
		}
	}
}

func TestFlexFloat64(t *testing.T) {
	cases := []struct {
		in      string
		want    float64
		wantSet bool
		wantErr bool
	}{
		{`27`, 27, true, false},
		{`"27"`, 27, true, false},
		{`"27.5"`, 27.5, true, false},
		{`0`, 0, true, false},
		{`"0"`, 0, true, false},
		{`null`, 0, false, false},
		{`""`, 0, false, false},
		{`"n/a"`, 0, false, true},
	}
	for _, c := range cases {
		var v flexFloat64
		err := json.Unmarshal([]byte(c.in), &v)
		if c.wantErr {
			if err == nil {
				t.Errorf("Unmarshal(%s): want error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("Unmarshal(%s): %v", c.in, err)
			continue
		}
		if v.Value != c.want || v.Set != c.wantSet {
			t.Errorf("Unmarshal(%s) = {%v %v}, want {%v %v}", c.in, v.Value, v.Set, c.want, c.wantSet)
		}
	}
}

// ---------------------------------------------------------------------------
// session handling
// ---------------------------------------------------------------------------

func TestSessionTokenIsReusedAcrossRequests(t *testing.T) {
	f := newFakeREEN(t)
	f.static("/contentTypes", `{"count":0,"contentTypes":[]}`)

	a := newTestAdapter(t, f, nil)
	for range 3 {
		if _, err := a.fetchContentTypes(context.Background()); err != nil {
			t.Fatalf("fetchContentTypes: %v", err)
		}
	}
	if got := f.loginCount(); got != 1 {
		t.Errorf("logins = %d, want 1 (token should be cached)", got)
	}
}

func TestStaleTokenTriggersReloginAndRetry(t *testing.T) {
	f := newFakeREEN(t)
	f.static("/contentTypes", `{"count":1,"contentTypes":[{"id":1,"name":"Restafval"}]}`)

	a := newTestAdapter(t, f, nil)
	if _, err := a.fetchContentTypes(context.Background()); err != nil {
		t.Fatalf("first fetch: %v", err)
	}

	// The server expires the token behind our back.
	f.rotateToken("tok-2")

	got, err := a.fetchContentTypes(context.Background())
	if err != nil {
		t.Fatalf("fetch after token rotation: %v", err)
	}
	if len(got) != 1 || got[1].label() != "Restafval" {
		t.Fatalf("unexpected content types: %+v", got)
	}
	if n := f.loginCount(); n != 2 {
		t.Errorf("logins = %d, want 2 (one initial, one re-login)", n)
	}
}

func TestPersistent401IsPermanent(t *testing.T) {
	f := newFakeREEN(t)
	f.static("/contentTypes", `{"count":0,"contentTypes":[]}`)

	a := newTestAdapter(t, f, nil)
	// Every issued token is rejected: the login succeeds but hands out a
	// token the request path will not accept, so the retry 401s too.
	f.route("/contentTypes", func(url.Values) (int, string) {
		return http.StatusUnauthorized, `{"error":"insufficient rights"}`
	})

	_, err := a.fetchContentTypes(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	if !adapters.IsPermanent(err) {
		t.Errorf("err = %v, want permanent", err)
	}
	if n := f.loginCount(); n != 2 {
		t.Errorf("logins = %d, want 2 (retry once, then give up)", n)
	}
}

func TestBadCredentialsArePermanent(t *testing.T) {
	f := newFakeREEN(t)
	f.static("/contentTypes", `{"count":0,"contentTypes":[]}`)

	a := newTestAdapter(t, f, func(c *Config) { c.Password = "wrong" })
	_, err := a.fetchContentTypes(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	if !adapters.IsPermanent(err) {
		t.Errorf("err = %v, want permanent (a wrong password never fixes itself)", err)
	}
}

func TestLoginServerErrorIsTransient(t *testing.T) {
	f := newFakeREEN(t)
	f.loginStatus = http.StatusBadGateway
	f.static("/contentTypes", `{"count":0,"contentTypes":[]}`)

	a := newTestAdapter(t, f, nil)
	_, err := a.fetchContentTypes(context.Background())
	if !adapters.IsTransient(err) {
		t.Errorf("err = %v, want transient", err)
	}
}

func TestReloginIsSingleFlight(t *testing.T) {
	f := newFakeREEN(t)
	f.static("/containerSlots", `{"count":0,"containerSlots":[]}`)

	a := newTestAdapter(t, f, nil)

	// Many goroutines race for the very first token.
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			if _, err := a.fetchContainerSlots(context.Background()); err != nil {
				t.Errorf("fetchContainerSlots: %v", err)
			}
		})
	}
	wg.Wait()

	if n := f.loginCount(); n != 1 {
		t.Errorf("logins = %d, want 1 — concurrent callers must share one login", n)
	}
}

func TestCustomerScopeHeader(t *testing.T) {
	f := newFakeREEN(t)
	f.static("/contentTypes", `{"count":0,"contentTypes":[]}`)

	a := newTestAdapter(t, f, func(c *Config) { c.CustomerID = "5678" })
	if _, err := a.fetchContentTypes(context.Background()); err != nil {
		t.Fatalf("fetchContentTypes: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.customerSeen) == 0 {
		t.Fatal("X-Customer header was never sent")
	}
	for _, c := range f.customerSeen {
		if c != "5678" {
			t.Errorf("X-Customer = %q, want 5678", c)
		}
	}
}

// ---------------------------------------------------------------------------
// error classification
// ---------------------------------------------------------------------------

func TestClassifyStatus(t *testing.T) {
	cases := []struct {
		status      int
		header      http.Header
		wantTransie bool
		wantPerm    bool
	}{
		{200, nil, false, false},
		{204, nil, false, false},
		{400, nil, false, true},
		{401, nil, false, true},
		{403, nil, false, true},
		{404, nil, false, true},
		{422, nil, false, true},
		{429, nil, true, false},
		{500, nil, true, false},
		{503, nil, true, false},
	}
	for _, c := range cases {
		err := classifyStatus("/x", c.status, c.header, []byte(`{"error":"detail here"}`))
		switch {
		case !c.wantTransie && !c.wantPerm:
			if err != nil {
				t.Errorf("status %d: want nil, got %v", c.status, err)
			}
		case c.wantTransie:
			if !adapters.IsTransient(err) {
				t.Errorf("status %d: want transient, got %v", c.status, err)
			}
		case c.wantPerm:
			if !adapters.IsPermanent(err) {
				t.Errorf("status %d: want permanent, got %v", c.status, err)
			}
		}
		if err != nil && !strings.Contains(err.Error(), "detail here") {
			t.Errorf("status %d: error should surface the REEN message, got %v", c.status, err)
		}
	}
}

func TestClassifyStatusHonorsRetryAfter(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "42")
	err := classifyStatus("/x", http.StatusTooManyRequests, h, nil)

	var te *adapters.TransientError
	if !asTransient(err, &te) {
		t.Fatalf("want TransientError, got %v", err)
	}
	if te.RetryAfter != 42*time.Second {
		t.Errorf("RetryAfter = %v, want 42s", te.RetryAfter)
	}
}

func TestClassifyStatusClampsRetryAfter(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "99999")
	err := classifyStatus("/x", http.StatusTooManyRequests, h, nil)

	var te *adapters.TransientError
	if !asTransient(err, &te) {
		t.Fatalf("want TransientError, got %v", err)
	}
	if te.RetryAfter != adapters.MaxRetryAfter {
		t.Errorf("RetryAfter = %v, want clamped to %v", te.RetryAfter, adapters.MaxRetryAfter)
	}
}

func asTransient(err error, out **adapters.TransientError) bool {
	te, ok := err.(*adapters.TransientError)
	if ok {
		*out = te
	}
	return ok
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("30"); got != 30*time.Second {
		t.Errorf("seconds form = %v, want 30s", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("empty = %v, want 0", got)
	}
	if got := parseRetryAfter("garbage"); got != 0 {
		t.Errorf("garbage = %v, want 0", got)
	}
	if got := parseRetryAfter("-5"); got != 0 {
		t.Errorf("negative = %v, want 0", got)
	}
	// HTTP-date form.
	future := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got <= 0 {
		t.Errorf("date form = %v, want > 0", got)
	}
}

// ---------------------------------------------------------------------------
// paging
// ---------------------------------------------------------------------------

func TestPagedFollowsLimitOffset(t *testing.T) {
	f := newFakeREEN(t)

	// 5 slots served 2 at a time.
	all := make([]string, 5)
	for i := range all {
		all[i] = fmt.Sprintf(`{"id":%d,"name":"slot-%d","site":1}`, i+1, i+1)
	}
	f.route("/containerSlots", func(q url.Values) (int, string) {
		limit := atoiOr(q.Get("limit"), 100)
		offset := atoiOr(q.Get("offset"), 0)
		offset = min(offset, len(all))
		end := min(offset+limit, len(all))
		page := all[offset:end]
		return 200, fmt.Sprintf(`{"count":%d,"containerSlots":[%s]}`, len(page), strings.Join(page, ","))
	})

	a := newTestAdapter(t, f, func(c *Config) { c.PageSize = 2 })
	slots, err := a.fetchContainerSlots(context.Background())
	if err != nil {
		t.Fatalf("fetchContainerSlots: %v", err)
	}
	if len(slots) != 5 {
		t.Fatalf("got %d slots, want 5", len(slots))
	}
	for i, s := range slots {
		if s.ID != int64(i+1) {
			t.Errorf("slot[%d].ID = %d, want %d", i, s.ID, i+1)
		}
	}
}

func TestPagedStopsOnRepeatingServer(t *testing.T) {
	f := newFakeREEN(t)
	// A server that ignores offset and always returns a full page would
	// otherwise loop forever; the page cap must stop it.
	f.route("/containerSlots", func(q url.Values) (int, string) {
		limit := atoiOr(q.Get("limit"), 2)
		rows := make([]string, limit)
		for i := range rows {
			rows[i] = `{"id":1,"name":"same","site":1}`
		}
		return 200, fmt.Sprintf(`{"count":%d,"containerSlots":[%s]}`, limit, strings.Join(rows, ","))
	})

	a := newTestAdapter(t, f, func(c *Config) { c.PageSize = 2 })
	slots, err := a.fetchContainerSlots(context.Background())
	if err != nil {
		t.Fatalf("fetchContainerSlots: %v", err)
	}
	if want := maxPages * 2; len(slots) != want {
		t.Errorf("got %d slots, want the page cap to stop at %d", len(slots), want)
	}
}

func atoiOr(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return fallback
	}
	return n
}

// ---------------------------------------------------------------------------
// discovery
// ---------------------------------------------------------------------------

// seedDiscovery wires a small but complete REEN data set:
//   - slot 10 at site 1, container 100, content type 7, device linked
//   - slot 11 at site 1, container 0 (no device)
//   - slot 12 at site 2, which has no coordinates
func seedDiscovery(f *fakeREEN) {
	f.static("/containerSlots", `{"count":3,"containerSlots":[
		{"id":10,"name":"Slot A","customerKey":"ext-10","contentType":7,"siteContentType":70,"site":1,"container":100,"fillLevel":42},
		{"id":11,"name":"Slot B","contentType":7,"siteContentType":70,"site":"1","container":0},
		{"id":12,"name":"Slot C","contentType":7,"site":2,"container":102}
	]}`)
	f.static("/sites", `{"count":2,"sites":[
		{"id":1,"name":"Rembrandtplein","typeName":"Ondergronds","areaName":"Centrum",
		 "address":"Rembrandtplein 1","city":"Amsterdam","postalCode":"1017 CT","country":"Netherlands",
		 "latitude":52.3661,"longitude":4.8956},
		{"id":2,"name":"No Coords Site","city":"Utrecht"}
	]}`)
	f.static("/devices/linked", `{"count":1,"devices":[
		{"id":900,"brand":"REEN","model":"US-3","serial":"SN-900","container":100,
		 "installed":"2026-01-05T10:00:00Z","lastConnection":"2026-07-30T06:00:00Z",
		 "status":{"signal":78,"temperature":19,"batteryPercentage":91}}
	]}`)
	f.static("/contentTypes", `{"count":1,"contentTypes":[
		{"id":7,"name":"Restafval","englishName":"Residual waste","categoryName":"Household","state":"solid"}
	]}`)
}

func TestListThings(t *testing.T) {
	f := newFakeREEN(t)
	seedDiscovery(f)
	a := newTestAdapter(t, f, nil)

	things, err := a.ListThings(context.Background())
	if err != nil {
		t.Fatalf("ListThings: %v", err)
	}

	// Slot 12's site has no coordinates, so it must be skipped rather than
	// published at Point(0,0).
	if len(things) != 2 {
		t.Fatalf("got %d things, want 2 (slot 12 has no site coordinates): %+v", len(things), things)
	}

	byID := map[string]canonical.Thing{}
	for _, th := range things {
		byID[th.VendorNativeID] = th
	}

	a10, ok := byID["10"]
	if !ok {
		t.Fatal("slot 10 missing")
	}
	if a10.VendorID != VendorID {
		t.Errorf("VendorID = %q, want %q", a10.VendorID, VendorID)
	}
	// Name must stay empty so the OMS mapper synthesises the
	// Implementation-Contract name that the scheduler also stores.
	if a10.Name != "" {
		t.Errorf("Name = %q, want empty so oms.ThingName applies", a10.Name)
	}
	// REEN reports (lat, lon); canonical.Coord is (lon, lat).
	if a10.Location.Lat != 52.3661 || a10.Location.Lon != 4.8956 {
		t.Errorf("Location = %+v, want {Lon:4.8956 Lat:52.3661} — lat/lon must be swapped", a10.Location)
	}
	for k, want := range map[string]string{
		"reen_container_slot_id":   "10",
		"reen_container_slot_name": "Slot A",
		"reen_customer_key":        "ext-10",
		"reen_site_id":             "1",
		"reen_content_type_id":     "7",
		"reen_content_type_name":   "Restafval",
		"reen_device_id":           "900",
		"device_serial":            "SN-900",
		"city":                     "Amsterdam",
		"postal_code":              "1017 CT",
	} {
		if got := a10.Properties[k]; got != want {
			t.Errorf("Properties[%q] = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(a10.Description, "Restafval") || !strings.Contains(a10.Description, "Rembrandtplein") {
		t.Errorf("Description = %q, want it to mention the content type and site", a10.Description)
	}

	// Slot 11 proves the string-typed "site" field decodes too.
	a11, ok := byID["11"]
	if !ok {
		t.Fatal("slot 11 missing")
	}
	if a11.Location.Lat != 52.3661 {
		t.Errorf(`slot 11 Location = %+v, want the site resolved from the string-typed "site" field`, a11.Location)
	}
}

func TestListThingsEnrichmentIsBestEffort(t *testing.T) {
	f := newFakeREEN(t)
	seedDiscovery(f)
	// Devices and content types fail; slots and sites still succeed.
	f.route("/devices/linked", func(url.Values) (int, string) {
		return http.StatusForbidden, `{"error":"requires device installation rights"}`
	})
	f.route("/contentTypes", func(url.Values) (int, string) {
		return http.StatusInternalServerError, `{"error":"boom"}`
	})

	a := newTestAdapter(t, f, nil)
	things, err := a.ListThings(context.Background())
	if err != nil {
		t.Fatalf("ListThings must tolerate enrichment failures, got: %v", err)
	}
	if len(things) != 2 {
		t.Fatalf("got %d things, want 2", len(things))
	}
}

func TestListThingsFailsWhenSlotsOrSitesFail(t *testing.T) {
	t.Run("slots", func(t *testing.T) {
		f := newFakeREEN(t)
		seedDiscovery(f)
		f.route("/containerSlots", func(url.Values) (int, string) {
			return http.StatusInternalServerError, `{"error":"boom"}`
		})
		a := newTestAdapter(t, f, nil)
		if _, err := a.ListThings(context.Background()); err == nil {
			t.Fatal("want error when container slots cannot be listed")
		}
	})
	t.Run("sites", func(t *testing.T) {
		f := newFakeREEN(t)
		seedDiscovery(f)
		f.route("/sites", func(url.Values) (int, string) {
			return http.StatusInternalServerError, `{"error":"boom"}`
		})
		a := newTestAdapter(t, f, nil)
		if _, err := a.ListThings(context.Background()); err == nil {
			t.Fatal("want error when sites (and therefore coordinates) cannot be listed")
		}
	})
}

func TestListDatastreamsForThing(t *testing.T) {
	f := newFakeREEN(t)
	seedDiscovery(f)
	a := newTestAdapter(t, f, func(c *Config) { c.ExpectedCadenceSeconds = 14400 })

	if _, err := a.ListThings(context.Background()); err != nil {
		t.Fatalf("ListThings: %v", err)
	}

	ds, err := a.ListDatastreamsForThing(context.Background(), "10")
	if err != nil {
		t.Fatalf("ListDatastreamsForThing: %v", err)
	}
	if len(ds) != 1 {
		t.Fatalf("got %d datastreams, want 1", len(ds))
	}
	d := ds[0]
	if d.ObservedProperty != canonical.FillLevel {
		t.Errorf("ObservedProperty = %q, want %q", d.ObservedProperty, canonical.FillLevel)
	}
	if d.Unit != canonical.Percent {
		t.Errorf("Unit = %q, want %q", d.Unit, canonical.Percent)
	}
	if d.ThingVendorNativeID != "10" {
		t.Errorf("ThingVendorNativeID = %q, want 10", d.ThingVendorNativeID)
	}
	if d.ExpectedCadenceSeconds != 14400 {
		t.Errorf("ExpectedCadenceSeconds = %d, want 14400", d.ExpectedCadenceSeconds)
	}
	// The mapper reads these two keys to build the Sensor name.
	if d.SensorMetadata["model"] != "US-3" {
		t.Errorf("SensorMetadata[model] = %q, want US-3", d.SensorMetadata["model"])
	}
	if d.SensorMetadata["firmware_version"] == "" {
		t.Error("SensorMetadata[firmware_version] must be set for the Sensor name template")
	}
	if d.SensorMetadata["serial"] != "SN-900" {
		t.Errorf("SensorMetadata[serial] = %q, want SN-900", d.SensorMetadata["serial"])
	}
	// Passthrough naming must stay empty so canonical fill-level names win.
	if d.Name != "" || d.ObservedPropertyName != "" || d.UnitSymbol != "" {
		t.Errorf("passthrough fields must be empty for a fixed-phenomenon vendor: %+v", d)
	}
	if !strings.Contains(d.Description, "Restafval") {
		t.Errorf("Description = %q, want it to name the waste fraction", d.Description)
	}
}

func TestListDatastreamsForUnknownSlot(t *testing.T) {
	f := newFakeREEN(t)
	a := newTestAdapter(t, f, nil)

	// No discovery pass ran, so the cache is empty — the stream still exists.
	ds, err := a.ListDatastreamsForThing(context.Background(), "99")
	if err != nil {
		t.Fatalf("ListDatastreamsForThing: %v", err)
	}
	if len(ds) != 1 || ds[0].ObservedProperty != canonical.FillLevel {
		t.Fatalf("got %+v, want one fill-level stream", ds)
	}
	if ds[0].SensorMetadata["model"] != "reen-unlinked" {
		t.Errorf("model = %q, want the unlinked placeholder", ds[0].SensorMetadata["model"])
	}

	if _, err := a.ListDatastreamsForThing(context.Background(), "not-a-number"); !adapters.IsPermanent(err) {
		t.Errorf("err = %v, want permanent for a non-numeric slot id", err)
	}
}

// ---------------------------------------------------------------------------
// observations
// ---------------------------------------------------------------------------

func TestFetchObservations(t *testing.T) {
	f := newFakeREEN(t)
	// REEN returns newest-first, and mixes matured, interpolated and
	// predicted rows. "now" in tests is 2026-07-30T12:00:00Z.
	f.static("/fillLevels/containerSlot/10", `{"count":5,"fillLevels":[
		{"time":"2026-07-31T00:00:00Z","fillLevel":"95","containerSlot":10,"confidence":100,"frozen":false},
		{"time":"2026-07-30T11:00:00Z","fillLevel":"71","containerSlot":10,"confidence":100,"frozen":true},
		{"time":"2026-07-30T10:00:00Z","fillLevel":"64","containerSlot":10,"confidence":60,"frozen":true},
		{"time":"2026-07-30T09:00:00Z","fillLevel":"999","containerSlot":10,"confidence":0,"frozen":true},
		{"time":"2026-07-30T08:00:00Z","fillLevel":"55","containerSlot":10,"confidence":100,"frozen":true}
	]}`)

	a := newTestAdapter(t, f, nil)
	since := time.Date(2026, 7, 30, 7, 0, 0, 0, time.UTC)
	obs, err := a.FetchObservations(context.Background(), "10", canonical.FillLevel, since, 0)
	if err != nil {
		t.Fatalf("FetchObservations: %v", err)
	}

	// Kept: 08:00 (55), 10:00 (64, confidence 60 is interpolated but valid),
	// 11:00 (71). Dropped: the 31 July forecast, and the confidence-0 row.
	wantResults := []float64{55, 64, 71}
	if len(obs) != len(wantResults) {
		t.Fatalf("got %d observations, want %d: %+v", len(obs), len(wantResults), obs)
	}
	for i, want := range wantResults {
		if obs[i].Result != want {
			t.Errorf("obs[%d].Result = %v, want %v", i, obs[i].Result, want)
		}
	}

	// Ascending order is required by the cursor arithmetic.
	for i := 1; i < len(obs); i++ {
		if !obs[i].PhenomenonTime.After(obs[i-1].PhenomenonTime) {
			t.Errorf("observations must be ascending: %v then %v", obs[i-1].PhenomenonTime, obs[i].PhenomenonTime)
		}
	}

	last := obs[len(obs)-1]
	if last.ObservedProperty != canonical.FillLevel {
		t.Errorf("ObservedProperty = %q", last.ObservedProperty)
	}
	if last.ThingVendorNativeID != "10" {
		t.Errorf("ThingVendorNativeID = %q, want 10", last.ThingVendorNativeID)
	}
	if !last.ResultTime.Equal(last.PhenomenonTime) {
		t.Errorf("ResultTime %v should equal PhenomenonTime %v", last.ResultTime, last.PhenomenonTime)
	}
	if want := "10@2026-07-30T11:00:00Z"; last.RawObservationID != want {
		t.Errorf("RawObservationID = %q, want %q", last.RawObservationID, want)
	}
	if last.PhenomenonTime.Location() != time.UTC {
		t.Errorf("PhenomenonTime must be UTC, got %v", last.PhenomenonTime.Location())
	}

	// The cursor must be pushed to REEN as the exclusive "after" bound.
	if !f.sawPathWith("after=2026-07-30T07%3A00%3A00Z") {
		t.Errorf("expected an after= query for the cursor, got %v", f.requests())
	}
}

// A predicted (future-dated) row must never reach the ingest core: the
// validator counts in_future as a definitive rejection, so the cursor would
// advance to the forecast date and silently skip every real measurement
// until then.
func TestFetchObservationsDropsPredictedFutureRows(t *testing.T) {
	f := newFakeREEN(t)
	f.static("/fillLevels/containerSlot/10", `{"count":3,"fillLevels":[
		{"time":"2026-08-05T00:00:00Z","fillLevel":"100","containerSlot":10,"confidence":100,"frozen":false},
		{"time":"2026-08-02T00:00:00Z","fillLevel":"88","containerSlot":10,"confidence":100,"frozen":false},
		{"time":"2026-07-30T11:59:00Z","fillLevel":"70","containerSlot":10,"confidence":100,"frozen":true}
	]}`)

	a := newTestAdapter(t, f, nil)
	obs, err := a.FetchObservations(context.Background(), "10", canonical.FillLevel, time.Time{}, 0)
	if err != nil {
		t.Fatalf("FetchObservations: %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("got %d observations, want only the past one: %+v", len(obs), obs)
	}
	if got := obs[0].PhenomenonTime; !got.Equal(time.Date(2026, 7, 30, 11, 59, 0, 0, time.UTC)) {
		t.Errorf("PhenomenonTime = %v, want the 11:59 measurement", got)
	}
}

func TestFetchObservationsFiltersAndDedupes(t *testing.T) {
	f := newFakeREEN(t)
	f.static("/fillLevels/containerSlot/10", `{"count":5,"fillLevels":[
		{"time":"2026-07-30T10:00:00Z","fillLevel":"64","containerSlot":10},
		{"time":"2026-07-30T10:00:00Z","fillLevel":"64","containerSlot":10},
		{"time":"2026-07-30T09:00:00Z","containerSlot":10},
		{"time":"not-a-time","fillLevel":"5","containerSlot":10},
		{"time":"2026-07-30T08:00:00Z","fillLevel":0,"containerSlot":10}
	]}`)

	a := newTestAdapter(t, f, nil)
	obs, err := a.FetchObservations(context.Background(), "10", canonical.FillLevel, time.Time{}, 0)
	if err != nil {
		t.Fatalf("FetchObservations: %v", err)
	}
	// 10:00 de-duplicated to one; the value-less row and the unparseable
	// timestamp dropped; a genuine 0 percent reading kept.
	if len(obs) != 2 {
		t.Fatalf("got %d observations, want 2: %+v", len(obs), obs)
	}
	if obs[0].Result != 0 {
		t.Errorf("obs[0].Result = %v, want a real 0%% reading to survive", obs[0].Result)
	}
	if obs[1].Result != 64 {
		t.Errorf("obs[1].Result = %v, want 64", obs[1].Result)
	}
}

func TestFetchObservationsRespectsCursorStrictly(t *testing.T) {
	f := newFakeREEN(t)
	// A row exactly at the cursor must not be re-emitted.
	f.static("/fillLevels/containerSlot/10", `{"count":2,"fillLevels":[
		{"time":"2026-07-30T10:00:00Z","fillLevel":"64","containerSlot":10},
		{"time":"2026-07-30T09:00:00Z","fillLevel":"60","containerSlot":10}
	]}`)

	a := newTestAdapter(t, f, nil)
	since := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	obs, err := a.FetchObservations(context.Background(), "10", canonical.FillLevel, since, 0)
	if err != nil {
		t.Fatalf("FetchObservations: %v", err)
	}
	if len(obs) != 1 || obs[0].Result != 64 {
		t.Fatalf("got %+v, want only the 10:00 row", obs)
	}
}

func TestFetchObservationsMinConfidenceOverride(t *testing.T) {
	f := newFakeREEN(t)
	f.static("/fillLevels/containerSlot/10", `{"count":3,"fillLevels":[
		{"time":"2026-07-30T11:00:00Z","fillLevel":"71","containerSlot":10,"confidence":100},
		{"time":"2026-07-30T10:00:00Z","fillLevel":"64","containerSlot":10,"confidence":80},
		{"time":"2026-07-30T09:00:00Z","fillLevel":"60","containerSlot":10,"confidence":60}
	]}`)

	a := newTestAdapter(t, f, func(c *Config) { c.MinConfidence = 80 })
	obs, err := a.FetchObservations(context.Background(), "10", canonical.FillLevel, time.Time{}, 0)
	if err != nil {
		t.Fatalf("FetchObservations: %v", err)
	}
	if len(obs) != 2 {
		t.Fatalf("got %d observations, want 2 (confidence 60 excluded): %+v", len(obs), obs)
	}
}

func TestFetchObservationsIgnoresOtherProperties(t *testing.T) {
	f := newFakeREEN(t)
	a := newTestAdapter(t, f, nil)

	obs, err := a.FetchObservations(context.Background(), "10", canonical.Temperature, time.Time{}, 0)
	if err != nil {
		t.Fatalf("FetchObservations: %v", err)
	}
	if obs != nil {
		t.Errorf("got %+v, want nil for a non-fill-level property", obs)
	}
	if len(f.requests()) != 0 {
		t.Errorf("no HTTP request should be made, got %v", f.requests())
	}
}

func TestFetchObservationsRejectsBadNativeID(t *testing.T) {
	f := newFakeREEN(t)
	a := newTestAdapter(t, f, nil)

	_, err := a.FetchObservations(context.Background(), "slot-abc", canonical.FillLevel, time.Time{}, 0)
	if !adapters.IsPermanent(err) {
		t.Errorf("err = %v, want permanent", err)
	}
}

func TestFetchObservationsPropagatesTransientError(t *testing.T) {
	f := newFakeREEN(t)
	f.route("/fillLevels/containerSlot/10", func(url.Values) (int, string) {
		return http.StatusServiceUnavailable, `{"error":"maintenance"}`
	})

	a := newTestAdapter(t, f, nil)
	_, err := a.FetchObservations(context.Background(), "10", canonical.FillLevel, time.Time{}, 0)
	if !adapters.IsTransient(err) {
		t.Errorf("err = %v, want transient", err)
	}
}

func TestFetchObservationsHonorsLimit(t *testing.T) {
	f := newFakeREEN(t)
	var gotLimit string
	f.route("/fillLevels/containerSlot/10", func(q url.Values) (int, string) {
		gotLimit = q.Get("limit")
		return 200, `{"count":0,"fillLevels":[]}`
	})

	a := newTestAdapter(t, f, nil)
	if _, err := a.FetchObservations(context.Background(), "10", canonical.FillLevel, time.Time{}, 7); err != nil {
		t.Fatalf("FetchObservations: %v", err)
	}
	if gotLimit != "7" {
		t.Errorf("limit = %q, want 7", gotLimit)
	}
}

// ---------------------------------------------------------------------------
// mapping helpers
// ---------------------------------------------------------------------------

func TestParseREENTime(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		want time.Time
	}{
		{"2026-07-30T11:00:00Z", true, time.Date(2026, 7, 30, 11, 0, 0, 0, time.UTC)},
		{"2026-07-30T13:00:00+02:00", true, time.Date(2026, 7, 30, 11, 0, 0, 0, time.UTC)},
		{"2026-07-30T11:00:00.500Z", true, time.Date(2026, 7, 30, 11, 0, 0, 5e8, time.UTC)},
		{"", false, time.Time{}},
		{"30-07-2026", false, time.Time{}},
	}
	for _, c := range cases {
		got, ok := parseREENTime(c.in)
		if ok != c.ok {
			t.Errorf("parseREENTime(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && !got.Equal(c.want) {
			t.Errorf("parseREENTime(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestObservationIDIsStable(t *testing.T) {
	ts := time.Date(2026, 7, 30, 11, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	a := observationID(10, ts)
	b := observationID(10, ts.UTC())
	if a != b {
		t.Errorf("observationID must normalise to UTC: %q vs %q", a, b)
	}
	if want := "10@2026-07-30T09:00:00Z"; a != want {
		t.Errorf("observationID = %q, want %q", a, want)
	}
}

func TestSlotViewCoordRequiresBothOrdinates(t *testing.T) {
	lat, lon := 52.0, 4.0
	cases := map[string]slotView{
		"no site": {},
		"no lat":  {site: &siteDTO{Longitude: &lon}},
		"no lon":  {site: &siteDTO{Latitude: &lat}},
	}
	for name, v := range cases {
		if _, ok := v.coord(); ok {
			t.Errorf("%s: coord() should report ok=false", name)
		}
	}
	full := slotView{site: &siteDTO{Latitude: &lat, Longitude: &lon}}
	c, ok := full.coord()
	if !ok || c.Lat != lat || c.Lon != lon {
		t.Errorf("coord() = %+v ok=%v, want {Lon:4 Lat:52}", c, ok)
	}
}

func TestContentTypeLabelFallback(t *testing.T) {
	cases := []struct {
		in   contentTypeDTO
		want string
	}{
		{contentTypeDTO{Name: "Restafval", EnglishName: "Residual"}, "Restafval"},
		{contentTypeDTO{EnglishName: "Residual"}, "Residual"},
		{contentTypeDTO{CategoryName: "Household"}, "Household"},
		{contentTypeDTO{}, ""},
	}
	for _, c := range cases {
		if got := c.in.label(); got != c.want {
			t.Errorf("label(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestErrorResponseMessagePrecedence(t *testing.T) {
	if got := (errorResponse{Message: "m", Detail: "d"}).message(); got != "m" {
		t.Errorf("message() = %q, want m", got)
	}
	if got := (errorResponse{Detail: "d"}).message(); got != "d" {
		t.Errorf("message() = %q, want d", got)
	}
	if got := (errorResponse{}).message(); got != "" {
		t.Errorf("message() = %q, want empty", got)
	}
}

// The adapter must satisfy the poll contract the scheduler drives.
func TestImplementsPollAdapter(t *testing.T) {
	var _ adapters.PollAdapter = (*Adapter)(nil)
}
