package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
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
)

// ---------- Config ----------

type RootMapping struct {
	Dir    string `json:"dir"`    // "client"
	Target string `json:"target"` // "StarterPlayer.StarterPlayerScripts"
}

type ProjectConfig struct {
	Port     int                    `json:"port"`
	Token    string                 `json:"token"`
	Roots    []RootMapping          `json:"roots"`
	Packages []GlobalPackageMapping `json:"packages"`
}

type GlobalPackageMapping struct {
	PackageName string `json:"package"`
	Target      string `json:"target"`
}

type ChainToml struct {
	Packages map[string]string `toml:"Packages"`
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

	// 1. Load chainlink.json (Overrides/Explicit config)
	data, err := os.ReadFile(configFileName)
	if err == nil {
		var file ProjectConfig
		if err := json.Unmarshal(data, &file); err == nil {
			if file.Port != 0 {
				cfg.Port = file.Port
			}
			if file.Token != "" {
				cfg.Token = file.Token
			}
			if len(file.Roots) > 0 {
				cfg.Roots = file.Roots
			}
			if len(file.Packages) > 0 {
				cfg.Packages = file.Packages
			}
		}
	}

	// 2. Load chain.toml (Package discovery)
	var tomlCfg ChainToml
	if _, err := toml.DecodeFile("chain.toml", &tomlCfg); err == nil {
		for pkgSpec := range tomlCfg.Packages {
			// pkgSpec is e.g. "vibeonline/razorinput@0.1.0"
			// We need to convert this to a folder name like "vibeonline_razorinput/0.1.0"

			// Split "vibeonline/razorinput@0.1.0" into "vibeonline/razorinput" and "0.1.0"
			parts := strings.Split(pkgSpec, "@")
			var name, version string
			if len(parts) == 2 {
				name = parts[0]
				version = parts[1]
			} else {
				name = pkgSpec
				version = "latest" // fallback
			}

			// Convert "vibeonline/razorinput" to "vibeonline_razorinput"
			folderName := strings.ReplaceAll(name, "/", "_")

			// The logical package name for classification (e.g. "razorinput")
			// is the part after the last slash in the name.
			nameParts := strings.Split(name, "/")
			shortName := nameParts[len(nameParts)-1]

			// The actual disk path is ~/.chainlink/packages/vibeonline_razorinput/0.1.0
			diskPath := folderName + "/" + version

			// Avoid duplicates
			exists := false
			for _, p := range cfg.Packages {
				if p.PackageName == diskPath {
					exists = true
					break
				}
			}
			if !exists {
				cfg.Packages = append(cfg.Packages, GlobalPackageMapping{
					PackageName: diskPath, // Used for disk lookup
					Target:      "ReplicatedStorage.packages." + shortName,
				})
			}
		}
	}

	return cfg
}

// ---------- Events / Queue ----------

