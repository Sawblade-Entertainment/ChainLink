package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fsnotify/fsnotify"
)

const (
	longPollTimeout = 20 * time.Second
	debounceWindow  = 75 * time.Millisecond
	maxQueueLen     = 10000
	configFileName  = "chainlink.json"
	packageSrcDir   = "src"
	pkgPrefix       = "~pkg/" // namespace for package rel paths
)

// ---------- Config ----------

type RootMapping struct {
	Dir    string `json:"dir"`
	Target string `json:"target"`
}

type GlobalPackageMapping struct {
	PackageName string `json:"package"` // disk path under ~/.chainlink/packages
	Target      string `json:"target"`  // e.g. "ReplicatedStorage.packages.razorinput"
}

type ProjectConfig struct {
	Port     int                    `json:"port"`
	Token    string                 `json:"token"`
	Roots    []RootMapping          `json:"roots"`
	Packages []GlobalPackageMapping `json:"packages"`
}

type ChainToml struct {
	Packages map[string]string `toml:"Packages"`
}

func packagesBase() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".chainlink", "packages")
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// findPackageDir tries common on-disk layouts for "scope/name" + version.
func findPackageDir(name, version string) string {
	short := name[strings.LastIndex(name, "/")+1:]
	candidates := []string{
		strings.ReplaceAll(name, "/", "_") + "/" + version,
		name + "/" + version,
		short + "/" + version,
		strings.ReplaceAll(name, "/", "_") + "@" + version,
	}
	for _, c := range candidates {
		if dirExists(filepath.Join(packagesBase(), filepath.FromSlash(c))) {
			return c
		}
	}
	return candidates[0]
}

// withSrc returns "<p>/src" if it exists, otherwise p (copy the root).
func withSrc(p string) string {
	p = strings.Trim(filepath.ToSlash(p), "/")
	if strings.HasSuffix(p, "/"+packageSrcDir) {
		return p
	}
	if dirExists(filepath.Join(packagesBase(), filepath.FromSlash(p), packageSrcDir)) {
		return p + "/" + packageSrcDir
	}
	return p
}

// splitTarget: "ReplicatedStorage.packages.razorinput" -> ("ReplicatedStorage.packages", "razorinput")
func splitTarget(t string) (parent, name string) {
	i := strings.LastIndex(t, ".")
	if i < 0 {
		return "", t
	}
	return t[:i], t[i+1:]
}

func defaultConfig() ProjectConfig {
	return ProjectConfig{
		Port: 8080,
		Roots: []RootMapping{
			{Dir: "client", Target: "StarterPlayer.StarterPlayerScripts"},
			{Dir: "server", Target: "ServerScriptService"},
			{Dir: "shared", Target: "ReplicatedStorage.shared"},
			{Dir: "prefab", Target: "ServerStorage"},
		},
	}
}

func loadConfig() ProjectConfig {
	cfg := defaultConfig()

	if data, err := os.ReadFile(configFileName); err == nil {
		var file ProjectConfig
		if err := json.Unmarshal(data, &file); err != nil {
			log.Printf("Warning: %s is invalid: %v", configFileName, err)
		} else {
			if file.Port != 0 {
				cfg.Port = file.Port
			}
			if file.Token != "" {
				cfg.Token = file.Token
			}
			if len(file.Roots) > 0 {
				cfg.Roots = file.Roots
			}
			cfg.Packages = file.Packages
		}
	}

	var tomlCfg ChainToml
	if _, err := toml.DecodeFile("chain.toml", &tomlCfg); err == nil {
		for _, spec := range tomlCfg.Packages {
			name, version := spec, "latest"
			if i := strings.LastIndex(spec, "@"); i > 0 {
				name, version = spec[:i], spec[i+1:]
			}
			short := name[strings.LastIndex(name, "/")+1:]
			cfg.Packages = append(cfg.Packages, GlobalPackageMapping{
				PackageName: findPackageDir(name, version),
				Target:      "ReplicatedStorage.packages." + short,
			})
		}
	} else if !os.IsNotExist(err) {
		log.Printf("Warning: chain.toml could not be parsed: %v", err)
	}

	// Resolve src/root and dedupe.
	seen := map[string]bool{}
	var pkgs []GlobalPackageMapping
	for _, p := range cfg.Packages {
		p.PackageName = withSrc(p.PackageName)
		if seen[p.PackageName] {
			continue
		}
		seen[p.PackageName] = true
		pkgs = append(pkgs, p)
	}
	cfg.Packages = pkgs
	return cfg
}

