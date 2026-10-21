package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v4"
	"golang.org/x/sys/unix"

	"runlink/internal/frontend"
)

const (
	maxSessions   = 16               // recipients connecting or connected at once
	authTimeout   = 30 * time.Second // from the answer to a verified peer
	maxMessage    = 64 << 10         // largest data-channel message the runner reads
	outputChunk   = 16 << 10
	gatherTimeout = 5 * time.Second
	// A write that the browser does not take within this time ends its session,
	// so a stalled recipient never holds a run or another recipient's turn.
	writeTimeout = 15 * time.Second
)

// share registers the task with the service, prints its link, and serves
// recipients until ctx ends or the owner presses Ctrl-C. Sessions already open
// outlive the service connection.
func share(ctx context.Context, origin string, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("share", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	separate := flags.Bool("separate-secret", false, "print the secret apart from the link")
	edit := flags.Bool("edit", false, "change a script's stored task file, then share")
	const usage = "usage: runlink share [--separate-secret] [--edit] <task.json or executable>; see docs/tasks.md"
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(stdout, usage)
		return 0
	} else if err != nil || flags.NArg() != 1 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	path := flags.Arg(0)
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	st := newStyle(getenv, isTerminal(stdout))
	r := &region{w: stdout, st: st, width: func() int { return width(stdout) }}
	if st.live {
		defer fmt.Fprint(stdout, "\x1b[?25h")
	}
	// At a terminal, keys arrive one by one: the setup's answers, then Enter
	// to open the link and Ctrl-C to stop.
	var k *keys
	var a *asker
	interactive := isTerminal(stdin) && isTerminal(stdout)
	if interactive {
		restore, err := rawMode(stdin.(*os.File))
		if err != nil {
			fmt.Fprintln(stderr, "runlink: cannot read keys from the terminal:", err)
			return 1
		}
		defer restore()
		k = readKeys(stdin)
		if st.live {
			a = &asker{ctx: ctx, keys: k, st: st, r: r}
		}
	}
	t, from, err := taskFor(path, *edit, a, notesFile(getenv), func(err error) { fmt.Fprintln(stderr, "runlink:", err) })
	if errors.Is(err, errCanceled) {
		r.draw([]string{"", "  Canceled. Nothing was saved."}, nil, -1, 0)
		return 130
	} else if err != nil {
		fmt.Fprintln(stderr, "runlink:", err)
		return 2
	}

	v := newView(st, r, stdout, stderr)
	defer v.close()
	var open atomic.Pointer[string]
	if k != nil {
		go func() {
			for {
				switch k.next(ctx).kind {
				case keyCancel:
					stop()
					return
				case keyEnter:
					// An opener can take its time, or hang; Ctrl-C still stops sharing.
					if link := open.Load(); link != nil {
						go func() {
							if err := browse(ctx, *link); err != nil && ctx.Err() == nil {
								v.notice("Could not open a browser: " + err.Error())
							}
						}()
					}
				}
			}
		}()
	}
	v.connecting(origin)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// Connecting and registering get ten seconds together.
	startCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(startCtx, origin+"/api/v1/share", &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		v.close()
		if ctx.Err() != nil {
			return 130
		}
		fmt.Fprintf(stderr, "runlink: %s is unreachable%s\n", origin, localHint(origin))
		return 1
	}
	defer conn.CloseNow()
	conn.SetReadLimit(2 * maxMessage)
	var registered struct {
		Task  string `json:"task"`
		Error string `json:"error"`
	}
	if err := wsjson.Read(startCtx, conn, &registered); err != nil || !frontend.TaskPath.MatchString("/"+registered.Task) {
		v.close()
		switch {
		case ctx.Err() != nil:
			return 130
		case registered.Error == "capacity_exceeded":
			fmt.Fprintf(stderr, "runlink: %s is not accepting new tasks right now\n", origin)
		case startCtx.Err() != nil:
			fmt.Fprintf(stderr, "runlink: %s did not register the task in time\n", origin)
		default:
			fmt.Fprintf(stderr, "runlink: %s returned an unexpected response\n", origin)
		}
		return 1
	}
	cancel()

	secret := make([]byte, 32)
	_, _ = rand.Read(secret) // never fails; see crypto/rand
	link, key := origin+"/"+registered.Task, base64.RawURLEncoding.EncodeToString(secret)
	full := link + "#" + key
	copied := ""
	if interactive {
		what := full
		if *separate {
			what = link
		}
		copied = clipboard(ctx, stdout, what)
		open.Store(&full)
	}
	v.begin(header(st, t.Title, link, key, from, path, copied, *separate, risks(t)))

	s := newSharer(ctx, t, registered.Task, secret, v)
	defer s.sessions.Wait()
	for {
		var offer struct {
			Session string             `json:"session"`
			SDP     string             `json:"sdp"`
			ICE     []webrtc.ICEServer `json:"ice"`
		}
		if err := wsjson.Read(ctx, conn, &offer); err != nil {
			break
		}
		s.sessions.Go(func() {
			answer, err := s.answer(offer.SDP, offer.ICE)
			if err != nil {
				v.notice("Could not answer a recipient: " + err.Error())
			}
			// An empty answer tells the browser at once that this machine won't connect.
			_ = wsjson.Write(ctx, conn, map[string]string{"session": offer.Session, "sdp": answer})
		})
	}
	if ctx.Err() == nil {
		v.notice("Lost the connection to " + origin + ". New recipients can't connect; open sessions continue until Ctrl-C.")
		<-ctx.Done()
	}
	return 0
}

