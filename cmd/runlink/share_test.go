package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v4"

	"runlink/internal/devstack"
	"runlink/internal/frontend"
)

// browser is a test peer that speaks the loader's side of the protocol.
type browser struct {
	pc *webrtc.PeerConnection
	ch datachannel.ReadWriteCloserDeadliner
}

// offerer creates a peer with the loader's data channel and its complete offer.
// The peer closes when the test ends or after 60 seconds, so reads cannot hang.
func offerer(t *testing.T, ice []webrtc.ICEServer) (*webrtc.PeerConnection, string, <-chan datachannel.ReadWriteCloserDeadliner) {
	t.Helper()
	settings := webrtc.SettingEngine{}
	settings.DetachDataChannels()
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	time.AfterFunc(time.Minute, func() { _ = pc.Close() })
	dc, err := pc.CreateDataChannel("runlink", nil)
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan datachannel.ReadWriteCloserDeadliner, 1)
	dc.OnOpen(func() {
		if ch, err := dc.DetachWithDeadline(); err == nil {
			opened <- ch
		}
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	return pc, pc.LocalDescription().SDP, opened
}

func opened(t *testing.T, pc *webrtc.PeerConnection, answer string, ch <-chan datachannel.ReadWriteCloserDeadliner) *browser {
	t.Helper()
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-ch:
		return &browser{pc, c}
	case <-time.After(15 * time.Second):
		t.Fatal("the data channel did not open")
		return nil
	}
}

// viaService connects through the service as the loader does, offering host
// candidates only so the session cannot depend on the stack's TURN server.
func viaService(t *testing.T, origin, task string) *browser {
	t.Helper()
	conn, _, err := websocket.Dial(t.Context(), origin+"/api/v1/connect/"+task, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var ice struct {
		ICE []webrtc.ICEServer `json:"ice"`
	}
	if err := wsjson.Read(t.Context(), conn, &ice); err != nil || len(ice.ICE) != 1 || len(ice.ICE[0].URLs) != 3 || ice.ICE[0].Username == "" {
		t.Fatalf("ICE servers %+v: %v", ice, err)
	}
	pc, offer, ch := offerer(t, nil)
	if err := wsjson.Write(t.Context(), conn, map[string]string{"sdp": offer}); err != nil {
		t.Fatal(err)
	}
	var answer struct {
		SDP string `json:"sdp"`
	}
	if err := wsjson.Read(t.Context(), conn, &answer); err != nil || answer.SDP == "" {
		t.Fatalf("answer: %v", err)
	}
	return opened(t, pc, answer.SDP, ch)
}

// direct connects a browser to s without signaling.
func direct(t *testing.T, s *sharer) *browser {
	t.Helper()
	pc, offer, ch := offerer(t, nil)
	answer, err := s.answer(offer, nil)
	if err != nil {
		t.Fatal(err)
	}
	return opened(t, pc, answer, ch)
}

func key() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}

func encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// authenticate runs the browser's side with secret. Unless claimedRunnerFP is
// empty, the browser's proof covers that fingerprint instead of the runner's,
// as when an attacker relays the connection. It returns the nonce and proof sent,
// and whether the runner's proof verified.
func (b *browser) authenticate(t *testing.T, secret []byte, task, claimedRunnerFP, nonce string) (string, string, bool) {
	t.Helper()
	if nonce == "" {
		nonce = encode(key())
	}
	if err := send(b.ch, map[string]string{"nonce": nonce}); err != nil {
		t.Fatal(err)
	}
	var hello struct{ Nonce, Proof string }
	if err := read(b.ch, &hello); err != nil {
		t.Fatal(err)
	}
	browserFP, err := fingerprint(b.pc.LocalDescription().SDP)
	if err != nil {
		t.Fatal(err)
	}
	runnerFP, err := fingerprint(b.pc.RemoteDescription().SDP)
	if err != nil {
		t.Fatal(err)
	}
	verified := hello.Proof == authProof(secret, "runner", task, browserFP, runnerFP, nonce, hello.Nonce)
	if claimedRunnerFP != "" {
		runnerFP = claimedRunnerFP
	}
	proof := authProof(secret, "browser", task, browserFP, runnerFP, nonce, hello.Nonce)
	if err := send(b.ch, map[string]string{"proof": proof}); err != nil {
		t.Fatal(err)
	}
	return nonce, proof, verified
}