// ---------- Events / Queue ----------

type Event struct {
	Seq          uint64   `json:"seq"`
	Op           string   `json:"op"`
	Path         string   `json:"path"`
	Target       string   `json:"target"`
	InstancePath []string `json:"instancePath"`
	ClassName    string   `json:"className,omitempty"`
	Source       string   `json:"source,omitempty"`
	KeepChildren bool     `json:"keepChildren,omitempty"`
}

type ChangesResponse struct {
	SessionID string  `json:"sessionId"`
	Seq       uint64  `json:"seq"`
	Full      bool    `json:"full"`
	Events    []Event `json:"events"`
}

type Queue struct {
	mu        sync.Mutex
	sessionID string
	seq       uint64
	events    []Event
	notify    chan struct{}
}

func NewQueue() *Queue {
	return &Queue{
		sessionID: strconv.FormatInt(time.Now().UnixNano(), 36),
		notify:    make(chan struct{}),
	}
}

func (q *Queue) Push(evs ...Event) {
	if len(evs) == 0 {
		return
	}
	q.mu.Lock()
	for _, ev := range evs {
		q.seq++
		ev.Seq = q.seq
		q.events = append(q.events, ev)
	}
	if len(q.events) > maxQueueLen {
		q.events = q.events[len(q.events)-maxQueueLen:]
	}
	close(q.notify)
	q.notify = make(chan struct{})
	q.mu.Unlock()
}

func (q *Queue) Since(since uint64) (events []Event, latest uint64, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if since > q.seq {
		return nil, q.seq, false
	}
	if len(q.events) > 0 && since < q.events[0].Seq-1 {
		return nil, q.seq, false
	}
	for _, ev := range q.events {
		if ev.Seq > since {
			events = append(events, ev)
		}
	}
	return events, q.seq, true
}

func (q *Queue) Notify() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.notify
}

// ---------- Path classification ----------

type Classified struct {
	Target       string
	InstancePath []string
	ClassName    string
	IsInit       bool
	Syncable     bool
}

// mapPath returns the Roblox target and base instance segments for rel,
// plus the remaining path below the mapped root.
func mapPath(cfg ProjectConfig, rel string) (target string, base []string, rest string, ok bool) {
	if strings.HasPrefix(rel, pkgPrefix) {
		inner := strings.TrimPrefix(rel, pkgPrefix)
		for _, p := range cfg.Packages {
			if inner == p.PackageName || strings.HasPrefix(inner, p.PackageName+"/") {
				parent, name := splitTarget(p.Target)
				return parent, []string{name}, strings.TrimPrefix(strings.TrimPrefix(inner, p.PackageName), "/"), true
			}
		}
		return "", nil, "", false
	}
	for _, r := range cfg.Roots {
		if rel == r.Dir || strings.HasPrefix(rel, r.Dir+"/") {
			return r.Target, nil, strings.TrimPrefix(strings.TrimPrefix(rel, r.Dir), "/"), true
		}
	}
	return "", nil, "", false
}