// header is what share shows above the status: the task, its link, how to use
// it, and any guard the owner turned off. Without cursor control it keeps the
// plain wording.
func header(st style, title, link, key, from, path, copied string, separate bool, risk string) []string {
	if !st.live {
		lines := []string{fmt.Sprintf("Sharing %q. Send this link:", title), "", "  " + link + "#" + key, ""}
		if separate {
			lines = []string{fmt.Sprintf("Sharing %q. Send the link and the secret separately:", title), "", "  Link    " + link, "  Secret  " + key, ""}
		}
		lines = append(lines, "Anyone with it can run the task on this machine while this command runs. Ctrl-C stops sharing.")
		if risk != "" {
			lines = append(lines, "Unsafe by the task file's choice: "+risk+".")
		}
		if copied != "" {
			lines = append(lines, copied+" Press Enter to open it in your browser.")
		}
		return lines
	}
	dot := st.sgr(dim, "  "+st.sym("·", "-")+"  ")
	lines := []string{"", "  " + st.sgr(bold, escape(title)) + dot + st.sgr(green, "Sharing") + dot + st.sgr(dim, "Ctrl-C to stop")}
	if from != "" {
		lines = append(lines, "  "+st.sgr(dim, "From "+escape(from)+"; runlink share --edit "+commandLine([]string{path})+" changes it"))
	}
	lines = append(lines, "")
	if separate {
		lines = append(lines, "  "+st.sgr(dim, "Link    ")+st.tint(link), "  "+st.sgr(dim, "Secret  ")+st.tint(key))
	} else {
		lines = append(lines, "  "+st.tint(link+"#"+key))
	}
	if copied != "" {
		opens := "Enter opens it in your browser"
		if separate {
			copied, opens = strings.Replace(copied, "Copied", "Copied the link", 1), "Enter opens it with the secret"
		}
		lines = append(lines, "  "+st.sgr(green, st.sym("✓", "+"))+" "+strings.TrimSuffix(copied, ".")+dot+opens)
	}
	lines = append(lines, "  "+st.sgr(dim, "Anyone with the link can run this task on this machine until you stop."))
	if risk != "" {
		lines = append(lines, "  "+st.sgr(yellow, st.sym("⚠", "!")+" Unsafe by your choice: "+risk))
	}
	return append(lines, "")
}

// isTerminal reports whether w is a terminal. /dev/null is a character device
// too, but has no terminal settings.
func isTerminal(w any) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	_, err := unix.IoctlGetTermios(int(f.Fd()), getTermios)
	return err == nil
}

