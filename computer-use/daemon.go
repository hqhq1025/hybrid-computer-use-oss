package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The daemon serves one desktop to many local clients over a Unix socket.
// It is core plus a socket transport: every method maps to a public tool
// call through the same callTool, records and text rendering as MCP.
//
// Wire format: one JSON-RPC 2.0 message per line, in both directions.
//
//	hello  {client_version?, session?}          -> {version, contract_version, session, desktop}
//	call   {tool, args, format?, request_id?, since?} -> Record (format records)
//	                                            or {content, isError} (format text)
//	schema {}                                   -> describeContract()
//	mcp    {message}                            -> MCP response for an attached MCP server
//	stop   {}                                   -> {} then the daemon exits
//
// Sessions: each connection gets its own session state unless hello names
// a session; connections that name the same session share it. Desktop
// state (refs, provider, request_id results) is shared by all.

const daemonProtocolVersion = 1

const defaultDaemonIdleTimeout = 30 * time.Minute

// daemon is one running server.
type daemon struct {
	socket      string
	desktop     *desktopState
	environment string
	// display is the DISPLAY (or WAYLAND_DISPLAY) the daemon serves.
	display     string
	idleTimeout time.Duration
	// currentEnvironment re-reads the desktop environment before each call;
	// nil skips the check (tests).
	currentEnvironment func() string

	mu       sync.Mutex
	sessions map[string]*service
	active   int
	lastUsed time.Time
	listener net.Listener
	stopped  chan struct{}
	stopOnce sync.Once
	nextConn int
	// savedCounters is the last value written by saveRefCounters.
	savedCounters refCounters
}

// desktopFingerprint is the session environment the daemon serves. A
// client whose environment differs is talking about another desktop.
func desktopFingerprint(environment []string) string {
	values := map[string]string{}
	for _, item := range environment {
		key, value, _ := strings.Cut(item, "=")
		values[key] = value
	}
	parts := []string{}
	for _, key := range []string{"DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR"} {
		parts = append(parts, key+"="+values[key])
	}
	return strings.Join(parts, ";")
}

// environmentDisplay is WAYLAND_DISPLAY, or DISPLAY when that is empty.
func environmentDisplay(environment []string) string {
	values := map[string]string{}
	for _, item := range environment {
		key, value, _ := strings.Cut(item, "=")
		values[key] = value
	}
	return firstNonEmpty(values["WAYLAND_DISPLAY"], values["DISPLAY"])
}

// defaultDaemonSocket is $XDG_RUNTIME_DIR/hcu/<display>.sock, or a per-user
// directory under the temp dir when no runtime directory exists.
func defaultDaemonSocket() string {
	if path := strings.TrimSpace(os.Getenv("HCU_SOCKET")); path != "" {
		return path
	}
	base := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR"))
	if base == "" {
		base = filepath.Join(os.TempDir(), fmt.Sprintf("hcu-%d", os.Getuid()))
	} else {
		base = filepath.Join(base, "hcu")
	}
	display := strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY"))
	if display == "" {
		display = strings.TrimSpace(os.Getenv("DISPLAY"))
	}
	name := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '_'
	}, strings.TrimPrefix(display, ":"))
	if name == "" {
		name = "default"
	}
	return filepath.Join(base, "desktop-"+name+".sock")
}

