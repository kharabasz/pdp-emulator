// Package emulator is a stand-in for the Permit.io Policy Decision Point (PDP) whose decisions tests control.
//
// It serves the PDP's check API (POST /allowed, POST /allowed/bulk) and health checks, and a control API under
// /_emulator that tests use to script decisions and read back the checks the PDP received. Every route also works
// behind a path prefix (an ALB forwarding /pdp/* serves /pdp/allowed), because routes match on their path suffix.
package emulator

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Match selects checks by the fields of the check request. An empty field matches anything.
type Match struct {
	User         string `json:"user,omitempty"`
	Action       string `json:"action,omitempty"`
	ResourceType string `json:"resource_type,omitempty"`
	ResourceKey  string `json:"resource_key,omitempty"`
	Tenant       string `json:"tenant,omitempty"`
}

// Rule answers the checks it matches. Times is how many checks it answers before it is used up; 0 means no limit.
type Rule struct {
	ID        int   `json:"id"`
	Match     Match `json:"match"`
	Allow     bool  `json:"allow"`
	Times     int   `json:"times,omitempty"`
	Remaining int   `json:"remaining,omitempty"`
}

// Check is one check the PDP received, in normalised form, with the decision it got.
type Check struct {
	Time          time.Time       `json:"time"`
	Path          string          `json:"path"`
	Authorization string          `json:"authorization,omitempty"`
	User          string          `json:"user"`
	Action        string          `json:"action"`
	ResourceType  string          `json:"resource_type"`
	ResourceKey   string          `json:"resource_key,omitempty"`
	Tenant        string          `json:"tenant,omitempty"`
	Body          json.RawMessage `json:"body"`
	Allow         bool            `json:"allow"`
	DecidedBy     string          `json:"decided_by"`
}

// Config is the emulator's start-up configuration.
type Config struct {
	DefaultAllow bool   // decision when no rule matches
	APIKey       string // when RequireKey is set, checks must carry "Authorization: Bearer <APIKey>"
	RequireKey   bool
	Rules        []Rule // loaded at start and again on reset
	Logger       *log.Logger
}

// Emulator holds the rules and the checks received. It is safe for concurrent use.
type Emulator struct {
	cfg Config

	mu           sync.Mutex
	defaultAllow bool
	rules        []*Rule
	nextID       int
	checks       []Check
}

// New returns an emulator in its start-up state.
func New(cfg Config) *Emulator {
	if cfg.Logger == nil {
		cfg.Logger = log.New(io.Discard, "", 0)
	}
	e := &Emulator{cfg: cfg}
	e.Reset()
	return e
}

// Reset drops every rule and recorded check, then restores the start-up default and rules.
func (e *Emulator) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.defaultAllow = e.cfg.DefaultAllow
	e.rules = nil
	e.checks = nil
	for _, r := range e.cfg.Rules {
		e.addLocked(r)
	}
}

func (e *Emulator) addLocked(r Rule) Rule {
	e.nextID++
	r.ID = e.nextID
	r.Remaining = r.Times
	e.rules = append(e.rules, &r)
	return r
}

// AddRules appends rules, which are tried in the order they were added.
func (e *Emulator) AddRules(rs ...Rule) []Rule {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Rule, 0, len(rs))
	for _, r := range rs {
		if r.Times < 0 {
			r.Times = 0
		}
		out = append(out, e.addLocked(r))
	}
	return out
}

// Enqueue adds one single-use rule per answer, so matching checks get the answers in order.
func (e *Emulator) Enqueue(m Match, answers ...bool) []Rule {
	rs := make([]Rule, len(answers))
	for i, a := range answers {
		rs[i] = Rule{Match: m, Allow: a, Times: 1}
	}
	return e.AddRules(rs...)
}

// Rules returns the rules that can still answer.
func (e *Emulator) Rules() []Rule {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Rule, 0, len(e.rules))
	for _, r := range e.rules {
		out = append(out, *r)
	}
	return out
}

// ClearRules drops every rule.
func (e *Emulator) ClearRules() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = nil
}

// SetDefault sets the decision for checks no rule matches.
func (e *Emulator) SetDefault(allow bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.defaultAllow = allow
}