// clipboard copies text with the platform's tool or, without one, asks the
// terminal to through OSC 52, which also reaches the owner's machine over SSH.
func clipboard(ctx context.Context, term io.Writer, text string) string {
	// The tools get two seconds together, so a hung one cannot hold up sharing.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var tools [][]string
	switch runtime.GOOS {
	case "darwin":
		tools = [][]string{{"pbcopy"}}
	case "linux":
		tools = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	}
	for _, tool := range tools {
		cmd := exec.CommandContext(ctx, tool[0], tool[1:]...)
		cmd.Stdin = strings.NewReader(text)
		cmd.WaitDelay = time.Second
		if cmd.Run() == nil {
			return "Copied to the clipboard."
		}
	}
	fmt.Fprintf(term, "\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(text)))
	return "Sent to the terminal's clipboard."
}

// browse opens url in the owner's default browser; an opener still running
// when ctx ends is stopped.
func browse(ctx context.Context, url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.CommandContext(ctx, name, url)
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

func localHint(origin string) string {
	if strings.HasPrefix(origin, "http:") {
		return "; start the local stack with scripts/dev.sh"
	}
	return ""
}

// sharer serves one shared task to its recipients.
type sharer struct {
	task   *task
	id     string // the task's UUID
	secret []byte
	ctx    context.Context // ends sharing, every session and any run
	view   *view
	api    *webrtc.API

	slots    chan struct{} // one per session
	sessions sync.WaitGroup
	running  sync.Mutex // one run at a time per task (ADR 0001)
}

func newSharer(ctx context.Context, t *task, id string, secret []byte, v *view) *sharer {
	settings := webrtc.SettingEngine{}
	settings.DetachDataChannels()
	// Writes wait while the browser is not reading, so a stalled recipient holds
	// its session, not a growing queue of output.
	settings.EnableDataChannelBlockWrite(true)
	return &sharer{task: t, id: id, secret: secret, ctx: ctx, view: v,
		api: webrtc.NewAPI(webrtc.WithSettingEngine(settings)), slots: make(chan struct{}, maxSessions)}
}

// answer starts a session for a browser's offer and returns the answer with
// its ICE candidates. The session ends with sharing, when the peer connection
// fails, or when no authenticated peer arrives in time.
func (s *sharer) answer(offer string, ice []webrtc.ICEServer) (string, error) {
	select {
	case s.slots <- struct{}{}:
	default:
		return "", errors.New("too many recipients at once")
	}
	pc, err := s.api.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		<-s.slots
		return "", err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.sessions.Go(func() {
		<-ctx.Done()
		_ = pc.Close()
		<-s.slots
	})
	unauthenticated := time.AfterFunc(authTimeout, cancel)
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			cancel()
		}
	})
	var once sync.Once
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnOpen(func() {
			once.Do(func() {
				ch, err := dc.DetachWithDeadline()
				if err != nil {
					cancel()
					return
				}
				s.sessions.Go(func() {
					defer cancel()
					s.serve(ch, pc, unauthenticated)
				})
			})
		})
	})
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		cancel()
		return "", err
	}
	answer, err := pc.CreateAnswer(nil)
	if err == nil {
		err = pc.SetLocalDescription(answer)
	}
	if err != nil {
		cancel()
		return "", err
	}
	// Send what gathered in time; candidates arriving later would need trickle ICE.
	select {
	case <-gathered:
	case <-time.After(gatherTimeout):
	}
	return pc.LocalDescription().SDP, nil
}

// serve authenticates the browser, sends it the GUI and the task definition,
// then runs the task for it on request.
func (s *sharer) serve(ch datachannel.ReadWriteCloserDeadliner, pc *webrtc.PeerConnection, unauthenticated *time.Timer) {
	defer ch.Close()
	browserFP, err := fingerprint(pc.RemoteDescription().SDP)
	if err != nil {
		return
	}
	runnerFP, err := fingerprint(pc.LocalDescription().SDP)
	if err != nil || !s.authenticate(ch, browserFP, runnerFP) {
		s.view.notice("Turned away a connection that could not prove it holds the secret.")
		return
	}
	unauthenticated.Stop()
	s.view.connected(1)
	defer s.view.connected(-1)
	definition, err := json.Marshal(map[string]any{"definition": s.task.public()})
	if err != nil {
		return
	}
	if write(ch, frontend.GUI, false) != nil || write(ch, definition, true) != nil {
		return
	}
	for {
		var request struct {
			Values map[string]any `json:"values"`
		}
		if read(ch, &request) != nil || request.Values == nil {
			return
		}
		if s.execute(ch, request.Values) != nil {
			return
		}
	}
}