// next reads one message.
func (b *browser) next() ([]byte, bool, error) {
	buf := make([]byte, 1<<20)
	n, text, err := b.ch.ReadDataChannel(buf)
	return buf[:n], text, err
}

// accepted reports whether the runner sent the approved GUI and the definition.
func (b *browser) accepted(t *testing.T) (definition, bool) {
	t.Helper()
	gui, text, err := b.next()
	if err != nil {
		return definition{}, false
	}
	if text || !bytes.Equal(gui, frontend.GUI) {
		t.Fatal("the first message after authentication is not the GUI")
	}
	var message struct{ Definition definition }
	data, _, err := b.next()
	if err != nil || json.Unmarshal(data, &message) != nil {
		t.Fatalf("definition: %v", err)
	}
	return message.Definition, true
}

// run sends values and collects the stages, progress, output, and outcome. Once
// the run is accepted, it sends the files' bytes.
func (b *browser) run(t *testing.T, values map[string]any, files ...[]byte) (stages []string, output string, failure *runError) {
	t.Helper()
	if err := send(b.ch, map[string]any{"values": values}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for {
		data, text, err := b.next()
		if err != nil {
			t.Fatal(err)
		}
		if !text {
			out.Write(data)
			continue
		}
		var message struct {
			Stage, Message string
			Error          *runError
			Done           bool
		}
		if err := json.Unmarshal(data, &message); err != nil {
			t.Fatal(err)
		}
		switch {
		case message.Error != nil:
			return stages, out.String(), message.Error
		case message.Done:
			return stages, out.String(), nil
		case message.Message != "":
			stages = append(stages, message.Stage+": "+message.Message)
		default:
			stages = append(stages, message.Stage)
		}
		if message.Stage == "accepted" {
			for _, file := range files {
				for chunk := range slices.Chunk(file, outputChunk) {
					if err := write(b.ch, chunk, false); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
}

// writeTask writes a script and a task file running it to a new directory.
func writeTask(t *testing.T, script, file string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "task.sh"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "task.json")
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const echoTask = `{
  "title": "Echo",
  "command": ["./task.sh", "{name}", "--count={count}"],
  "inputs": [
    {"name": "name", "type": "text", "required": true},
    {"name": "count", "type": "number", "min": 1, "max": 3, "step": 1},
    {"name": "loud", "type": "boolean"}
  ],
  "output": {"kind": "text"},
  "progress": true
}`

// lockedBuffer collects output written from several goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var linkLine = regexp.MustCompile(`(?m)^  (http://\S+)/([0-9a-f-]{36})#([A-Za-z0-9_-]{43})$`)

// The whole path through the local stack: share registers and prints the link, a
// browser signals through A and B, authenticates, gets the GUI and definition,
// and runs the task. The session then survives the whole stack stopping.
func TestShareEndToEnd(t *testing.T) {
	stack, err := devstack.Start(t.Context(), devstack.Config{
		Frontend: "127.0.0.1:0", App: "127.0.0.1:0", TURN: "127.0.0.1:0", StateDir: t.TempDir(),
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Stop() })
	path := writeTask(t, `echo "starting $1" >&2; cat; echo; echo "$1 $2"`, echoTask)

	ctx, cancel := context.WithCancel(t.Context())
	var stdout, stderr lockedBuffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"share", path}, env(stack.Frontend), &stdout, &stderr) }()
	var link []string
	for deadline := time.Now().Add(10 * time.Second); link == nil; time.Sleep(20 * time.Millisecond) {
		link = linkLine.FindStringSubmatch(stdout.String())
		if time.Now().After(deadline) {
			t.Fatalf("no link: %q %q", stdout.String(), stderr.String())
		}
	}
	if link[1] != stack.Frontend {
		t.Fatalf("link origin %q", link[1])
	}
	task, secret := link[2], link[3]
	raw, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}

	b := viaService(t, stack.Frontend, task)
	if _, _, verified := b.authenticate(t, raw, task, "", ""); !verified {
		t.Fatal("the runner's proof does not verify")
	}
	def, ok := b.accepted(t)
	if !ok || def.Title != "Echo" || len(def.Inputs) != 3 {
		t.Fatalf("definition %+v", def)
	}
	stages, output, failure := b.run(t, map[string]any{"name": "Ada {count}", "count": 2.0})
	want := []string{"accepted", "running", "running: starting Ada {count}"}
	if failure != nil || strings.Join(stages, "|") != strings.Join(want, "|") ||
		output != `{"count":2,"name":"Ada {count}"}`+"\nAda {count} --count=2\n" {
		t.Fatalf("stages %q, output %q, failure %+v", stages, output, failure)
	}
	_, _, failure = b.run(t, map[string]any{"name": "-rf", "count": 9.0, "extra": true})
	if failure == nil || failure.Code != "invalid" || len(failure.Fields) != 3 {
		t.Fatalf("invalid input: %+v", failure)
	}

	// A, B, and the TURN server stop; the established session keeps working.
	if err := stack.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, output, failure = b.run(t, map[string]any{"name": "Bo", "count": 1.0}); failure != nil || !strings.HasSuffix(output, "Bo --count=1\n") {
		t.Fatalf("after the stack stopped: %q %+v", output, failure)
	}
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(stderr.String(), "Lost the connection"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the owner was not told about the lost service:\n%s", stderr.String())
		}
	}
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	events := stderr.String()
	for _, event := range []string{"A recipient connected.", "Run started.", "  starting Ada {count}", "Run finished in "} {
		if !strings.Contains(events, event) {
			t.Fatalf("owner events lack %q:\n%s", event, events)
		}
	}
	if strings.Contains(events, secret) {
		t.Fatal("the secret reached the event log")
	}
}