func classify(cfg ProjectConfig, rel string, isDir bool) Classified {
	target, base, rest, ok := mapPath(cfg, rel)
	if !ok {
		return Classified{}
	}
	segs := append([]string{}, base...)
	if rest != "" {
		segs = append(segs, strings.Split(rest, "/")...)
	}

	if isDir {
		return Classified{Target: target, InstancePath: segs, ClassName: "Folder", Syncable: true}
	}
	if rest == "" {
		return Classified{}
	}

	file := segs[len(segs)-1]
	parent := append([]string{}, segs[:len(segs)-1]...)
	ext := strings.ToLower(filepath.Ext(file))
	stem := strings.TrimSuffix(file, filepath.Ext(file))

	var class string
	switch ext {
	case ".luau", ".lua":
		switch {
		case strings.HasSuffix(stem, ".server"):
			class, stem = "Script", strings.TrimSuffix(stem, ".server")
		case strings.HasSuffix(stem, ".client"):
			class, stem = "LocalScript", strings.TrimSuffix(stem, ".client")
		default:
			class = "ModuleScript"
		}
	case ".json":
		class = "ModuleScript"
	case ".txt":
		class = "StringValue"
	default:
		return Classified{}
	}

	if strings.EqualFold(stem, "init") {
		if len(parent) == 0 {
			return Classified{} // init directly at a project root has nowhere to go
		}
		return Classified{Target: target, InstancePath: parent, ClassName: class, IsInit: true, Syncable: true}
	}
	return Classified{Target: target, InstancePath: append(parent, stem), ClassName: class, Syncable: true}
}

// ---------- Server ----------

type Server struct {
	cfg     ProjectConfig
	root    string
	queue   *Queue
	watcher *fsnotify.Watcher

	mu      sync.Mutex
	known   map[string]bool
	pending map[string]*time.Timer
}

