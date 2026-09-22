// Package ownerwindow is the browser transport for the owner view contract.
// It is mounted on the existing loopback listener, not a separate service.
package ownerwindow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/ownerview"
)

const Prefix = "/__ownward/owner/"
const bootstrapTTL = time.Minute
const sessionTTL = 12 * time.Hour
const maxSessions = 32

type session struct {
	credential string
	expires    time.Time
	poll, save time.Time
	used       uint64
}
type Archives struct {
	Backup  func(context.Context) (string, error)
	Restore func(context.Context, io.Reader) (string, error)
}
type Server struct {
	View       *ownerview.Service
	Archives   Archives
	mu         sync.Mutex
	origin     string
	enabled    bool
	bootstraps map[string]session
	sessions   map[string]session
	activity   uint64
	admission  chan struct{}
}

func New(view *ownerview.Service, archives Archives) *Server {
	return &Server{View: view, Archives: archives, bootstraps: map[string]session{}, sessions: map[string]session{}, admission: make(chan struct{}, 4)}
}
func token() (string, error) {
	var b [32]byte
	_, e := rand.Read(b[:])
	return hex.EncodeToString(b[:]), e
}

func (s *Server) Mount(origin string) http.Handler {
	s.mu.Lock()
	s.origin = origin
	s.mu.Unlock()
	return s
}

// Bootstrap is reachable only through the existing authenticated machine
// control route. A service-start token by itself cannot mint an owner session.
func (s *Server) Bootstrap(ctx context.Context, credential string) (string, error) {
	if _, e := s.View.Control.Owner(informationcontrol.Authenticate(ctx, credential)); e != nil {
		return "", e
	}
	id, e := token()
	if e != nil {
		return "", e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(time.Now())
	if s.origin == "" {
		return "", errors.New("物主入口暂不可用，请稍后再试")
	}
	s.enabled = true
	s.putBounded(s.bootstraps, id, session{credential: credential, expires: time.Now().Add(bootstrapTTL)})
	return s.origin + Prefix + "#" + id, nil
}
func (s *Server) prune(now time.Time) {
	for k, v := range s.bootstraps {
		if !now.Before(v.expires) {
			delete(s.bootstraps, k)
		}
	}
	for k, v := range s.sessions {
		if !now.Before(v.expires) {
			delete(s.sessions, k)
		}
	}
}

// Called under mu, after owner authentication. New verified entry must remain
// possible even when clients have lost their old session/bootstrap tokens.
func (s *Server) putBounded(entries map[string]session, id string, value session) {
	if len(entries) >= maxSessions {
		oldest := ""
		for key, entry := range entries {
			if oldest == "" || entry.used < entries[oldest].used {
				oldest = key
			}
		}
		delete(entries, oldest)
	}
	s.activity++
	value.used = s.activity
	entries[id] = value
}

func (s *Server) touch(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(time.Now())
	v, ok := s.sessions[key]
	if ok {
		s.activity++
		v.used = s.activity
		s.sessions[key] = v
	}
	return ok
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	s.mu.Lock()
	origin, enabled := s.origin, s.enabled
	s.mu.Unlock()
	u, e := url.Parse(origin)
	if e != nil || u.Host == "" || r.Host != u.Host || !enabled {
		http.Error(w, "入口尚未验证", http.StatusForbidden)
		return
	}
	if r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin {
		http.Error(w, "拒绝跨来源请求", http.StatusForbidden)
		return
	}
	fetch := r.Header.Get("Sec-Fetch-Site")
	if fetch != "" && fetch != "same-origin" && fetch != "none" {
		http.Error(w, "拒绝跨来源请求", http.StatusForbidden)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, Prefix)
	if path == "" || path == "bootstrap.js" {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}
		if path == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(landing)
		} else {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(bootstrapScript)
		}
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	// Requiring a custom header + JSON prevents form, image, and no-cors fake
	// pages from exercising a loopback authority, including the bootstrap.
	if r.Header.Get("Origin") != origin || r.Header.Get("X-Ownward-View") != contract.OwnerViewSchema {
		http.Error(w, "来源验证失败", 403)
		return
	}
	if path != "v1/restore" && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "须使用 JSON", 415)
		return
	}
	select {
	case s.admission <- struct{}{}:
		defer func() { <-s.admission }()
	default:
		http.Error(w, "请求已满，请稍后继续", 429)
		return
	}
	if path == "v1/bootstrap" {
		s.exchange(w, r)
		return
	}
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	s.prune(time.Now())
	current, ok := s.sessions[key]
	s.mu.Unlock()
	if !ok || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		http.Error(w, "请从本机物主入口重新验证", 401)
		return
	}
	ctx := informationcontrol.Authenticate(r.Context(), current.credential)
	if _, e := s.View.Control.Owner(ctx); e != nil {
		s.mu.Lock()
		delete(s.sessions, key)
		s.mu.Unlock()
		http.Error(w, "物主会话已失效", 401)
		return
	}
	// All authenticated routes count as activity, independently of polling or
	// save pacing. A concurrent logout/expiry must not resurrect the session.
	if !s.touch(key) {
		http.Error(w, "请从本机物主入口重新验证", 401)
		return
	}
	var value any
	switch path {
	case "v1/query":
		var in contract.OwnerQuery
		e = decode(w, r, &in)
		if e == nil && in.View == "changes" {
			e = s.pace(key, false)
		}
		if e == nil {
			value, e = s.View.Query(ctx, in)
		}
	case "v1/action":
		var in contract.OwnerAction
		e = decode(w, r, &in)
		if e == nil && (in.Action == "replace_draft" || in.Action == "append_draft") {
			e = s.pace(key, true)
		}
		if e == nil {
			value, e = s.View.Act(ctx, in)
		}
	case "v1/logout":
		s.mu.Lock()
		delete(s.sessions, key)
		s.mu.Unlock()
		value = map[string]bool{"closed": true}
	case "v1/backup":
		s.backup(ctx, w)
		return
	case "v1/restore":
		if s.Archives.Restore == nil {
			e = errors.New("恢复入口不可用")
		} else {
			var target string
			target, e = s.Archives.Restore(ctx, r.Body)
			value = map[string]string{"state": "verification_required", "data_dir": target}
		}
	default:
		http.NotFound(w, r)
		return
	}
	if e != nil {
		writeError(w, e)
		return
	}
	if _, e = s.View.Control.Owner(ctx); e != nil {
		http.Error(w, "物主会话已失效", 401)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func decode(w http.ResponseWriter, r *http.Request, value any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, contract.OwnerRequestBytes))
	d.DisallowUnknownFields()
	if e := d.Decode(value); e != nil {
		return e
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return errors.New("请求含多余内容")
	}
	return nil
}
func writeError(w http.ResponseWriter, e error) {
	status := 400
	if errors.Is(e, contract.ErrOwnerRefresh) {
		status = 409
	}
	if errors.Is(e, informationcontrol.ErrDenied) {
		status = 403
	}
	if errors.Is(e, errPaced) {
		status = 429
		w.Header().Set("Retry-After", "1")
	}
	http.Error(w, e.Error(), status)
}