// Checks returns the recorded checks that match m.
func (e *Emulator) Checks(m Match) []Check {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []Check{}
	for _, c := range e.checks {
		if m.matches(c) {
			out = append(out, c)
		}
	}
	return out
}

// ClearChecks drops the recorded checks.
func (e *Emulator) ClearChecks() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checks = nil
}

func (m Match) matches(c Check) bool {
	return (m.User == "" || m.User == c.User) &&
		(m.Action == "" || m.Action == c.Action) &&
		(m.ResourceType == "" || m.ResourceType == c.ResourceType) &&
		(m.ResourceKey == "" || m.ResourceKey == c.ResourceKey) &&
		(m.Tenant == "" || m.Tenant == c.Tenant)
}

// decide answers c from the first matching rule that is not used up, else the default, and records it.
func (e *Emulator) decide(c Check) Check {
	e.mu.Lock()
	defer e.mu.Unlock()
	c.Allow, c.DecidedBy = e.defaultAllow, "default"
	for i, r := range e.rules {
		if !r.Match.matches(c) {
			continue
		}
		c.Allow, c.DecidedBy = r.Allow, fmt.Sprintf("rule:%d", r.ID)
		if r.Times > 0 {
			r.Remaining--
			if r.Remaining == 0 {
				e.rules = append(e.rules[:i], e.rules[i+1:]...)
			}
		}
		break
	}
	e.checks = append(e.checks, c)
	return c
}

// parseCheck reads a check request body. The PDP takes the user and the resource either as objects or as strings
// ("user-key", "type:key").
func parseCheck(raw json.RawMessage) (Check, error) {
	var req struct {
		User     json.RawMessage `json:"user"`
		Action   string          `json:"action"`
		Resource json.RawMessage `json:"resource"`
		Tenant   string          `json:"tenant"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return Check{}, fmt.Errorf("body is not a check: %w", err)
	}
	c := Check{Action: req.Action, Body: raw, Tenant: req.Tenant}

	var s string
	if json.Unmarshal(req.User, &s) == nil {
		c.User = s
	} else {
		var u struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(req.User, &u); err != nil {
			return Check{}, fmt.Errorf("user must be a string or an object with a key")
		}
		c.User = u.Key
	}

	if json.Unmarshal(req.Resource, &s) == nil {
		c.ResourceType, c.ResourceKey, _ = strings.Cut(s, ":")
	} else {
		var r struct {
			Type   string `json:"type"`
			Key    string `json:"key"`
			Tenant string `json:"tenant"`
		}
		if err := json.Unmarshal(req.Resource, &r); err != nil {
			return Check{}, fmt.Errorf("resource must be a string or an object with a type")
		}
		c.ResourceType, c.ResourceKey = r.Type, r.Key
		if r.Tenant != "" {
			c.Tenant = r.Tenant
		}
	}
	if c.User == "" || c.Action == "" || c.ResourceType == "" {
		return Check{}, fmt.Errorf("a check needs user, action and resource type")
	}
	return c, nil
}

// ServeHTTP routes on the path suffix, so the emulator works under any prefix.
func (e *Emulator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimSuffix(r.URL.Path, "/")
	if i := strings.Index(p, "/_emulator/"); i >= 0 {
		e.control(w, r, p[i+len("/_emulator/"):])
		return
	}
	switch {
	case strings.HasSuffix(p, "/allowed/bulk"):
		e.bulk(w, r)
	case strings.HasSuffix(p, "/allowed"):
		e.allowed(w, r)
	case strings.HasSuffix(p, "/healthy"), strings.HasSuffix(p, "/ready"), strings.HasSuffix(p, "/health"), p == "":
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "Not Found"})
	}
}

func (e *Emulator) authorized(w http.ResponseWriter, r *http.Request) bool {
	if !e.cfg.RequireKey || r.Header.Get("Authorization") == "Bearer "+e.cfg.APIKey {
		return true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]string{"detail": "invalid or missing API key"})
	return false
}

func (e *Emulator) check(r *http.Request, raw json.RawMessage) (Check, error) {
	c, err := parseCheck(raw)
	if err != nil {
		return c, err
	}
	c.Time, c.Path, c.Authorization = time.Now().UTC(), r.URL.Path, r.Header.Get("Authorization")
	c = e.decide(c)
	e.cfg.Logger.Printf("check user=%s action=%s resource=%s:%s tenant=%s allow=%t by=%s",
		c.User, c.Action, c.ResourceType, c.ResourceKey, c.Tenant, c.Allow, c.DecidedBy)
	return c, nil
}

func result(c Check) map[string]any {
	return map[string]any{"allow": c.Allow, "query": c.Body, "debug": map[string]string{"pdp_emulator": c.DecidedBy}}
}

func (e *Emulator) allowed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"detail": "Method Not Allowed"})
		return
	}
	if !e.authorized(w, r) {
		return
	}
	raw, err := readBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": err.Error()})
		return
	}
	c, err := e.check(r, raw)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result(c))
}

func (e *Emulator) bulk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"detail": "Method Not Allowed"})
		return
	}
	if !e.authorized(w, r) {
		return
	}
	raw, err := readBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": err.Error()})
		return
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		var wrapped struct {
			Checks []json.RawMessage `json:"checks"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil || wrapped.Checks == nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": "body must be a list of checks"})
			return
		}
		items = wrapped.Checks
	}
	// Parse every check before deciding any, so a bad item uses up no rule.
	for _, it := range items {
		if _, err := parseCheck(it); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": err.Error()})
			return
		}
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		c, _ := e.check(r, it)
		out = append(out, result(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"allow": out})
}

