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
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v4"

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
// recipients until ctx ends. Sessions already open outlive the service connection.
func share(ctx context.Context, origin string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("share", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	separate := flags.Bool("separate-secret", false, "print the secret apart from the link")
	const usage = "usage: runlink share [--separate-secret] <task.json or executable>; see docs/tasks.md"
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(stdout, usage)
		return 0
	} else if err != nil || flags.NArg() != 1 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	t, err := loadTask(flags.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, "runlink:", err)
		return 2
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// Connecting and registering get ten seconds together.
	startCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(startCtx, origin+"/api/v1/share", &websocket.DialOptions{HTTPClient: client})
	if err != nil {
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
		switch {
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
	if *separate {
		fmt.Fprintf(stdout, "Sharing %q. Send the link and the secret separately:\n\n  Link    %s\n  Secret  %s\n\n", t.Title, link, key)
	} else {
		fmt.Fprintf(stdout, "Sharing %q. Send this link:\n\n  %s#%s\n\n", t.Title, link, key)
	}
	fmt.Fprintln(stdout, "Anyone with it can run the task on this machine while this command runs. Ctrl-C stops sharing.")

	s := newSharer(ctx, t, registered.Task, secret, stderr)
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
				s.log.Print("Could not answer a recipient: ", err)
			}
			// An empty answer tells the browser at once that this machine won't connect.
			_ = wsjson.Write(ctx, conn, map[string]string{"session": offer.Session, "sdp": answer})
		})
	}
	if ctx.Err() == nil {
		s.log.Print("Lost the connection to ", origin, ". New recipients can't connect; open sessions continue until Ctrl-C.")
		<-ctx.Done()
	}
	return 0
}

// terminal writes log entries with their control characters escaped, so a
// script's stderr, which may echo recipient input, cannot drive the owner's
// terminal or fake a line.
type terminal struct{ w io.Writer }

func (t terminal) Write(p []byte) (int, error) {
	var b strings.Builder
	for _, r := range strings.TrimSuffix(string(p), "\n") {
		if unicode.IsControl(r) && r != '\t' {
			fmt.Fprintf(&b, "\\x%02x", r)
		} else {
			b.WriteRune(r) // invalid UTF-8 arrives as U+FFFD
		}
	}
	b.WriteByte('\n')
	_, err := io.WriteString(t.w, b.String())
	return len(p), err
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
	log    *log.Logger
	api    *webrtc.API

	slots    chan struct{} // one per session
	sessions sync.WaitGroup
	running  sync.Mutex // one run at a time per task (ADR 0001)
}

func newSharer(ctx context.Context, t *task, id string, secret []byte, events io.Writer) *sharer {
	settings := webrtc.SettingEngine{}
	settings.DetachDataChannels()
	// Writes wait while the browser is not reading, so a stalled recipient holds
	// its session, not a growing queue of output.
	settings.EnableDataChannelBlockWrite(true)
	return &sharer{task: t, id: id, secret: secret, ctx: ctx, log: log.New(terminal{events}, "", log.Ltime),
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
		s.log.Print("Turned away a connection that could not prove it holds the secret.")
		return
	}
	unauthenticated.Stop()
	s.log.Print("A recipient connected.")
	defer s.log.Print("A recipient disconnected.")
	definition, err := json.Marshal(map[string]any{"definition": s.task.definition})
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
	s.log.Print("Run started.")
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
		s.log.Print("  ", line)
		if s.task.Progress && shown < maxProgress {
			shown++
			select {
			case progress <- truncate(line, 300):
			default:
			}
		}
	})
	close(progress)
	s.running.Unlock() // delivery waits on the browser, not on other recipients' runs
	<-delivered
	took := time.Since(started).Round(time.Millisecond)
	var failure *runError
	if errors.As(err, &failure) {
		s.log.Printf("Run %s after %v. %v", failure.Code, took, failure)
		return send(ch, map[string]any{"error": failure})
	}
	if err != nil {
		return err
	}
	s.log.Printf("Run finished in %v.", took)
	for len(stdout) > 0 {
		chunk := stdout[:min(outputChunk, len(stdout))]
		if err := write(ch, chunk, false); err != nil {
			return err
		}
		stdout = stdout[len(chunk):]
	}
	return send(ch, map[string]bool{"done": true})
}

// receive accepts the run and stores its files, which the browser then sends in
// input order as binary messages. Each keeps its name in a directory named by
// its input's position, as input names may differ only in case, under a new
// directory that the caller removes. Values and arguments get the files' paths.
func (s *sharer) receive(ctx context.Context, ch datachannel.ReadWriteCloserDeadliner, values map[string]any, args map[string]string) (dir string, err error) {
	if err := send(ch, map[string]string{"stage": "accepted"}); err != nil {
		return "", err
	}
	var buf []byte
	for i, in := range s.task.Inputs {
		file, ok := values[in.Name].(map[string]any)
		if in.Type != "file" || !ok {
			continue
		}
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
		path := filepath.Join(dir, strconv.Itoa(i), file["name"].(string))
		if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
			return dir, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return dir, err
		}
		for left := int(file["size"].(float64)); left > 0 && err == nil; {
			n, text, readErr := ch.ReadDataChannel(buf)
			switch {
			case readErr != nil:
				err = readErr
			case text || n > left:
				err = errors.New("the browser sent more than the file it announced")
			default:
				_, err = f.Write(buf[:n])
				left -= n
			}
		}
		if err = errors.Join(err, f.Close()); err != nil {
			return dir, fmt.Errorf("receiving a file: %w", err)
		}
		values[in.Name], args[in.Name] = path, path
	}
	return dir, nil
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