// prepareSocketPath creates the socket directory with mode 0700 and refuses
// a directory or socket that another user owns. A stale socket left by a
// crashed daemon is removed; a live one means another daemon is serving.
func prepareSocketPath(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fileOwnedByUID(info, os.Getuid()) {
		return fmt.Errorf("socket directory %s is owned by another user", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	existing, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fileOwnedByUID(existing, os.Getuid()) {
		return fmt.Errorf("socket %s is owned by another user; refusing to replace it", path)
	}
	if existing.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s exists and is not a socket", path)
	}
	if conn, dialErr := net.DialTimeout("unix", path, 500*time.Millisecond); dialErr == nil {
		conn.Close()
		return fmt.Errorf("a daemon is already serving %s", path)
	}
	return os.Remove(path)
}

func newDaemon(socket string, desktop *desktopState, environment []string, idle time.Duration) *daemon {
	if idle <= 0 {
		idle = defaultDaemonIdleTimeout
	}
	server := &daemon{
		socket:      socket,
		desktop:     desktop,
		environment: desktopFingerprint(environment),
		display:     environmentDisplay(environment),
		idleTimeout: idle,
		sessions:    map[string]*service{},
		lastUsed:    time.Now(),
		stopped:     make(chan struct{}),
	}
	server.loadRefCounters()
	return server
}

// refCounters are the next aN, wN and rN numbers. The daemon keeps them in a
// file beside the socket, so a restarted daemon never issues a number that a
// client of the previous daemon still holds: an old ref then fails as stale
// instead of naming a different element.
type refCounters struct {
	App     uint64 `json:"a"`
	Window  uint64 `json:"w"`
	Element uint64 `json:"r"`
}

func (d *daemon) countersPath() string { return d.socket + ".refs" }

func (d *daemon) loadRefCounters() {
	data, err := os.ReadFile(d.countersPath())
	if err != nil {
		return
	}
	var counters refCounters
	if json.Unmarshal(data, &counters) != nil {
		return
	}
	if counters.App > d.desktop.appRefRegistry.next {
		d.desktop.appRefRegistry.next = counters.App
	}
	if counters.Window > d.desktop.windowRefRegistry.next {
		d.desktop.windowRefRegistry.next = counters.Window
	}
	if counters.Element > d.desktop.refRegistry.next {
		d.desktop.refRegistry.next = counters.Element
	}
}

// saveRefCounters runs after each call, under the desktop lock.
func (d *daemon) saveRefCounters() {
	counters := refCounters{
		App:     d.desktop.appRefRegistry.next,
		Window:  d.desktop.windowRefRegistry.next,
		Element: d.desktop.refRegistry.next,
	}
	if counters == d.savedCounters {
		return
	}
	data, _ := json.Marshal(counters)
	if os.WriteFile(d.countersPath(), data, 0o600) == nil {
		d.savedCounters = counters
	}
}

// listen binds the socket with mode 0600.
func (d *daemon) listen() error {
	if err := prepareSocketPath(d.socket); err != nil {
		return err
	}
	listener, err := net.Listen("unix", d.socket)
	if err != nil {
		return err
	}
	if err := os.Chmod(d.socket, 0o600); err != nil {
		listener.Close()
		return err
	}
	d.listener = listener
	return nil
}

// serve accepts connections until stop, an idle timeout, or a listener
// error. It removes the socket on exit.
func (d *daemon) serve() error {
	defer os.Remove(d.socket)
	go d.watchIdle()
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			select {
			case <-d.stopped:
				return nil
			default:
				return err
			}
		}
		if !peerIsSameUser(conn) {
			conn.Close()
			continue
		}
		d.mu.Lock()
		d.active++
		d.nextConn++
		connID := d.nextConn
		d.mu.Unlock()
		go func() {
			d.handleConn(conn, connID)
			d.mu.Lock()
			d.active--
			d.lastUsed = time.Now()
			d.mu.Unlock()
		}()
	}
}

func (d *daemon) stop() {
	d.stopOnce.Do(func() {
		close(d.stopped)
		if d.listener != nil {
			d.listener.Close()
		}
	})
}

// watchIdle stops the daemon when no client has been connected for the
// idle timeout.
func (d *daemon) watchIdle() {
	tick := d.idleTimeout / 10
	if tick > time.Second {
		tick = time.Second
	}
	if tick < 10*time.Millisecond {
		tick = 10 * time.Millisecond
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-d.stopped:
			return
		case <-ticker.C:
			d.mu.Lock()
			idle := d.active == 0 && time.Since(d.lastUsed) >= d.idleTimeout
			d.mu.Unlock()
			if idle {
				d.stop()
				return
			}
		}
	}
}

// session returns the named session, creating it on first use.
func (d *daemon) session(name string) *service {
	d.mu.Lock()
	defer d.mu.Unlock()
	if svc := d.sessions[name]; svc != nil {
		return svc
	}
	svc := newSessionService(d.desktop)
	d.sessions[name] = svc
	return svc
}

func (d *daemon) handleConn(conn net.Conn, connID int) {
	defer conn.Close()
	reader := bufio.NewReaderSize(conn, 64*1024)
	encoder := json.NewEncoder(conn)
	sessionName := fmt.Sprintf("conn-%d", connID)
	for {
		line, oversized, err := readMCPLine(reader)
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 || oversized {
			var response map[string]any
			if oversized {
				response = jsonRPCError(nil, -32600, "message too large")
			} else {
				response = d.handleRequest(trimmed, &sessionName)
			}
			if response != nil {
				if encodeErr := encoder.Encode(response); encodeErr != nil {
					return
				}
			}
			d.mu.Lock()
			d.lastUsed = time.Now()
			d.mu.Unlock()
		}
		if err != nil {
			return
		}
		select {
		case <-d.stopped:
			return
		default:
		}
	}
}