// execute runs the task once and sends its stages, progress, output, and outcome.
func (s *sharer) execute(ch datachannel.ReadWriteCloserDeadliner, values map[string]any) error {
	args, problems := s.task.check(values)
	if len(problems) > 0 {
		for _, name := range slices.Sorted(maps.Keys(problems)) {
			if problems[name] == dashProblem {
				s.view.notice(`Refused a value for ` + escape(name) + ` that starts with -, as the script could take it for an option; "unsafe": true on the input allows it.`)
			}
		}
		return send(ch, map[string]any{"error": &runError{Code: "invalid", Fields: problems}})
	}
	if !s.running.TryLock() {
		return send(ch, map[string]any{"error": &runError{Code: "failed", Message: "Another run is in progress on this machine. Try again in a moment."}})
	}
	// The timeout covers receiving the run's files as well as running it.
	ctx, cancel := context.WithTimeout(s.ctx, s.task.timeout())
	defer cancel()
	dir, err := s.receive(ctx, ch, values, args)
	defer os.RemoveAll(dir)
	if err == nil {
		err = send(ch, map[string]string{"stage": "running"})
	}
	if err != nil {
		s.running.Unlock()
		return err
	}
	s.view.started()
	// Progress goes out from its own goroutine, so a slow browser misses lines
	// instead of slowing the task; the output follows the last line sent.
	progress, delivered := make(chan string, 64), make(chan struct{})
	go func() {
		defer close(delivered)
		for line := range progress {
			_ = send(ch, map[string]string{"stage": "running", "message": line})
		}
	}()
	started, shown := time.Now(), 0
	stdout, err := s.task.run(ctx, values, args, func(line string) {
		s.view.stderr(line)
		if s.task.Progress && shown < maxProgress {
			shown++
			select {
			case progress <- truncate(line, 300):
			default:
			}
		}
	})
	// The owner sees the outcome before another run can start and take its place.
	took := time.Since(started).Round(time.Millisecond)
	var failure *runError
	if errors.As(err, &failure) || err == nil {
		s.view.finished(took, failure)
	} else {
		s.view.finished(took, &runError{Code: "failed", cause: err})
	}
	close(progress)
	s.running.Unlock() // delivery waits on the browser, not on other recipients' runs
	<-delivered
	if failure != nil {
		return send(ch, map[string]any{"error": failure})
	}
	if err != nil {
		return err
	}
	defer stdout.Close()
	return s.deliver(ch, stdout)
}

// deliver sends a run's output: its size, then its bytes, but more than a
// recipient takes without asking only once the recipient says to.
func (s *sharer) deliver(ch datachannel.ReadWriteCloserDeadliner, stdout *os.File) error {
	info, err := stdout.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if s.task.Output == nil {
		size = 0 // the page shows only that the run finished
	}
	if err := send(ch, map[string]int64{"size": size}); err != nil {
		return err
	}
	if size > unasked {
		var reply struct {
			Receive *bool `json:"receive"`
		}
		if err := read(ch, &reply); err != nil {
			return err
		}
		if reply.Receive == nil {
			return errors.New("expected whether to receive the result")
		}
		if !*reply.Receive {
			s.view.notice(fmt.Sprintf("A recipient chose not to receive a result of %.1f MB.", float64(size)/1e6))
			return send(ch, map[string]bool{"done": true})
		}
	}
	buf := make([]byte, outputChunk)
	for left := size; left > 0; {
		n, err := io.ReadFull(stdout, buf[:min(int64(len(buf)), left)])
		if err != nil {
			return err
		}
		if err := write(ch, buf[:n], false); err != nil {
			return err
		}
		left -= int64(n)
	}
	return send(ch, map[string]bool{"done": true})
}

// receive accepts the run and stores its files, which the browser then sends in
// input order as binary messages. Each keeps its name in a directory named by
// its input's position and its own, as names may differ only in case, under a
// new directory that the caller removes. Values and arguments get the files' paths.
func (s *sharer) receive(ctx context.Context, ch datachannel.ReadWriteCloserDeadliner, values map[string]any, args map[string][]string) (dir string, err error) {
	if err := send(ch, map[string]string{"stage": "accepted"}); err != nil {
		return "", err
	}
	var buf []byte
	for i, in := range s.task.Inputs {
		announced, ok := values[in.Name]
		if in.Type != "file" || !ok {
			continue
		}
		files, several := announced.([]any) // check made sure of the rest
		if !several {
			files = []any{announced}
		}
		if len(files) == 0 {
			continue // an empty list is no value
		}
		var paths []string
		for j, file := range files {
			if dir == "" {
				deadline, _ := ctx.Deadline()
				if err := ch.SetReadDeadline(deadline); err != nil {
					return "", err
				}
				defer ch.SetReadDeadline(time.Time{})
				if dir, err = os.MkdirTemp("", "runlink-run-"); err != nil {
					return "", err
				}
				buf = make([]byte, maxMessage)
			}
			path, err := store(ch, buf, filepath.Join(dir, strconv.Itoa(i), strconv.Itoa(j)), file.(map[string]any))
			if err != nil {
				return dir, fmt.Errorf("receiving a file: %w", err)
			}
			paths = append(paths, path)
		}
		values[in.Name], args[in.Name] = paths[0], paths
		if several {
			values[in.Name] = paths
		}
	}
	return dir, nil
}