func (s *Server) relPath(abs string) (string, bool) {
	base := packagesBase()
	if rel, err := filepath.Rel(base, abs); err == nil && !strings.HasPrefix(rel, "..") {
		rel = filepath.ToSlash(rel)
		for _, p := range s.cfg.Packages {
			if rel == p.PackageName || strings.HasPrefix(rel, p.PackageName+"/") {
				return pkgPrefix + rel, true
			}
		}
		return "", false
	}
	rel, err := filepath.Rel(s.root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func (s *Server) absPath(rel string) string {
	if strings.HasPrefix(rel, pkgPrefix) {
		return filepath.Join(packagesBase(), filepath.FromSlash(strings.TrimPrefix(rel, pkgPrefix)))
	}
	return filepath.Join(s.root, filepath.FromSlash(rel))
}

func (s *Server) fileEvent(rel string, op string) (Event, bool) {
	c := classify(s.cfg, rel, false)
	if !c.Syncable {
		return Event{}, false
	}
	ev := Event{Op: op, Path: rel, Target: c.Target, InstancePath: c.InstancePath, ClassName: c.ClassName}
	if op == "DELETE" {
		ev.KeepChildren = c.IsInit
		return ev, true
	}
	data, err := os.ReadFile(s.absPath(rel))
	if err != nil {
		return Event{}, false
	}
	source := string(data)
	if strings.HasSuffix(strings.ToLower(rel), ".json") {
		var parsed any
		if err := json.Unmarshal(data, &parsed); err != nil {
			source = "return " + source
		} else {
			source = "return " + jsonToLuau(parsed)
		}
	}
	ev.Source = source
	return ev, true
}

func jsonToLuau(v any) string {
	switch t := v.(type) {
	case map[string]any:
		var pairs []string
		for k, val := range t {
			pairs = append(pairs, fmt.Sprintf("[%q] = %s", k, jsonToLuau(val)))
		}
		return "{" + strings.Join(pairs, ", ") + "}"
	case []any:
		var items []string
		for _, val := range t {
			items = append(items, jsonToLuau(val))
		}
		return "{" + strings.Join(items, ", ") + "}"
	case string:
		return fmt.Sprintf("%q", t)
	case float64:
		return fmt.Sprintf("%g", t)
	case bool:
		return fmt.Sprintf("%t", t)
	case nil:
		return "nil"
	default:
		return fmt.Sprintf("%v", t)
	}
}

// walkFiles emits CREATE events for every syncable file under absDir.
// Reports whether an init file sits directly at the mapped root.
func (s *Server) walkFiles(absDir string, out *[]Event) (rootInit bool) {
	filepath.Walk(absDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, ok := s.relPath(p)
		if !ok {
			return nil
		}
		if ev, ok := s.fileEvent(rel, "CREATE"); ok {
			*out = append(*out, ev)
			if c := classify(s.cfg, rel, false); c.IsInit && len(c.InstancePath) == 1 {
				rootInit = true
			}
		}
		return nil
	})
	return rootInit
}

func (s *Server) snapshot() []Event {
	var out []Event
	for _, r := range s.cfg.Roots {
		s.walkFiles(filepath.Join(s.root, r.Dir), &out)
	}
	for _, p := range s.cfg.Packages {
		rel := pkgPrefix + p.PackageName
		if !s.walkFiles(s.absPath(rel), &out) {
			// No init: the package is a plain Folder.
			c := classify(s.cfg, rel, true)
			out = append(out, Event{Op: "CREATE", Path: rel, Target: c.Target, InstancePath: c.InstancePath, ClassName: "Folder"})
		}
	}
	// Parents (and init files, which share their folder's depth) before children.
	sort.SliceStable(out, func(i, j int) bool {
		return len(out[i].InstancePath) < len(out[j].InstancePath)
	})
	return out
}

func (s *Server) schedule(abs string) {
	rel, ok := s.relPath(abs)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, exists := s.pending[rel]; exists {
		t.Stop()
	}
	s.pending[rel] = time.AfterFunc(debounceWindow, func() { s.resolve(rel) })
}

func (s *Server) resolve(rel string) {
	s.mu.Lock()
	delete(s.pending, rel)
	wasDir, wasKnown := s.known[rel]
	s.mu.Unlock()

	abs := s.absPath(rel)
	info, err := os.Stat(abs)

	// --- removed ---
	if err != nil {
		if !wasKnown {
			return
		}
		s.mu.Lock()
		delete(s.known, rel)
		if wasDir {
			for k := range s.known {
				if strings.HasPrefix(k, rel+"/") {
					delete(s.known, k)
				}
			}
		}
		s.mu.Unlock()

		if wasDir {
			if c := classify(s.cfg, rel, true); c.Syncable {
				s.queue.Push(Event{Op: "DELETE", Path: rel, Target: c.Target, InstancePath: c.InstancePath})
				log.Printf("[DELETE] %s/", rel)
			}
			return
		}
		if ev, ok := s.fileEvent(rel, "DELETE"); ok {
			s.queue.Push(ev)
			log.Printf("[DELETE] %s", rel)
		}
		return
	}

	// --- directory created/moved in ---
	if info.IsDir() {
		s.mu.Lock()
		s.known[rel] = true
		s.mu.Unlock()
		if wasKnown {
			return
		}
		s.watcher.Add(abs)
		filepath.Walk(abs, func(p string, fi os.FileInfo, err error) error {
			if err != nil || p == abs {
				return nil
			}
			r, ok := s.relPath(p)
			if !ok {
				return nil
			}
			if fi.IsDir() {
				s.watcher.Add(p)
				s.mu.Lock()
				s.known[r] = true
				s.mu.Unlock()
				return nil
			}
			s.resolve(r)
			return nil
		})
		return
	}

	// --- file created/updated ---
	op := "UPDATE"
	if !wasKnown {
		op = "CREATE"
	}
	ev, ok := s.fileEvent(rel, op)
	if !ok {
		return
	}
	s.mu.Lock()
	s.known[rel] = false
	s.mu.Unlock()
	s.queue.Push(ev)
	log.Printf("[%s] %s -> %s.%s (%s)", op, rel, ev.Target, strings.Join(ev.InstancePath, "."), ev.ClassName)
}

func (s *Server) watchLoop() {
	for {
		select {
		case event, ok := <-s.watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) != 0 {
				s.schedule(event.Name)
			}
		case err, ok := <-s.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("Watcher error: %v", err)
		}
	}
}

