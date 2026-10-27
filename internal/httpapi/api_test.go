package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/codebyjuliamendes/entregaviva/internal/config"
	"github.com/codebyjuliamendes/entregaviva/internal/events"
	"github.com/codebyjuliamendes/entregaviva/internal/store"
)

type env struct {
	srv   *httptest.Server
	store *store.Store
	hub   *events.Hub
}

func newEnv(t *testing.T, mutate ...func(*config.Config)) *env {
	t.Helper()
	cfg, err := config.Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	cfg.SSEHeartbeat = 50 * time.Millisecond
	cfg.TrackRatePerMin = 1000
	for _, m := range mutate {
		m(&cfg)
	}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "api.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	hub := events.NewHub(events.Options{MaxSubscribers: cfg.SSEMaxClients})
	static := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><title>painel</title>")},
		"track.html": {Data: []byte("<!doctype html><title>rastreio</title>")},
		"app.js":     {Data: []byte("// app")},
		"styles.css": {Data: []byte("body{}")},
	}
	h, err := New(Deps{Store: st, Hub: hub, Config: cfg, Static: static})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(func() {
		hub.Close()
		srv.Close()
		_ = st.Close()
	})
	return &env{srv: srv, store: st, hub: hub}
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

func (e *env) do(t *testing.T, method, path, body string, hdr ...string) resp {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, header: res.Header, raw: raw}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

const validOrder = `{"restaurant":"Corner Cafe","customer":"Guest Person","address":"1 Main St","total":25,"estimatedMinutes":60}`

func (e *env) create(t *testing.T) map[string]any {
	t.Helper()
	r := e.do(t, http.MethodPost, "/api/orders", validOrder)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	return r.body
}