// control serves the /_emulator API.
func (e *Emulator) control(w http.ResponseWriter, r *http.Request, route string) {
	switch {
	case route == "rules" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, e.Rules())
	case route == "rules" && r.Method == http.MethodPost:
		raw, err := readBody(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"detail": err.Error()})
			return
		}
		var rs []Rule
		if json.Unmarshal(raw, &rs) != nil {
			var one Rule
			if err := json.Unmarshal(raw, &one); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "body must be a rule or a list of rules"})
				return
			}
			rs = []Rule{one}
		}
		writeJSON(w, http.StatusCreated, e.AddRules(rs...))
	case route == "rules" && r.Method == http.MethodDelete:
		e.ClearRules()
		w.WriteHeader(http.StatusNoContent)
	case route == "answers" && r.Method == http.MethodPost:
		var body struct {
			Match   Match  `json:"match"`
			Answers []bool `json:"answers"`
		}
		raw, err := readBody(r)
		if err == nil {
			err = json.Unmarshal(raw, &body)
		}
		if err != nil || len(body.Answers) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"detail": `body must be {"answers": [true, false, ...], "match": {...}}`})
			return
		}
		writeJSON(w, http.StatusCreated, e.Enqueue(body.Match, body.Answers...))
	case route == "default" && r.Method == http.MethodPut:
		var body struct {
			Allow *bool `json:"allow"`
		}
		raw, err := readBody(r)
		if err == nil {
			err = json.Unmarshal(raw, &body)
		}
		if err != nil || body.Allow == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"detail": `body must be {"allow": true|false}`})
			return
		}
		e.SetDefault(*body.Allow)
		w.WriteHeader(http.StatusNoContent)
	case route == "checks" && r.Method == http.MethodGet:
		q := r.URL.Query()
		writeJSON(w, http.StatusOK, e.Checks(Match{
			User: q.Get("user"), Action: q.Get("action"), ResourceType: q.Get("resource_type"),
			ResourceKey: q.Get("resource_key"), Tenant: q.Get("tenant"),
		}))
	case route == "checks" && r.Method == http.MethodDelete:
		e.ClearChecks()
		w.WriteHeader(http.StatusNoContent)
	case route == "reset" && r.Method == http.MethodPost:
		e.Reset()
		w.WriteHeader(http.StatusNoContent)
	case route == "state" && r.Method == http.MethodGet:
		e.mu.Lock()
		state := map[string]any{"default_allow": e.defaultAllow, "rules": len(e.rules), "checks": len(e.checks)}
		e.mu.Unlock()
		writeJSON(w, http.StatusOK, state)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"detail": "unknown control route " + r.Method + " /_emulator/" + route})
	}
}

const maxBody = 1 << 20

func readBody(r *http.Request) (json.RawMessage, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBody {
		return nil, fmt.Errorf("body larger than %d bytes", maxBody)
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("body is not JSON")
	}
	return b, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