func (s *Server) primeDir(absDir string) {
	filepath.Walk(absDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, ok := s.relPath(p)
		if !ok {
			return nil
		}
		if info.IsDir() {
			s.watcher.Add(p)
			s.known[rel] = true
		} else if c := classify(s.cfg, rel, false); c.Syncable {
			s.known[rel] = false
		}
		return nil
	})
}

func (s *Server) primeKnown() {
	for _, r := range s.cfg.Roots {
		s.primeDir(filepath.Join(s.root, r.Dir))
	}
	for _, p := range s.cfg.Packages {
		s.primeDir(s.absPath(pkgPrefix + p.PackageName))
	}
}

// ---------- HTTP ----------

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token != "" && r.Header.Get("X-ChainLink-Token") != s.cfg.Token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true, "sessionId": s.queue.sessionID})
}

func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	session := r.URL.Query().Get("session")

	needFull := (since == 0 && session == "") || session != s.queue.sessionID
	if !needFull {
		events, latest, ok := s.queue.Since(since)
		if !ok {
			needFull = true
		} else if len(events) > 0 {
			writeJSON(w, ChangesResponse{SessionID: s.queue.sessionID, Seq: latest, Events: events})
			return
		}
	}
	if needFull {
		_, latest, _ := s.queue.Since(0)
		writeJSON(w, ChangesResponse{SessionID: s.queue.sessionID, Seq: latest, Full: true, Events: s.snapshot()})
		return
	}

	deadline := time.After(longPollTimeout)
	for {
		events, latest, _ := s.queue.Since(since)
		if len(events) > 0 {
			writeJSON(w, ChangesResponse{SessionID: s.queue.sessionID, Seq: latest, Events: events})
			return
		}
		notify := s.queue.Notify()
		select {
		case <-notify:
		case <-deadline:
			writeJSON(w, ChangesResponse{SessionID: s.queue.sessionID, Seq: latest, Events: []Event{}})
			return
		case <-r.Context().Done():
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// ---------- Entry point ----------

func serve() error {
	fmt.Println("Starting ChainLink serve...")
	cfg := loadConfig()

	root, err := os.Getwd()
	if err != nil {
		return err
	}

	for _, r := range cfg.Roots {
		if !dirExists(filepath.Join(root, r.Dir)) {
			fmt.Printf("Warning: directory %s/ not found. It will be ignored until created.\n", r.Dir)
		}
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	defer watcher.Close()

	s := &Server{
		cfg:     cfg,
		root:    root,
		queue:   NewQueue(),
		watcher: watcher,
		known:   map[string]bool{},
		pending: map[string]*time.Timer{},
	}
	watcher.Add(root)
	s.primeKnown()
	go s.watchLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.auth(s.handleHealth))
	mux.HandleFunc("/changes", s.auth(s.handleChanges))

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	fmt.Printf("Listening on http://%s  (session %s)\n", addr, s.queue.sessionID)
	for _, r := range cfg.Roots {
		fmt.Printf("  %-8s -> %s\n", r.Dir+"/", r.Target)
	}
	for _, p := range cfg.Packages {
		abs := s.absPath(pkgPrefix + p.PackageName)
		status := "NOT FOUND"
		if dirExists(abs) {
			var tmp []Event
			if s.walkFiles(abs, &tmp) {
				status = fmt.Sprintf("ModuleScript, %d files", len(tmp))
			} else {
				status = fmt.Sprintf("Folder (no init), %d files", len(tmp))
			}
		}
		fmt.Printf("  pkg %s -> %s  [%s]\n", abs, p.Target, status)
	}
	fmt.Println("Press Ctrl+C to stop.")

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return srv.ListenAndServe()
}