// Port of test/api.test.js from the Node backend.
func TestTrackingFlowMatchesLegacyContract(t *testing.T) {
	e := newEnv(t)
	invalid := e.do(t, http.MethodPost, "/api/orders", `{"restaurant":"Corner Cafe","customer":"Guest Person","address":"1 Main St","total":25,"estimatedMinutes":5}`)
	if invalid.status != 400 || invalid.body["error"] == "" {
		t.Fatalf("invalid: %d %s", invalid.status, invalid.raw)
	}
	created := e.create(t)
	if created["estimatedMinutes"].(float64) != 60 || created["code"] == "" || created["totalCents"].(float64) != 2500 {
		t.Fatalf("created %v", created)
	}
	code := created["code"].(string)
	id := int(created["id"].(float64))

	if r := e.do(t, http.MethodGet, "/track.html", ""); r.status != 200 {
		t.Fatalf("track.html: %d", r.status)
	}
	tr := e.do(t, http.MethodGet, "/api/orders/track/"+strings.ToLower(code), "")
	if tr.status != 200 || tr.body["status"] != "recebido" || tr.body["remainingMinutes"].(float64) > 60 {
		t.Fatalf("track: %d %s", tr.status, tr.raw)
	}
	for _, private := range []string{"address", "customer", "courier", "totalCents", "id"} {
		if _, ok := tr.body[private]; ok {
			t.Errorf("tracking exposes %q", private)
		}
	}
	if strings.Contains(string(tr.raw), "note") {
		t.Error("tracking exposes free-text notes")
	}

	bad := e.do(t, http.MethodPatch, "/api/orders/"+itoa(id)+"/status", `{"status":"entregue"}`)
	if bad.status != 409 {
		t.Fatalf("bad transition: %d", bad.status)
	}
	next := e.do(t, http.MethodPatch, "/api/orders/"+itoa(id)+"/status", `{"status":"preparando"}`)
	if next.status != 200 || next.body["status"] != "preparando" {
		t.Fatalf("next: %d %s", next.status, next.raw)
	}
	up := e.do(t, http.MethodGet, "/api/orders/track/"+code, "")
	if up.body["status"] != "preparando" || len(up.body["events"].([]any)) != 2 {
		t.Fatalf("updated track: %s", up.raw)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestCreateAcceptsFormStringsAndReportsFields(t *testing.T) {
	e := newEnv(t)
	r := e.do(t, http.MethodPost, "/api/orders", `{"restaurant":"  Pizza   da Vila ","customer":"Caio","address":"Av. Central, 81","courier":"","total":"82,90","estimatedMinutes":""}`)
	if r.status != 201 || r.body["totalCents"].(float64) != 8290 || r.body["estimatedMinutes"].(float64) != 45 || r.body["restaurant"] != "Pizza da Vila" {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	if !strings.HasSuffix(r.body["createdAt"].(string), "Z") || r.body["estimatedDeliveryAt"] == nil {
		t.Fatalf("timestamps must be UTC: %s", r.raw)
	}
	r = e.do(t, http.MethodPost, "/api/orders", `{"restaurant":"a","customer":"Caio","address":"Av","total":"abc","estimatedMinutes":"45.5"}`)
	fields, _ := r.body["fields"].(map[string]any)
	for _, f := range []string{"restaurant", "address", "total", "estimatedMinutes"} {
		if fields[f] == nil {
			t.Errorf("missing field error %q in %s", f, r.raw)
		}
	}
	if r.status != 400 || r.body["error"] != msgInvalidOrder {
		t.Fatalf("%d %s", r.status, r.raw)
	}
}

func TestBodyErrors(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{"", "{", `{"restaurant":true}`, validOrder + validOrder} {
		if r := e.do(t, http.MethodPost, "/api/orders", body); r.status != 400 || r.body["error"] != msgBadBody {
			t.Errorf("body %q: %d %s", body, r.status, r.raw)
		}
	}
	if r := e.do(t, http.MethodPost, "/api/orders", validOrder, "Content-Type", "text/plain"); r.status != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain must be rejected (CSRF): %d", r.status)
	}
	if r := e.do(t, http.MethodPost, "/api/orders", validOrder, "Content-Type", "application/json; charset=utf-8"); r.status != http.StatusCreated {
		t.Fatalf("json with charset: %d", r.status)
	}
	big := `{"restaurant":"` + strings.Repeat("x", maxBodyBytes) + `"}`
	if r := e.do(t, http.MethodPost, "/api/orders", big); r.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("big body: %d", r.status)
	}
}

func TestStatusEndpointErrors(t *testing.T) {
	e := newEnv(t)
	o := e.create(t)
	id := itoa(int(o["id"].(float64)))
	cases := []struct {
		path, body string
		status     int
	}{
		{"/api/orders/abc/status", `{"status":"preparando"}`, 404},
		{"/api/orders/999/status", `{"status":"preparando"}`, 404},
		{"/api/orders/" + id + "/status", `{"status":"voando"}`, 400},
		{"/api/orders/" + id + "/status", `{"status":"preparando","note":"` + strings.Repeat("n", 201) + `"}`, 400},
		{"/api/orders/" + id + "/status", `{"status":"preparando","from":"pronto"}`, 409},
	}
	for _, c := range cases {
		if r := e.do(t, http.MethodPatch, c.path, c.body); r.status != c.status {
			t.Errorf("%s %s: got %d %s", c.path, c.body, r.status, r.raw)
		}
	}
	ok := e.do(t, http.MethodPatch, "/api/orders/"+id+"/status", `{"status":"cancelado","from":"recebido","note":"cliente desistiu"}`)
	if ok.status != 200 || ok.body["status"] != "cancelado" {
		t.Fatalf("%d %s", ok.status, ok.raw)
	}
	again := e.do(t, http.MethodPatch, "/api/orders/"+id+"/status", `{"status":"preparando"}`)
	if again.status != 409 || !strings.Contains(again.body["error"].(string), "Cancelado") {
		t.Fatalf("terminal: %d %s", again.status, again.raw)
	}
}

func TestListOrders(t *testing.T) {
	e := newEnv(t)
	e.create(t)
	r := e.do(t, http.MethodGet, "/api/orders", "")
	var list []map[string]any
	if err := json.Unmarshal(r.raw, &list); err != nil || len(list) != 1 {
		t.Fatalf("%s %v", r.raw, err)
	}
	for _, k := range []string{"id", "code", "restaurant", "customer", "courier", "address", "totalCents", "estimatedMinutes", "status", "createdAt", "updatedAt", "remainingMinutes", "events"} {
		if _, ok := list[0][k]; !ok {
			t.Errorf("missing %q", k)
		}
	}
}

func TestTrackNotFoundAndLongCode(t *testing.T) {
	e := newEnv(t)
	for _, c := range []string{"EV-NOPE-NOPE", strings.Repeat("A", 40)} {
		if r := e.do(t, http.MethodGet, "/api/orders/track/"+c, ""); r.status != 404 || r.body["error"] != msgTrackNotFound {
			t.Errorf("%s: %d %s", c, r.status, r.raw)
		}
	}
}

func TestHealth(t *testing.T) {
	e := newEnv(t)
	if r := e.do(t, http.MethodGet, "/api/health", ""); r.status != 200 || r.body["status"] != "ok" {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	_ = e.store.Close()
	if r := e.do(t, http.MethodGet, "/api/health", ""); r.status != 503 {
		t.Fatalf("closed db: %d", r.status)
	}
}

func TestUnknownAPIRouteAndMethod(t *testing.T) {
	e := newEnv(t)
	if r := e.do(t, http.MethodGet, "/api/nada", ""); r.status != 404 || r.body["error"] != msgAPINotFound {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	if r := e.do(t, http.MethodDelete, "/api/orders", ""); r.status != 405 {
		t.Fatalf("method: %d %s", r.status, r.raw)
	}
}

func TestSecurityHeadersAndRequestID(t *testing.T) {
	e := newEnv(t)
	r := e.do(t, http.MethodGet, "/", "", "X-Request-Id", "abc-123")
	if r.status != 200 || !strings.Contains(r.header.Get("Content-Security-Policy"), "script-src 'self'") ||
		r.header.Get("X-Content-Type-Options") != "nosniff" || r.header.Get("X-Request-Id") != "abc-123" || r.header.Get("ETag") == "" {
		t.Fatalf("headers %v", r.header)
	}
	if r := e.do(t, http.MethodGet, "/", "", "X-Request-Id", "bad id with spaces"); r.header.Get("X-Request-Id") == "bad id with spaces" {
		t.Fatal("malformed request id must be replaced")
	}
}

func TestOperatorAuth(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.OperatorPassword = "senha-muito-segura" })
	for _, p := range []string{"/", "/index.html", "/app.js", "/api/orders", "/api/events"} {
		r := e.do(t, http.MethodGet, p, "")
		if r.status != 401 || !strings.HasPrefix(r.header.Get("WWW-Authenticate"), "Basic") {
			t.Errorf("%s: %d", p, r.status)
		}
	}
	for _, p := range []string{"/track.html", "/styles.css", "/api/health", "/api/orders/track/EV-NOPE-NOPE"} {
		if r := e.do(t, http.MethodGet, p, ""); r.status == 401 {
			t.Errorf("%s must be public", p)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/orders", nil)
	req.SetBasicAuth("operacao", "senha-muito-segura")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("authorized: %v %v", res.StatusCode, err)
	}
	_ = res.Body.Close()
	req.SetBasicAuth("operacao", "errada")
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != 401 {
		t.Fatalf("wrong password: %d", res.StatusCode)
	}
	_ = res.Body.Close()
}

func TestTrackRateLimit(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.TrackRatePerMin = 2 })
	for i := range 3 {
		r := e.do(t, http.MethodGet, "/api/orders/track/EV-NOPE-NOPE", "")
		if i < 2 && r.status != 404 {
			t.Fatalf("request %d: %d", i, r.status)
		}
		if i == 2 && (r.status != 429 || r.header.Get("Retry-After") == "") {
			t.Fatalf("expected 429, got %d", r.status)
		}
	}
	// Panel routes are not rate limited.
	if r := e.do(t, http.MethodGet, "/api/orders", ""); r.status != 200 {
		t.Fatalf("panel: %d", r.status)
	}
}