// store writes the next file the browser sends into a new directory dir, under
// the name it announced, and returns its path.
func store(ch datachannel.ReadWriteCloserDeadliner, buf []byte, dir string, file map[string]any) (string, error) {
	path := filepath.Join(dir, file["name"].(string))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	for left := int64(file["size"].(float64)); left > 0 && err == nil; {
		n, text, readErr := ch.ReadDataChannel(buf)
		switch {
		case readErr != nil:
			err = readErr
		case text || int64(n) > left:
			err = errors.New("the browser sent more than the file it announced")
		default:
			_, err = f.Write(buf[:n])
			left -= int64(n)
		}
	}
	return path, errors.Join(err, f.Close())
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// authenticate runs the runner's side of the challenge-response in ADR 0009.
// The browser sends its nonce; the runner replies with its nonce and proof; the
// browser's proof must then match.
func (s *sharer) authenticate(ch datachannel.ReadWriteCloserDeadliner, browserFP, runnerFP string) bool {
	var hello struct {
		Nonce string `json:"nonce"`
	}
	if read(ch, &hello) != nil || !nonceFormat.MatchString(hello.Nonce) {
		return false
	}
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	runnerNonce := base64.RawURLEncoding.EncodeToString(nonce)
	proof := func(role string) string {
		return authProof(s.secret, role, s.id, browserFP, runnerFP, hello.Nonce, runnerNonce)
	}
	if send(ch, map[string]string{"nonce": runnerNonce, "proof": proof("runner")}) != nil {
		return false
	}
	var reply struct {
		Proof string `json:"proof"`
	}
	return read(ch, &reply) == nil && subtle.ConstantTimeCompare([]byte(reply.Proof), []byte(proof("browser"))) == 1
}

var nonceFormat = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// authProof is HMAC-SHA256 keyed by the link secret over one line per field.
// No field can contain a newline, so the transcript is unambiguous.
func authProof(secret []byte, role, task, browserFP, runnerFP, browserNonce, runnerNonce string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(strings.Join([]string{"runlink-auth-v1", role, task, browserFP, runnerFP, browserNonce, runnerNonce}, "\n")))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

var sdpFingerprint = regexp.MustCompile(`(?m)^a=fingerprint:sha-256 ((?:[0-9A-Fa-f]{2}:){31}[0-9A-Fa-f]{2})\r?$`)

// fingerprint returns the one SHA-256 certificate fingerprint a description
// declares, as lowercase hex. DTLS accepts a certificate that matches any
// declared fingerprint, so a description with any other fingerprint is refused.
func fingerprint(sdp string) (string, error) {
	matches := sdpFingerprint.FindAllStringSubmatch(sdp, -1)
	if len(matches) == 0 || len(matches) != strings.Count(sdp, "a=fingerprint:") {
		return "", errors.New("the description needs exactly one SHA-256 fingerprint")
	}
	fp := strings.ToLower(strings.ReplaceAll(matches[0][1], ":", ""))
	for _, m := range matches {
		if strings.ToLower(strings.ReplaceAll(m[1], ":", "")) != fp {
			return "", errors.New("the description declares different fingerprints")
		}
	}
	return fp, nil
}

// read decodes the next data-channel message, which must be a JSON text message.
func read(ch datachannel.ReadWriteCloserDeadliner, v any) error {
	buf := make([]byte, maxMessage)
	n, text, err := ch.ReadDataChannel(buf)
	if err != nil {
		return err
	}
	if !text {
		return errors.New("expected a text message")
	}
	return json.Unmarshal(buf[:n], v)
}

func send(ch datachannel.ReadWriteCloserDeadliner, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return write(ch, data, true)
}

// write sends one message, or closes the channel when the browser does not take
// it in time, so later writes fail at once.
func write(ch datachannel.ReadWriteCloserDeadliner, data []byte, text bool) error {
	if err := ch.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	if _, err := ch.WriteDataChannel(data, text); err != nil {
		_ = ch.Close()
		return err
	}
	return nil
}