// ADR 0009: only a peer proving the secret over this connection's fingerprints
// and fresh nonces gets the task.
func TestAuthenticationRejectsWrongSecretSubstitutionAndReplay(t *testing.T) {
	path := writeTask(t, "echo ok", `{"title": "T", "command": ["./task.sh"]}`)
	tk, err := loadTask(path)
	if err != nil {
		t.Fatal(err)
	}
	const id = "12345678-1234-4234-8234-123456789abc"
	secret := key()
	var events lockedBuffer
	s := newSharer(t.Context(), tk, id, secret, &events)

	b := direct(t, s)
	nonce, proof, verified := b.authenticate(t, secret, id, "", "")
	if _, ok := b.accepted(t); !ok || !verified {
		t.Fatal("the right secret was refused")
	}

	cases := map[string]func(b *browser){
		"wrong secret": func(b *browser) {
			// The browser would stop here: the runner's proof is for another secret.
			if _, _, verified := b.authenticate(t, key(), id, "", ""); verified {
				t.Error("the runner's proof verified under a wrong secret")
			}
		},
		"other task":    func(b *browser) { b.authenticate(t, secret, "12345678-1234-4234-8234-123456789abd", "", "") },
		"relayed peers": func(b *browser) { b.authenticate(t, secret, id, strings.Repeat("ab", 32), "") },
		"replayed proof": func(b *browser) {
			if err := send(b.ch, map[string]string{"nonce": nonce}); err != nil {
				t.Fatal(err)
			}
			var hello struct{ Nonce, Proof string }
			if err := read(b.ch, &hello); err != nil {
				t.Fatal(err)
			}
			if err := send(b.ch, map[string]string{"proof": proof}); err != nil {
				t.Fatal(err)
			}
		},
		"no proof": func(b *browser) {
			if err := send(b.ch, map[string]string{"nonce": "short"}); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, attack := range cases {
		t.Run(name, func(t *testing.T) {
			b := direct(t, s)
			attack(b)
			if _, ok := b.accepted(t); ok {
				t.Fatal("the task was sent")
			}
		})
	}
	if strings.Count(events.String(), "Turned away") != len(cases) {
		t.Fatalf("owner events:\n%s", events.String())
	}
}

// A recipient that stops reading holds only its own session: delivery waits on
// it without queueing output or keeping other recipients from running the task.
func TestStalledRecipientBlocksOnlyItself(t *testing.T) {
	path := writeTask(t, `if [ "$1" = big ]; then head -c 1000000 /dev/zero; else echo small; fi`,
		`{"title": "T", "command": ["./task.sh", "{size}"], "inputs": [{"name": "size", "type": "text"}], "output": {"kind": "text"}}`)
	tk, err := loadTask(path)
	if err != nil {
		t.Fatal(err)
	}
	const id = "12345678-1234-4234-8234-123456789abc"
	secret := key()
	s := newSharer(t.Context(), tk, id, secret, io.Discard)
	stalled := direct(t, s)
	stalled.authenticate(t, secret, id, "", "")
	if _, ok := stalled.accepted(t); !ok {
		t.Fatal("refused")
	}
	for range 4 {
		if err := send(stalled.ch, map[string]any{"values": map[string]any{"size": "big"}}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(time.Second) // the runner fills the stalled browser's receive window
	other := direct(t, s)
	other.authenticate(t, secret, id, "", "")
	if _, ok := other.accepted(t); !ok {
		t.Fatal("refused")
	}
	if _, output, failure := other.run(t, map[string]any{"size": "small"}); failure != nil || output != "small\n" {
		t.Fatalf("%q %+v", output, failure)
	}
}

// Files arrive after acceptance and reach the command as paths in a private
// directory, deleted with everything in it once the run ends.
func TestFileInputs(t *testing.T) {
	path := writeTask(t, `cat "$1"; printf '\n%s\n' "$1"; ls -ld "$1" | cut -c1-10; ls -ld "${1%/*/*}" | cut -c1-10; cat`, `{"title": "T", "timeout": 1,
		"command": ["./task.sh", "{doc}"], "output": {"kind": "text"},
		"inputs": [{"name": "doc", "type": "file", "required": true, "maxBytes": 100000}, {"name": "note", "type": "file"}]}`)
	tk, err := loadTask(path)
	if err != nil {
		t.Fatal(err)
	}
	const id = "12345678-1234-4234-8234-123456789abc"
	secret := key()
	s := newSharer(t.Context(), tk, id, secret, io.Discard)
	connect := func() *browser {
		b := direct(t, s)
		b.authenticate(t, secret, id, "", "")
		def, ok := b.accepted(t)
		if !ok || def.Inputs[1].MaxBytes != maxUpload {
			t.Fatalf("definition %+v", def)
		}
		return b
	}
	b := connect()
	content := bytes.Repeat([]byte("line\n"), 10000)
	doc := map[string]any{"name": "-q3 report.csv", "size": float64(len(content))}
	_, output, failure := b.run(t, map[string]any{"doc": doc}, content)
	stored, rest, _ := strings.Cut(strings.TrimPrefix(output, string(content)+"\n"), "\n")
	if failure != nil || !strings.HasPrefix(output, string(content)) || !strings.HasSuffix(stored, "/0/-q3 report.csv") ||
		rest != "-rw-------\ndrwx------\n"+`{"doc":"`+stored+`"}` {
		t.Fatalf("%q %+v", output[len(content):], failure)
	}
	if _, err := os.Stat(filepath.Dir(filepath.Dir(stored))); !os.IsNotExist(err) {
		t.Fatalf("the run's files outlived it: %v", err)
	}

	// A browser sending more than it announced loses its session.
	if err := send(b.ch, map[string]any{"values": map[string]any{"doc": map[string]any{"name": "a", "size": 3.0}}}); err != nil {
		t.Fatal(err)
	}
	if data, _, err := b.next(); err != nil || !strings.Contains(string(data), "accepted") {
		t.Fatalf("%q %v", data, err)
	}
	if err := write(b.ch, []byte("abcde"), false); err != nil {
		t.Fatal(err)
	}
	if data, _, err := b.next(); err == nil {
		t.Fatalf("the session went on: %q", data)
	}

	// A browser that never sends its file holds the run only until the timeout.
	stalled := connect()
	if err := send(stalled.ch, map[string]any{"values": map[string]any{"doc": map[string]any{"name": "a", "size": 3.0}}}); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	for {
		if _, _, err := stalled.next(); err != nil {
			break
		}
	}
	if took := time.Since(began); took > 3*time.Second {
		t.Fatalf("the stalled upload held the run for %v", took)
	}
	if _, output, failure := connect().run(t, map[string]any{"doc": map[string]any{"name": "a", "size": 1.0}}, []byte("x")); failure != nil || !strings.HasPrefix(output, "x\n") {
		t.Fatalf("after a stalled upload: %q %+v", output, failure)
	}

	// Files of inputs whose names differ only in case stay apart on a
	// case-insensitive file system, as macOS has by default.
	if tk, err = loadTask(writeTask(t, `cat "$1" "$2"`, `{"title": "T", "command": ["./task.sh", "{Doc}", "{doc}"],
		"inputs": [{"name": "Doc", "type": "file"}, {"name": "doc", "type": "file"}], "output": {"kind": "text"}}`)); err != nil {
		t.Fatal(err)
	}
	s = newSharer(t.Context(), tk, id, secret, io.Discard)
	same := map[string]any{"name": "a.txt", "size": 1.0}
	if _, output, failure := connect().run(t, map[string]any{"Doc": same, "doc": same}, []byte("A"), []byte("b")); failure != nil || output != "Ab" {
		t.Fatalf("case-variant inputs: %q %+v", output, failure)
	}
}

// A script's stderr can echo recipient input; it reaches the owner's terminal
// with control characters escaped, one entry per line.
func TestOwnerLogEscapesControlCharacters(t *testing.T) {
	var events bytes.Buffer
	s := newSharer(t.Context(), &task{}, "", nil, &events)
	s.log.Print("  Choosing words for \x1b]52;c;cHduZWQ=\x07\u009b2J\x9b\nRun finished\tin 1s.")
	got := events.String()
	if strings.ContainsAny(got, "\x1b\x07\u009b") || strings.Contains(got, "\x9b") || strings.Count(got, "\n") != 1 ||
		!strings.HasSuffix(got, `Choosing words for \x1b]52;c;cHduZWQ=\x07\x9b2J`+"\ufffd"+`\x0aRun finished`+"\tin 1s.\n") {
		t.Fatalf("%q", got)
	}
}

// A write the stalled browser does not take ends its session at the deadline.
func TestStalledRecipientIsDisconnected(t *testing.T) {
	path := writeTask(t, "head -c 1000000 /dev/zero", `{"title": "T", "command": ["./task.sh"], "output": {"kind": "text"}}`)
	tk, err := loadTask(path)
	if err != nil {
		t.Fatal(err)
	}
	const id = "12345678-1234-4234-8234-123456789abc"
	secret := key()
	var events lockedBuffer
	s := newSharer(t.Context(), tk, id, secret, &events)
	b := direct(t, s)
	b.authenticate(t, secret, id, "", "")
	if _, ok := b.accepted(t); !ok {
		t.Fatal("refused")
	}
	for range 3 {
		if err := send(b.ch, map[string]any{"values": map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	for deadline := time.Now().Add(writeTimeout + 10*time.Second); !strings.Contains(events.String(), "A recipient disconnected."); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the stalled session stayed open:\n%s", events.String())
		}
	}
}

// slowChannel stands in for a browser that takes one message a second.
type slowChannel struct {
	datachannel.ReadWriteCloserDeadliner
}

func (slowChannel) SetWriteDeadline(time.Time) error { return nil }
func (slowChannel) WriteDataChannel(p []byte, _ bool) (int, error) {
	time.Sleep(time.Second)
	return len(p), nil
}

// A slow browser misses progress lines instead of slowing the task down: here
// stderr outgrows the pipe buffer, so blocking on delivery would stall the script
// until its five-second timeout.
func TestSlowRecipientDoesNotSlowTheRun(t *testing.T) {
	path := writeTask(t, `line=$(head -c 4000 /dev/zero | tr '\0' x); i=0; while [ $i -lt 100 ]; do echo "$line" >&2; i=$((i+1)); done`,
		`{"title": "T", "command": ["./task.sh"], "progress": true, "timeout": 5}`)
	tk, err := loadTask(path)
	if err != nil {
		t.Fatal(err)
	}
	s := newSharer(t.Context(), tk, "12345678-1234-4234-8234-123456789abc", key(), io.Discard)
	began := time.Now()
	go func() { _ = s.execute(slowChannel{}, map[string]any{}) }()
	time.Sleep(100 * time.Millisecond) // execute takes the run lock
	// Two stage messages take two seconds; the run itself takes milliseconds.
	for !s.running.TryLock() {
		if time.Since(began) > 4*time.Second {
			t.Fatal("progress delivery held the run")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFingerprint(t *testing.T) {
	fp := strings.Repeat("AB:", 31) + "CD"
	want := strings.Repeat("ab", 31) + "cd"
	for sdp, ok := range map[string]bool{
		"a=fingerprint:sha-256 " + fp + "\r\n":                                     true,
		"a=fingerprint:sha-256 " + fp + "\r\na=fingerprint:sha-256 " + fp + "\r\n": true,
		"":                              false,
		"a=fingerprint:sha-1 AB:CD\r\n": false,
		"a=fingerprint:sha-256 " + fp + "\r\na=fingerprint:sha-1 AB:CD\r\n":                     false,
		"a=fingerprint:sha-256 " + fp + "\r\na=fingerprint:sha-256 " + fp[:len(fp)-1] + "E\r\n": false,
		"a=fingerprint:sha-256 " + fp[:len(fp)-3] + "\r\n":                                      false,
	} {
		got, err := fingerprint(sdp)
		if ok != (err == nil) || ok && got != want {
			t.Errorf("%q: %q %v", sdp, got, err)
		}
	}
}

func TestShareReportsServiceProblems(t *testing.T) {
	path := writeTask(t, "echo ok", `{"title": "T", "command": ["./task.sh"]}`)
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{"share", path}, env("http://127.0.0.1:1"), &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "start the local stack") {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if code := run(t.Context(), []string{"share", "--help"}, env(""), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "--separate-secret") {
		t.Fatalf("help: exit %d, %q", code, stdout.String())
	}
	// A service that upgrades but never registers the task fails within the startup deadline.
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err == nil {
			_, _, _ = conn.Read(r.Context())
		}
	}))
	defer silent.Close()
	stderr.Reset()
	if code := run(t.Context(), []string{"share", path}, env(silent.URL), io.Discard, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "did not register the task in time") {
		t.Fatalf("silent service: exit %d: %s", code, stderr.String())
	}
	for _, args := range [][]string{{"share"}, {"share", "a", "b"}, {"share", "--unknown", path}, {"share", filepath.Join(t.TempDir(), "missing.json")}} {
		stderr.Reset()
		if code := run(t.Context(), args, env("http://127.0.0.1:1"), io.Discard, &stderr); code != 2 {
			t.Errorf("%q: exit %d: %s", args, code, stderr.String())
		}
	}
}

// Without a clipboard tool, or with a hung one, the link goes to the terminal
// through OSC 52; with one, the tool gets it on stdin. Enter's opener gets the
// full link. /dev/null is no terminal, so redirected output copies nothing.
func TestClipboardAndBrowse(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if isTerminal(null) || isTerminal(&bytes.Buffer{}) {
		t.Fatal("/dev/null or a buffer is a terminal")
	}
	copier, opener := "xclip", "xdg-open"
	if runtime.GOOS == "darwin" {
		copier, opener = "pbcopy", "open"
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	var term bytes.Buffer
	if got := clipboard(t.Context(), &term, "link"); got != "Sent to the terminal's clipboard." || term.String() != "\x1b]52;c;bGluaw==\a" {
		t.Fatalf("without a tool: %q, terminal %q", got, term.String())
	}
	if err := browse("http://x/#k"); err == nil {
		t.Fatal("browse succeeded without an opener")
	}

	if err := os.WriteFile(filepath.Join(dir, copier), []byte("#!/bin/sh\nexec /bin/sleep 60"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if got := clipboard(ctx, &term, "link"); got != "Sent to the terminal's clipboard." || ctx.Err() == nil {
		t.Fatalf("with a hung %s: %q", copier, got)
	}

	got := filepath.Join(dir, "got")
	for name, script := range map[string]string{copier: "exec /bin/cat >" + got, opener: `printf %s "$1" >` + got + ".url"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	term.Reset()
	if msg := clipboard(t.Context(), &term, "link"); msg != "Copied to the clipboard." || term.Len() != 0 {
		t.Fatalf("with %s: %q, terminal %q", copier, msg, term.String())
	}
	if err := browse("http://x/#k"); err != nil {
		t.Fatal(err)
	}
	copied, _ := os.ReadFile(got)
	opened, _ := os.ReadFile(got + ".url")
	if string(copied) != "link" || string(opened) != "http://x/#k" {
		t.Fatalf("copied %q, opened %q", copied, opened)
	}
}