func TestRateLimiterWindow(t *testing.T) {
	now := time.Unix(0, 0)
	l := newRateLimiter(1, time.Minute, func() time.Time { return now })
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("first")
	}
	if ok, wait := l.allow("a"); ok || wait != time.Minute {
		t.Fatalf("second: %v %v", ok, wait)
	}
	if ok, _ := l.allow("b"); !ok {
		t.Fatal("other key")
	}
	now = now.Add(time.Minute)
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("new window")
	}
	if len(l.hits) != 1 {
		t.Fatalf("stale entries not swept: %d", len(l.hits))
	}
}

// sseClient reads SSE frames from a live stream.
type sseClient struct {
	cancel context.CancelFunc
	frames chan map[string]string
	res    *http.Response
}

func openStream(t *testing.T, url string, hdr ...string) *sseClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	c := &sseClient{cancel: cancel, frames: make(chan map[string]string, 64), res: res}
	go func() {
		defer close(c.frames)
		defer func() { _ = res.Body.Close() }()
		sc := bufio.NewScanner(res.Body)
		frame := map[string]string{}
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				if len(frame) > 0 {
					c.frames <- frame
				}
				frame = map[string]string{}
				continue
			}
			if strings.HasPrefix(line, ":") {
				frame["comment"] = line
				continue
			}
			k, v, _ := strings.Cut(line, ":")
			frame[k] = strings.TrimPrefix(v, " ")
		}
	}()
	t.Cleanup(cancel)
	return c
}

