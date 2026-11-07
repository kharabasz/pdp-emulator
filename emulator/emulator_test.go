package emulator

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type client struct {
	t   *testing.T
	url string
}

func start(t *testing.T, cfg Config) (*Emulator, client) {
	t.Helper()
	e := New(cfg)
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return e, client{t, srv.URL}
}

func (c client) do(method, path string, body any, header ...string) (int, []byte) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		if s, ok := body.(string); ok {
			rd = bytes.NewBufferString(s)
		} else {
			b, _ := json.Marshal(body)
			rd = bytes.NewBuffer(b)
		}
	}
	req, _ := http.NewRequest(method, c.url+path, rd)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (c client) allow(path string, check any) bool {
	c.t.Helper()
	code, b := c.do("POST", path, check)
	if code != 200 {
		c.t.Fatalf("POST %s: %d %s", path, code, b)
	}
	var r struct {
		Allow bool `json:"allow"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		c.t.Fatal(err)
	}
	return r.Allow
}

func chk(user, action, rtype, key, tenant string) map[string]any {
	return map[string]any{"user": map[string]string{"key": user}, "action": action,
		"resource": map[string]string{"type": rtype, "key": key, "tenant": tenant}}
}

func TestDefaultAllowAndDeny(t *testing.T) {
	e, c := start(t, Config{DefaultAllow: true})
	if !c.allow("/allowed", chk("u", "read", "doc", "1", "t")) {
		t.Fatal("default allow should allow")
	}
	e.SetDefault(false)
	if c.allow("/allowed", chk("u", "read", "doc", "1", "t")) {
		t.Fatal("default deny should deny")
	}
}

func TestQueuedAnswersInOrderThenDefault(t *testing.T) {
	_, c := start(t, Config{DefaultAllow: true})
	if code, b := c.do("POST", "/_emulator/answers", map[string]any{"answers": []bool{false, true, false}}); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	want := []bool{false, true, false, true}
	for i, w := range want {
		if got := c.allow("/allowed", chk("u", "read", "doc", "1", "")); got != w {
			t.Fatalf("check %d: got %t want %t", i, got, w)
		}
	}
}

func TestMatchScopesAnswers(t *testing.T) {
	_, c := start(t, Config{DefaultAllow: true})
	c.do("POST", "/_emulator/answers", map[string]any{"match": map[string]string{"user": "alice"}, "answers": []bool{false}})
	if !c.allow("/allowed", chk("bob", "read", "doc", "1", "")) {
		t.Fatal("bob's check must not use alice's answer")
	}
	if c.allow("/allowed", chk("alice", "read", "doc", "1", "")) {
		t.Fatal("alice's check should get the queued deny")
	}
}

func TestStandingRuleAndTimes(t *testing.T) {
	_, c := start(t, Config{DefaultAllow: true})
	code, b := c.do("POST", "/_emulator/rules", []map[string]any{
		{"match": map[string]string{"action": "delete"}, "allow": false},
		{"match": map[string]string{"resource_type": "doc", "tenant": "t1"}, "allow": false, "times": 2},
	})
	if code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	for i := 0; i < 3; i++ {
		if c.allow("/allowed", chk("u", "delete", "doc", "1", "")) {
			t.Fatal("standing deny rule should keep denying")
		}
	}
	got := []bool{}
	for i := 0; i < 3; i++ {
		got = append(got, c.allow("/allowed", chk("u", "read", "doc", "1", "t1")))
	}
	if got[0] || got[1] || !got[2] {
		t.Fatalf("times=2 rule: got %v", got)
	}
}

func TestStringForms(t *testing.T) {
	e, c := start(t, Config{DefaultAllow: true})
	c.allow("/allowed", map[string]any{"user": "u1", "action": "read", "resource": "doc:42", "tenant": "default"})
	cs := e.Checks(Match{})
	if len(cs) != 1 || cs[0].User != "u1" || cs[0].ResourceType != "doc" || cs[0].ResourceKey != "42" || cs[0].Tenant != "default" {
		t.Fatalf("parsed %+v", cs)
	}
}

func TestPrefixedPaths(t *testing.T) {
	_, c := start(t, Config{DefaultAllow: false})
	for _, p := range []string{"/healthy", "/pdp/healthy", "/ready", "/"} {
		if code, _ := c.do("GET", p, nil); code != 200 {
			t.Fatalf("GET %s: %d", p, code)
		}
	}
	c.do("POST", "/pdp/_emulator/answers", map[string]any{"answers": []bool{true}})
	if !c.allow("/pdp/allowed", chk("u", "read", "doc", "1", "")) {
		t.Fatal("prefixed control and check routes should work")
	}
	if c.allow("/pdp/allowed/", chk("u", "read", "doc", "1", "")) {
		t.Fatal("trailing slash should reach /allowed and get the default deny")
	}
}

func TestRecordedChecksAndFilters(t *testing.T) {
	_, c := start(t, Config{DefaultAllow: true})
	c.do("POST", "/allowed", chk("alice", "read", "doc", "1", ""), "Authorization", "Bearer k")
	c.do("POST", "/allowed", chk("bob", "read", "doc", "1", ""))
	code, b := c.do("GET", "/_emulator/checks?user=alice", nil)
	var cs []Check
	if err := json.Unmarshal(b, &cs); code != 200 || err != nil || len(cs) != 1 {
		t.Fatalf("%d %s", code, b)
	}
	if cs[0].Authorization != "Bearer k" || cs[0].Path != "/allowed" || cs[0].DecidedBy != "default" {
		t.Fatalf("recorded %+v", cs[0])
	}
	c.do("DELETE", "/_emulator/checks", nil)
	_, b = c.do("GET", "/_emulator/checks", nil)
	if string(bytes.TrimSpace(b)) != "[]" {
		t.Fatalf("after clear: %s", b)
	}
}

func TestBulk(t *testing.T) {
	_, c := start(t, Config{DefaultAllow: true})
	c.do("POST", "/_emulator/answers", map[string]any{"answers": []bool{false}})
	code, b := c.do("POST", "/allowed/bulk", []any{chk("u", "read", "doc", "1", ""), chk("u", "read", "doc", "2", "")})
	var r struct {
		Allow []struct {
			Allow bool `json:"allow"`
		} `json:"allow"`
	}
	if err := json.Unmarshal(b, &r); code != 200 || err != nil || len(r.Allow) != 2 || r.Allow[0].Allow || !r.Allow[1].Allow {
		t.Fatalf("%d %s", code, b)
	}
	// An invalid item rejects the whole request and uses up no rule.
	c.do("POST", "/_emulator/answers", map[string]any{"answers": []bool{false}})
	if code, _ := c.do("POST", "/allowed/bulk", []any{chk("u", "read", "doc", "1", ""), map[string]string{"action": "x"}}); code != 422 {
		t.Fatalf("bad bulk: %d", code)
	}
	if c.allow("/allowed", chk("u", "read", "doc", "1", "")) {
		t.Fatal("queued deny should still be there after the rejected bulk")
	}
}

func TestRequireAPIKey(t *testing.T) {
	_, c := start(t, Config{DefaultAllow: true, RequireKey: true, APIKey: "secret"})
	if code, _ := c.do("POST", "/allowed", chk("u", "read", "doc", "1", "")); code != 401 {
		t.Fatalf("no key: %d", code)
	}
	if code, _ := c.do("POST", "/allowed", chk("u", "read", "doc", "1", ""), "Authorization", "Bearer secret"); code != 200 {
		t.Fatalf("right key: %d", code)
	}
}

func TestBadRequests(t *testing.T) {
	_, c := start(t, Config{})
	cases := []struct {
		method, path string
		body         any
		want         int
	}{
		{"POST", "/allowed", "not json", 400},
		{"POST", "/allowed", map[string]string{"action": "read"}, 422},
		{"GET", "/allowed", nil, 405},
		{"POST", "/_emulator/answers", map[string]any{"answers": []bool{}}, 400},
		{"PUT", "/_emulator/default", map[string]string{}, 400},
		{"GET", "/_emulator/nope", nil, 404},
		{"GET", "/nope", nil, 404},
	}
	for _, tc := range cases {
		if code, b := c.do(tc.method, tc.path, tc.body); code != tc.want {
			t.Errorf("%s %s: got %d want %d (%s)", tc.method, tc.path, code, tc.want, b)
		}
	}
}

func TestResetRestoresStartupRules(t *testing.T) {
	e, c := start(t, Config{DefaultAllow: true, Rules: []Rule{{Match: Match{User: "blocked"}, Allow: false}}})
	c.do("PUT", "/_emulator/default", map[string]bool{"allow": false})
	c.do("DELETE", "/_emulator/rules", nil)
	c.do("POST", "/allowed", chk("x", "read", "doc", "1", ""))
	if code, _ := c.do("POST", "/_emulator/reset", nil); code != 204 {
		t.Fatal("reset")
	}
	if len(e.Checks(Match{})) != 0 || len(e.Rules()) != 1 {
		t.Fatal("reset should clear checks and restore start-up rules")
	}
	if c.allow("/allowed", chk("blocked", "read", "doc", "1", "")) || !c.allow("/allowed", chk("ok", "read", "doc", "1", "")) {
		t.Fatal("start-up rule and default should apply after reset")
	}
}

func TestConcurrentQueuedAnswersUsedOnce(t *testing.T) {
	e, c := start(t, Config{DefaultAllow: true})
	answers := make([]bool, 50)
	e.Enqueue(Match{}, answers...) // 50 denies
	var wg sync.WaitGroup
	var mu sync.Mutex
	denied := 0
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !c.allow("/allowed", chk("u", "read", "doc", "1", "")) {
				mu.Lock()
				denied++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if denied != 50 {
		t.Fatalf("each queued answer should be used exactly once: %d denies", denied)
	}
}