var errPaced = errors.New("输入已保留，请按保存或刷新节奏重试")

func (s *Server) pace(key string, save bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[key]
	if !ok {
		return informationcontrol.ErrDenied
	}
	now := time.Now()
	if save {
		if now.Sub(v.save) < contract.OwnerAutosaveMin {
			return errPaced
		}
		v.save = now
	} else {
		if now.Sub(v.poll) < contract.OwnerPollMin {
			return errPaced
		}
		v.poll = now
	}
	s.sessions[key] = v
	return nil
}
func (s *Server) exchange(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if e := decode(w, r, &in); e != nil {
		writeError(w, e)
		return
	}
	id, e := token()
	if e != nil {
		writeError(w, e)
		return
	}
	s.mu.Lock()
	s.prune(time.Now())
	v, ok := s.bootstraps[in.Token]
	if ok {
		delete(s.bootstraps, in.Token)
	}
	s.mu.Unlock()
	if !ok {
		http.Error(w, "引导令牌已失效，请重新打开", 401)
		return
	}
	if _, e = s.View.Control.Owner(informationcontrol.Authenticate(r.Context(), v.credential)); e != nil {
		http.Error(w, "物主验证失效", 401)
		return
	}
	v.expires = time.Now().Add(sessionTTL)
	s.mu.Lock()
	s.prune(time.Now())
	s.putBounded(s.sessions, id, v)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"session": id, "expires": v.expires, "schema": contract.OwnerViewSchema, "poll_seconds": 2, "poll_min_seconds": 1, "poll_max_seconds": 30, "autosave_min_seconds": 1, "page_limit": contract.OwnerPageLimit, "text_bytes": contract.OwnerTextBytes, "history_days": contract.OwnerHistoryDays, "history_items": contract.OwnerHistoryItems})
}

func (s *Server) backup(ctx context.Context, w http.ResponseWriter) {
	if s.Archives.Backup == nil {
		http.Error(w, "备份入口不可用", 503)
		return
	}
	cp, e := s.View.Store.OwnerCheckpoint(ctx)
	if e != nil {
		writeError(w, e)
		return
	}
	path, e := s.Archives.Backup(ctx)
	if e != nil {
		writeError(w, e)
		return
	}
	defer os.Remove(path)
	f, e := os.Open(path)
	if e != nil {
		writeError(w, e)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="ownward-backup.zip"`)
	buf := make([]byte, 64<<10)
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			now, e := s.View.Store.OwnerCheckpoint(ctx)
			if e != nil || now.Deletion != cp.Deletion {
				panic(http.ErrAbortHandler)
			}
			if _, e = w.Write(buf[:n]); e != nil {
				return
			}
		}
		if readErr == io.EOF {
			return
		}
		if readErr != nil {
			panic(http.ErrAbortHandler)
		}
	}
}