// next returns the next frame with an event field, skipping heartbeats/id-only frames.
func (c *sseClient) next(t *testing.T) map[string]string {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case f, ok := <-c.frames:
			if !ok {
				t.Fatal("stream closed")
			}
			if f["event"] != "" {
				return f
			}
		case <-timeout:
			t.Fatal("timeout waiting for SSE frame")
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPanelStreamReceivesChangesAndDisconnectsCleanly(t *testing.T) {
	e := newEnv(t)
	c := openStream(t, e.srv.URL+"/api/events")
	if ct := c.res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type %q", ct)
	}
	first := c.next(t)
	if first["event"] != "connected" || first["id"] == "" {
		t.Fatalf("first frame %v", first)
	}
	created := e.create(t)
	ev := c.next(t)
	var got map[string]any
	_ = json.Unmarshal([]byte(ev["data"]), &got)
	if ev["event"] != "order" || got["code"] != created["code"] || got["customer"] != "Guest Person" || ev["id"] == "" {
		t.Fatalf("order frame %v", ev)
	}
	// Heartbeat comments keep proxies from closing idle streams.
	waitFor(t, func() bool {
		select {
		case f := <-c.frames:
			return f["comment"] != ""
		default:
			return false
		}
	})
	if e.hub.Len() != 1 {
		t.Fatalf("subscribers %d", e.hub.Len())
	}
	c.cancel()
	waitFor(t, func() bool { return e.hub.Len() == 0 })
}

func TestStreamReplaysAfterReconnect(t *testing.T) {
	e := newEnv(t)
	c := openStream(t, e.srv.URL+"/api/events")
	lastID := c.next(t)["id"]
	c.cancel()
	waitFor(t, func() bool { return e.hub.Len() == 0 })

	missed := e.create(t) // published while disconnected

	c2 := openStream(t, e.srv.URL+"/api/events", "Last-Event-ID", lastID)
	if f := c2.next(t); f["event"] != "connected" {
		t.Fatalf("%v", f)
	}
	f := c2.next(t)
	if f["event"] != "order" || !strings.Contains(f["data"], missed["code"].(string)) {
		t.Fatalf("missed event not replayed: %v", f)
	}

	c3 := openStream(t, e.srv.URL+"/api/events", "Last-Event-ID", "foreign-1")
	c3.next(t)
	if f := c3.next(t); f["event"] != "resync" {
		t.Fatalf("expected resync, got %v", f)
	}
}

func TestTrackStreamIsFilteredAndPrivate(t *testing.T) {
	e := newEnv(t)
	mine := e.create(t)
	other := e.create(t)
	c := openStream(t, e.srv.URL+"/api/orders/track/"+strings.ToLower(mine["code"].(string))+"/events")
	c.next(t) // connected

	e.do(t, http.MethodPatch, "/api/orders/"+itoa(int(other["id"].(float64)))+"/status", `{"status":"preparando"}`)
	e.do(t, http.MethodPatch, "/api/orders/"+itoa(int(mine["id"].(float64)))+"/status", `{"status":"preparando"}`)

	f := c.next(t)
	var got map[string]any
	_ = json.Unmarshal([]byte(f["data"]), &got)
	if f["event"] != "order" || got["code"] != mine["code"] || got["status"] != "preparando" {
		t.Fatalf("unexpected frame %v", f)
	}
	for _, private := range []string{"customer", "address", "courier", "id", "totalCents"} {
		if _, ok := got[private]; ok {
			t.Errorf("public stream exposes %q", private)
		}
	}
	if r := e.do(t, http.MethodGet, "/api/orders/track/EV-NOPE-NOPE/events", ""); r.status != 404 {
		t.Fatalf("unknown code stream: %d", r.status)
	}
}

func TestStreamLimitAndShutdown(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.SSEMaxClients = 1 })
	c := openStream(t, e.srv.URL+"/api/events")
	c.next(t)
	if r := e.do(t, http.MethodGet, "/api/events", ""); r.status != 503 {
		t.Fatalf("over limit: %d", r.status)
	}
	e.hub.Close() // what http.Server.RegisterOnShutdown triggers
	timeout := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-c.frames:
			if !ok {
				return // stream ended
			}
		case <-timeout:
			t.Fatal("stream not closed on shutdown")
		}
	}
}

func TestStreamSurvivesIdleLongerThanWriteTimeout(t *testing.T) {
	old := sseWriteTimeout
	sseWriteTimeout = 50 * time.Millisecond
	t.Cleanup(func() { sseWriteTimeout = old })
	e := newEnv(t, func(c *config.Config) { c.SSEHeartbeat = 300 * time.Millisecond })
	c := openStream(t, e.srv.URL+"/api/events")
	c.next(t)
	time.Sleep(700 * time.Millisecond) // idle well past the per-write deadline
	created := e.create(t)
	if f := c.next(t); f["event"] != "order" || !strings.Contains(f["data"], created["code"].(string)) {
		t.Fatalf("stream broke after idle period: %v", f)
	}
}