type Event struct {
	Seq          uint64   `json:"seq"`
	Op           string   `json:"op"`   // CREATE | UPDATE | DELETE
	Path         string   `json:"path"` // relative, forward-slash (for logging)
	Target       string   `json:"target"`
	InstancePath []string `json:"instancePath"`
	ClassName    string   `json:"className,omitempty"`
	Source       string   `json:"source,omitempty"`
	KeepChildren bool     `json:"keepChildren,omitempty"` // DELETE of init.* -> demote to Folder
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

// Since returns events after `since`. ok=false means the client needs a full snapshot.
func (q *Queue) Since(since uint64) (events []Event, latest uint64, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if since > q.seq {
		return nil, q.seq, false
	}
	if len(q.events) > 0 && since < q.events[0].Seq-1 {
		return nil, q.seq, false // trimmed past what they have
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

// classify maps a relative forward-slash path to its Roblox location.
// isDir=true classifies the path as a container (Folder) rather than a file.
func classify(cfg ProjectConfig, rel string, isDir bool) Classified {
	var target string

	// 1. Try project root mappings
	for _, r := range cfg.Roots {
		if strings.HasPrefix(rel, r.Dir+"/") || rel == r.Dir {
			target = r.Target
			break
		}
	}

	// 2. Try global package mappings
	if target == "" {
		for _, p := range cfg.Packages {
			if strings.HasPrefix(rel, p.PackageName+"/") || rel == p.PackageName {
				target = p.Target
				break
			}
		}
	}

	if target == "" {
		return Classified{}
	}

	// Calculate segments relative to the target root
	var segs []string
	if strings.Contains(rel, "/") {
		// Remove the root part (e.g., "shared/" or "vibeonline_razorinput/0.1.0/")
		var rootPart string
		for _, r := range cfg.Roots {
			if strings.HasPrefix(rel, r.Dir+"/") {
				rootPart = r.Dir + "/"
				break
			}
		}
		if rootPart == "" {
			for _, p := range cfg.Packages {
				if strings.HasPrefix(rel, p.PackageName+"/") {
					rootPart = p.PackageName + "/"
					break
				}
			}
		}

		if rootPart != "" {
			segs = strings.Split(strings.TrimPrefix(rel, rootPart), "/")
		} else {
			// Handle the case where rel is exactly the root
			segs = []string{}
		}
	} else {
		segs = []string{}
	}

	if isDir {
		return Classified{Target: target, InstancePath: segs, ClassName: "Folder", Syncable: true}
	}
	if len(segs) == 0 {
		return Classified{}
	}
	file := segs[len(segs)-1]
	parent := segs[:len(segs)-1]
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
		return Classified{} // .rbxm/.rbxmx not supported yet
	}

	if stem == "init" {
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
	known   map[string]bool // rel path -> isDir
	pending map[string]*time.Timer
}

func (s *Server) relPath(abs string) (string, bool) {
	// First check if it's a global package path
	home, _ := os.UserHomeDir()
	pkgBase := filepath.Join(home, ".chainlink", "packages")
	if strings.HasPrefix(abs, pkgBase) {
		rel, err := filepath.Rel(pkgBase, abs)
		if err == nil {
			return filepath.ToSlash(rel), true
		}
	}

	// Otherwise, relative to root
	rel, err := filepath.Rel(s.root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func (s *Server) fileEvent(rel string, op string) (Event, bool) {
	c := classify(s.cfg, rel, false)
	if !c.Syncable {
		return Event{}, false
	}
	ev := Event{Op: op, Path: rel, Target: c.Target, InstancePath: c.InstancePath, ClassName: c.ClassName}
	if op != "DELETE" {
		var abs string
		// Use a unified path resolver instead of manually checking slashes
		home, _ := os.UserHomeDir()
		pkgBase := filepath.Join(home, ".chainlink", "packages")

		// Check if it's a global package
		isGlobal := false
		for _, p := range s.cfg.Packages {
			if strings.HasPrefix(rel, p.PackageName+"/") || rel == p.PackageName {
				abs = filepath.Join(pkgBase, rel)
				isGlobal = true
				break
			}
		}

		if !isGlobal {
			abs = filepath.Join(s.root, filepath.FromSlash(rel))
		}

		// Basic path traversal check
		if !strings.HasPrefix(abs, s.root) && !strings.HasPrefix(abs, pkgBase) {
			return Event{}, false
		}

		data, err := os.ReadFile(abs)
		if err != nil {
			return Event{}, false
		}
		source := string(data)
		// Wrap JSON as a Luau table return
		if strings.HasSuffix(rel, ".json") {
			var parsed any
			if err := json.Unmarshal([]byte(source), &parsed); err != nil {
				// Fallback: if JSON is invalid, just prepend return and hope for the best
				source = fmt.Sprintf("return %s", source)
			} else {
				source = fmt.Sprintf("return %s", jsonToLuau(parsed))
			}
		}
		ev.Source = source
	} else if c.IsInit {
		ev.KeepChildren = true
	}
	return ev, true
}

func jsonToLuau(v any) string {
	switch t := v.(type) {
	case map[string]any:
		var pairs []string
		for k, val := range t {
			pairs = append(pairs, fmt.Sprintf("[%q] = %s", k, jsonToLuau(val)))
		}
		return fmt.Sprintf("{%s}", strings.Join(pairs, ", "))
	case []any:
		var items []string
		for _, val := range t {
			items = append(items, jsonToLuau(val))
		}
		return fmt.Sprintf("{%s}", strings.Join(items, ", "))
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

func (s *Server) snapshot() []Event {
	var out []Event

	// 1. Project root snapshot
	for _, r := range s.cfg.Roots {
		base := filepath.Join(s.root, r.Dir)
		filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, ok := s.relPath(p)
			if !ok {
				return nil
			}
			if ev, ok := s.fileEvent(rel, "CREATE"); ok {
				out = append(out, ev)
			}
			return nil
		})
	}

	// 2. Global packages snapshot
	home, _ := os.UserHomeDir()
	for _, p := range s.cfg.Packages {
		// p.PackageName is "vibeonline_razorinput/0.1.0"
		pkgRoot := filepath.Join(home, ".chainlink", "packages", p.PackageName)
		
		filepath.Walk(pkgRoot, func(pAbs string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			// Path is relative to ~/.chainlink/packages/
			// e.g. "vibeonline_razorinput/0.1.0/src/init.luau"
			rel, err := filepath.Rel(filepath.Join(home, ".chainlink", "packages"), pAbs)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if ev, ok := s.fileEvent(rel, "CREATE"); ok {
				out = append(out, ev)
			}
			return nil
		})
	}

	return out
}

// schedule debounces a path; when the timer fires we look at the *actual* state on disk.
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
	wasKnown, wasDir := false, false
	if d, ok := s.known[rel]; ok {
		wasKnown, wasDir = true, d
	}
	s.mu.Unlock()

	var abs string
	if strings.Contains(rel, "/") {
		parts := strings.SplitN(rel, "/", 2)
		pkgName := parts[0]
		home, _ := os.UserHomeDir()
		isGlobal := false
		for _, p := range s.cfg.Packages {
			if p.PackageName == pkgName {
				isGlobal = true
				break
			}
		}
		if isGlobal {
			abs = filepath.Join(home, ".chainlink", "packages", pkgName, parts[1])
		} else {
			abs = filepath.Join(s.root, filepath.FromSlash(rel))
		}
	} else {
		abs = filepath.Join(s.root, filepath.FromSlash(rel))
	}

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
			c := classify(s.cfg, rel, true)
			if c.Syncable {
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
		// Walk contents: new subdirs get watched, files get emitted.
		filepath.Walk(abs, func(p string, fi os.FileInfo, err error) error {
			if err != nil {
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

func (s *Server) primeKnown() {
	for _, r := range s.cfg.Roots {
		base := filepath.Join(s.root, r.Dir)
		filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
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

	// Also prime known global packages to avoid them being treated as NEW files if they are modified
	home, _ := os.UserHomeDir()
	pkgBase := filepath.Join(home, ".chainlink", "packages")
	for _, p := range s.cfg.Packages {
		pkgRoot := filepath.Join(pkgBase, p.PackageName)
		filepath.Walk(pkgRoot, func(pAbs string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			rel, ok := s.relPath(pAbs)
			if !ok {
				return nil
			}
			if info.IsDir() {
				s.watcher.Add(pAbs)
				s.known[rel] = true
			} else if c := classify(s.cfg, rel, false); c.Syncable {
				s.known[rel] = false
			}
			return nil
		})
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

// serve handles the "chainlink serve" command.
func serve() error {
	fmt.Println("Starting ChainLink serve...")
	cfg := loadConfig()

	root, err := os.Getwd()
	if err != nil {
		return err
	}

	for _, r := range cfg.Roots {
		if _, err := os.Stat(filepath.Join(root, r.Dir)); os.IsNotExist(err) {
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
	// Watch the project root so newly-created root dirs (e.g. prefab/) get picked up.
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
	fmt.Println("Press Ctrl+C to stop.")

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return srv.ListenAndServe()
}