func (d *daemon) handleRequest(line []byte, sessionName *string) map[string]any {
	request, err := decodeMCPRequest(line)
	if err != nil {
		return jsonRPCError(nil, -32700, "Parse error: invalid JSON")
	}
	id := request["id"]
	method, _ := request["method"].(string)
	params, _ := request["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
	}
	switch method {
	case "hello":
		// A client names its display only when its own environment sets
		// one; the daemon discovers the session for clients that do not.
		if display, _ := params["display"].(string); display != "" && display != d.display {
			return jsonRPCError(id, daemonErrorEnvironment, fmt.Sprintf(
				"this daemon serves display %q, the client runs on %q; use another socket",
				d.display, display))
		}
		if name, _ := params["session"].(string); strings.TrimSpace(name) != "" {
			*sessionName = strings.TrimSpace(name)
		}
		d.session(*sessionName)
		return jsonRPCResult(id, map[string]any{
			"protocol":         daemonProtocolVersion,
			"version":          version,
			"contract_version": contractVersion,
			"session":          *sessionName,
			"desktop":          d.environment,
			"pid":              os.Getpid(),
		})
	case "schema":
		return jsonRPCResult(id, describeContract())
	case "status":
		d.mu.Lock()
		names := make([]string, 0, len(d.sessions))
		for name := range d.sessions {
			names = append(names, name)
		}
		active := d.active
		d.mu.Unlock()
		sort.Strings(names)
		return jsonRPCResult(id, map[string]any{
			"version": version, "contract_version": contractVersion, "pid": os.Getpid(),
			"socket": d.socket, "desktop": d.environment, "sessions": names, "connections": active,
		})
	case "stop":
		go func() {
			time.Sleep(50 * time.Millisecond)
			d.stop()
		}()
		return jsonRPCResult(id, map[string]any{})
	case "call":
		return jsonRPCResult(id, d.call(d.session(*sessionName), params))
	case "mcp":
		message, _ := params["message"].(map[string]any)
		if message == nil {
			return jsonRPCError(id, -32602, "mcp requires message")
		}
		svc := d.session(*sessionName)
		d.desktop.shared.mu.Lock()
		response := handleMCPRequest(message, svc)
		d.desktop.shared.mu.Unlock()
		return jsonRPCResult(id, map[string]any{"message": response})
	default:
		return jsonRPCError(id, -32601, "Method not found: "+method)
	}
}

const daemonErrorEnvironment = -32010

// textCallResponse is a text-format call over the socket: the model-facing
// result plus its public error code, which the CLI maps to an exit status.
type textCallResponse struct {
	toolCallResult
	Code string `json:"code,omitempty"`
}

// call runs one public tool call for a session. The desktop lock serializes
// calls from all sessions, so input from two clients never interleaves.
func (d *daemon) call(svc *service, params map[string]any) any {
	tool, _ := params["tool"].(string)
	args, _ := params["args"].(map[string]any)
	if args == nil {
		args = map[string]any{}
	}
	format, _ := params["format"].(string)
	if format == "" {
		format = formatRecords
	}
	options := callOptions{Format: format}
	options.RequestID, _ = params["request_id"].(string)
	options.Since, _ = params["since"].(string)
	publicName, ok := publicToolCallName(tool)
	if !ok {
		return Record{OK: false, Op: tool, Code: axCodeInvalidArgument, Path: "tool",
			Message: fmt.Sprintf("unknown tool %q; use AX or GUI", tool), TimingMS: map[string]int64{}}
	}
	run := func() any {
		d.desktop.shared.mu.Lock()
		defer d.desktop.shared.mu.Unlock()
		defer d.saveRefCounters()
		if d.currentEnvironment != nil {
			if now := d.currentEnvironment(); now != d.environment {
				return Record{OK: false, Op: recordOp(publicName, args), Code: axCodeRuntimeError,
					Message: fmt.Sprintf("the desktop environment changed from %q to %q since this daemon started; "+
						"no call was run. Stop this daemon and start a new one", d.environment, now),
					TimingMS: map[string]int64{}}
			}
		}
		if format == formatText {
			result, code, err := svc.callForText(publicName, args)
			if err != nil {
				return textResult(err.Error(), true)
			}
			return textCallResponse{toolCallResult: result, Code: code}
		}
		return svc.callRecord(publicName, args, options)
	}
	if strings.TrimSpace(options.RequestID) == "" {
		return run()
	}
	key := publicName + "\x00" + compactJSON(args) + "\x00" + format + "\x00" + options.Since
	response, replayed, err := d.desktop.shared.replayOnce(options.RequestID, key, run)
	if err != nil {
		return Record{OK: false, Op: recordOp(publicName, args), RequestID: options.RequestID,
			Code: axCodeInvalidArgument, Path: "request_id", Message: err.Error(),
			TimingMS: map[string]int64{}}
	}
	if record, isRecord := response.(Record); isRecord {
		record.RequestID = options.RequestID
		record.Replayed = replayed
		return record
	}
	return response
}

// runServe implements `computer-use serve`.
func runServe(args []string, stderr io.Writer) error {
	socket := defaultDaemonSocket()
	idle := defaultDaemonIdleTimeout
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--socket":
			index++
			if index >= len(args) {
				return usageError("--socket requires a path")
			}
			socket = args[index]
		case "--idle-timeout":
			index++
			if index >= len(args) {
				return usageError("--idle-timeout requires a duration")
			}
			parsed, err := time.ParseDuration(args[index])
			if err != nil || parsed <= 0 {
				return usageError("--idle-timeout must be a positive duration such as 30m")
			}
			idle = parsed
		default:
			return usageError("unknown serve option: " + args[index])
		}
	}
	backend := defaultRuntimeBackend()
	server := newDaemon(socket, newDesktopState(backend), backend.RuntimeEnvironment(os.Environ()), idle)
	server.currentEnvironment = func() string {
		return desktopFingerprint(backend.RuntimeEnvironment(os.Environ()))
	}
	if err := server.listen(); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "computer-use serve: listening on %s\n", socket)
	return server.serve()
}

// usageErr is a command-line usage error; the CLI exits 64 for it.
type usageErr struct{ message string }

func (e *usageErr) Error() string { return e.message }

func usageError(message string) error { return &usageErr{message: message} }
