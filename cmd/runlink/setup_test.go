package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"runlink/internal/devstack"
)

// console runs the CLI on a pseudo-terminal, as from an owner's terminal:
// keys go in, and the screen collects what it shows.
type console struct {
	ctl, tty *os.File
	out      lockedBuffer
	errs     lockedBuffer // stderr, which stays apart from the terminal
}

func newConsole(t *testing.T) *console {
	t.Helper()
	ctl, tty, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	c := &console{ctl: ctl, tty: tty}
	go func() { _, _ = io.Copy(&c.out, ctl) }()
	t.Cleanup(func() {
		tty.Close()
		ctl.Close()
	})
	return c
}

func (c *console) share(ctx context.Context, env func(string) string, args ...string) <-chan int {
	done := make(chan int, 1)
	go func() { done <- run(ctx, append([]string{"share"}, args...), env, c.tty, c.tty, &c.errs) }()
	return done
}

var sequence = regexp.MustCompile(`\x1b(\[[0-9;?]*[A-Za-z]|\][^\a]*\a)`)

// screen is everything shown so far, without escape sequences.
func (c *console) screen() string { return sequence.ReplaceAllString(c.out.String(), "") }

func (c *console) await(t *testing.T, text string) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); !strings.Contains(c.screen(), text); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the terminal never showed %q:\n%s", text, c.screen())
		}
	}
}

func (c *console) press(t *testing.T, keys string) {
	t.Helper()
	if _, err := c.ctl.WriteString(keys); err != nil {
		t.Fatal(err)
	}
}

func exited(t *testing.T, done <-chan int, want int) {
	t.Helper()
	select {
	case code := <-done:
		if code != want {
			t.Fatalf("exit %d, want %d", code, want)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("share did not exit")
	}
}

// terminalEnv is a UTF-8 color terminal's environment, with overrides as pairs.
func terminalEnv(service string, pairs ...string) func(string) string {
	vars := map[string]string{"RUNLINK_SERVICE": service, "TERM": "xterm-256color", "LANG": "C.UTF-8"}
	for i := 0; i < len(pairs); i += 2 {
		vars[pairs[i]] = pairs[i+1]
	}
	return func(key string) string { return vars[key] }
}

// startStack runs the local stack in-process on free ports.
func startStack(t *testing.T) *devstack.Stack {
	t.Helper()
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil { // private whatever the umask, as the stack requires
		t.Fatal(err)
	}
	stack, err := devstack.Start(t.Context(), devstack.Config{
		Frontend: "127.0.0.1:0", App: "127.0.0.1:0", TURN: "127.0.0.1:0", StateDir: state,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Stop() })
	return stack
}

// stubClipboard keeps sharing at a terminal from replacing the owner's clipboard.
func stubClipboard(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"pbcopy", "wl-copy"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexec cat >/dev/null\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeScript(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

var shownLink = regexp.MustCompile(`(http://[^/\s]+)/([0-9a-f-]{36})#([A-Za-z0-9_-]{43})`)

// recipient connects to the link on screen as the loader does and returns the
// session and the form it got.
func recipient(t *testing.T, screen string) (*browser, string) {
	t.Helper()
	m := shownLink.FindStringSubmatch(screen)
	if m == nil {
		t.Fatalf("no link on the screen:\n%s", screen)
	}
	secret, err := base64.RawURLEncoding.DecodeString(m[3])
	if err != nil {
		t.Fatal(err)
	}
	b := viaService(t, m[1], m[2])
	if _, _, verified := b.authenticate(t, secret, m[2], "", ""); !verified {
		t.Fatal("the runner's proof does not verify")
	}
	def, ok := b.accepted(t)
	if !ok {
		t.Fatal("the task was refused")
	}
	return b, form(t, def)
}

func form(t *testing.T, def shown) string {
	t.Helper()
	data, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The first share of a script asks for its form and stores the answers beside
// it, and the next share loads them without asking: recipients get the same form.
func TestGuidedFirstShare(t *testing.T) {
	stack := startStack(t)
	stubClipboard(t)
	script := writeScript(t, "greet.sh", `echo "choosing words for $1" >&2; echo "Hi $1, $2"`)
	stored := filepath.Join(filepath.Dir(script), "greet.sh.runlink.json")
	env := terminalEnv(stack.Frontend)

	c := newConsole(t)
	done := c.share(t.Context(), env, script)
	c.await(t, "greet.sh has no task file yet")
	// A title replacing the suggestion, no description, an owner, then a text
	// input and a choice whose options are first left out.
	c.press(t, "Greet someone\r\rDana\r"+"y"+"Your name\rname\r\r\r\r\r"+"y"+"Tone\r\rc\r\r")
	c.await(t, "Options are needed for a choice.")
	// One value, optional, the second argument; no more inputs or variables;
	// text output with a label; the default timeout; progress.
	c.press(t, "warm, formal\r"+"\r"+"n"+"\r"+"\r"+"\r"+"\r"+"\ra greeting\r"+"\r"+"y")
	c.await(t, "Share now?")
	for _, shown := range []string{
		"Inputs        name         text, required, argument · Your name",
		"tone         choice of warm, formal, argument · Tone",
		"( None | warm | formal )  optional",
		"[ Run ]  You'll get a greeting · on Dana's machine",
		"Runs  ./greet.sh {name} [{tone}]",
		"Stdin the values as JSON",
		"Saves " + stored + "; change it by hand or with runlink share --edit " + script,
	} {
		c.await(t, shown)
	}
	if _, err := os.Stat(stored); !os.IsNotExist(err) {
		t.Fatalf("saved before the owner confirmed: %v", err)
	}
	c.press(t, "\r")
	c.await(t, "Saved "+stored)
	c.await(t, "Greet someone  ·  Sharing  ·  Ctrl-C to stop")

	want := `{"title":"Greet someone","owner":"Dana","inputs":[` +
		`{"name":"name","type":"text","label":"Your name","required":true},` +
		`{"name":"tone","type":"choice","label":"Tone","options":["warm","formal"]}],` +
		`"output":{"kind":"text","label":"a greeting"}}`
	if info, err := os.Stat(stored); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("a new task file has mode %v; it must be private to the owner", info.Mode())
	}
	loaded, err := loadTask(script)
	if err != nil || form(t, loaded.public()) != want || !slices.Equal(loaded.Command, []string{"./greet.sh", "{name}", "{tone}"}) ||
		loaded.Timeout != 60 || !loaded.Progress {
		t.Fatalf("stored task %+v: %v", loaded, err)
	}
	b, got := recipient(t, c.screen())
	if got != want {
		t.Fatalf("recipients got %s", got)
	}
	stages, output, failure := b.run(t, map[string]any{"name": "Ada", "tone": "warm"})
	if failure != nil || output != "Hi Ada, warm\n" || !slices.Contains(stages, "running: choosing words for Ada") {
		t.Fatalf("stages %q, output %q, failure %+v", stages, output, failure)
	}
	c.await(t, "Finished in")
	c.await(t, "│ choosing words for Ada")
	c.press(t, "\x03")
	exited(t, done, 0)
	c.await(t, "Stopped sharing. The link no longer works.")

	again := newConsole(t)
	done = again.share(t.Context(), env, script)
	again.await(t, "From "+stored+"; runlink share --edit "+script+" changes it")
	if _, got := recipient(t, again.screen()); got != want {
		t.Fatalf("the second share gave %s", got)
	}
	if strings.Contains(again.screen(), "Title") {
		t.Fatalf("the second share asked again:\n%s", again.screen())
	}
	again.press(t, "\x03")
	exited(t, done, 0)
}

// The setup asks how the script takes each input, and the run passes each one
// so: options before arguments, a variable, and a line of stdin. --edit
// suggests the same answers, so Enter through it saves the file unchanged.
func TestSetupAsksHowEachInputIsPassed(t *testing.T) {
	t.Setenv("TOKEN", "the owner's")
	stack := startStack(t)
	stubClipboard(t)
	script := writeScript(t, "tool.sh", `for a; do printf '[%s]' "$a"; done; echo; echo "TOKEN=${TOKEN-unset}"; cat`)
	env := terminalEnv(stack.Frontend)

	c := newConsole(t)
	done := c.share(t.Context(), env, script)
	c.await(t, "tool.sh has no task file yet")
	c.press(t, "\r\r\r"+"y"+"Name\r\r\r\r\r\r") // a required argument
	c.await(t, "name         text, required, argument · Name")
	c.press(t, "y"+"Verbose\r\rb\r\r\r\r") // a boolean, passed as --verbose when on
	c.await(t, "verbose      boolean, --verbose · Verbose")
	c.press(t, "y"+"Dest\r\r\r\rno\r\r") // an optional option
	c.await(t, "dest         text, --dest · Dest")
	c.press(t, "y"+"Token\r\r\r\rnv\r\r") // an optional variable
	c.await(t, "token        text, $TOKEN · Token")
	c.press(t, "y"+"Note\r\r\r\rns\r") // a line of stdin
	c.await(t, "note         text, stdin · Note")
	c.press(t, "\r"+"\r"+"\r\r\r\r")
	runs := "Runs  [TOKEN={token}] ./tool.sh [--verbose] [--dest {dest}] {name}"
	c.await(t, runs)
	c.await(t, "Stdin {note}")
	c.press(t, "\r")
	c.await(t, "Sharing")
	stored, err := os.ReadFile(storedPath(script))
	if err != nil {
		t.Fatal(err)
	}
	want := `"command": ["./tool.sh", "{verbose}", "{dest}", "{name}"],` + "\n" + `  "inputs": [` + "\n" +
		`    { "name": "name", "type": "text", "label": "Name", "required": true },` + "\n" +
		`    { "name": "verbose", "type": "boolean", "label": "Verbose", "flag": "--verbose" },` + "\n" +
		`    { "name": "dest", "type": "text", "label": "Dest", "flag": "--dest" },` + "\n" +
		`    { "name": "token", "type": "text", "label": "Token", "env": "TOKEN" },` + "\n" +
		`    { "name": "note", "type": "text", "label": "Note", "stdin": true }` + "\n  ],"
	if !strings.Contains(string(stored), want) {
		t.Fatalf("stored:\n%s", stored)
	}
	b, got := recipient(t, c.screen())
	if strings.Contains(got, "flag") || strings.Contains(got, "TOKEN") || strings.Contains(got, "stdin") {
		t.Fatalf("recipients got %s", got)
	}
	if _, output, failure := b.run(t, map[string]any{"name": "Ada", "verbose": true, "dest": "/tmp/x", "note": "hello"}); failure != nil ||
		output != "[--verbose][--dest][/tmp/x][Ada]\nTOKEN=unset\nhello\n" {
		t.Fatalf("output %q, failure %+v", output, failure)
	}
	c.press(t, "\x03")
	exited(t, done, 0)

	// A second way in, written by hand, stays through --edit.
	stored = []byte(strings.Replace(string(stored), `"env": "TOKEN" }`, `"env": "TOKEN", "stdin": true }`, 1))
	if err := os.WriteFile(storedPath(script), stored, 0o600); err != nil {
		t.Fatal(err)
	}
	c = newConsole(t)
	done = c.share(t.Context(), env, "--edit", script)
	c.await(t, "Editing")
	// Each input: keep, label, name, type, several and required unless a
	// switch, passed as, and its option, variable, or a switch's two texts.
	c.press(t, "\r\r\r"+strings.Repeat("\r", 7)+strings.Repeat("\r", 7)+strings.Repeat("\r", 8)+strings.Repeat("\r", 8)+strings.Repeat("\r", 7)+"\r"+"\r"+"\r\r\r\r")
	c.await(t, "Share now?")
	c.await(t, runs)
	c.await(t, "Stdin {token} {note}, a line each")
	c.press(t, "\r")
	c.await(t, "Sharing")
	if again, err := os.ReadFile(storedPath(script)); err != nil || string(again) != string(stored) {
		t.Fatalf("--edit changed the file to:\n%s %v", again, err)
	}
	c.press(t, "\x03")
	exited(t, done, 0)
}

// Ctrl-C during the setup writes nothing; --edit suggests the stored answers,
// keeps what it does not ask about, and saves the changes; a broken stored file
// is reported, never asked over.
func TestGuidedSetupCancelEditAndErrors(t *testing.T) {
	stack := startStack(t)
	stubClipboard(t)
	script := writeScript(t, "echo.sh", `echo "$1"`)
	stored := storedPath(script)
	env := terminalEnv(stack.Frontend)

	c := newConsole(t)
	done := c.share(t.Context(), env, script)
	c.await(t, "Title")
	c.press(t, "Half done\r")
	c.await(t, "Description")
	c.press(t, "\x03")
	exited(t, done, 130)
	c.await(t, "Canceled. Nothing was saved.")
	if entries, err := os.ReadDir(filepath.Dir(script)); err != nil || len(entries) != 1 {
		t.Fatalf("the canceled setup left files: %v %v", entries, err)
	}

	if err := os.WriteFile(stored, []byte(`{"title": "Echo", "command": ["./echo.sh", "{text}", "{pick}"], "output": {"kind": "text"},
		"inputs": [{"name": "text", "type": "text", "label": "Text", "help": "Echoed back", "required": true},
			{"name": "pick", "type": "choice", "options": [1, "1", "a,b", "b", "warm ", "a", "a ", ""], "default": 1}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stored, 0o640); err != nil {
		t.Fatal(err)
	}
	c = newConsole(t)
	done = c.share(t.Context(), env, "--edit", script)
	c.await(t, "Editing "+stored)
	c.await(t, "Title         Echo")
	// A new title; the first input renamed, the second, without a label and with
	// options no list of text could hold, kept as it is, each where it was; a
	// five-second timeout.
	c.press(t, "Echo it\r\r\r"+"\r"+"\rwords\r"+"\r\r\r\r\r"+"\r"+strings.Repeat("\r", 8)+"\r"+"\r"+"\r\r"+"5\r"+"\r")
	c.await(t, "Runs  ./echo.sh {words} {pick}")
	c.press(t, "\r")
	c.await(t, "Sharing")
	edited, err := loadTask(script)
	if err != nil || edited.Title != "Echo it" || len(edited.Inputs) != 2 || edited.Inputs[0].Name != "words" ||
		edited.Inputs[0].Help != "Echoed back" || !slices.Equal(edited.Command, []string{"./echo.sh", "{words}", "{pick}"}) || edited.Timeout != 5 ||
		form(t, shown{definition: definition{Inputs: edited.Inputs[1:]}}) != `{"title":"","inputs":[{"name":"pick","type":"choice","default":1,"options":[1,"1","a,b","b","warm ","a","a ",""]}]}` {
		t.Fatalf("edited task %+v: %v", edited, err)
	}
	if info, err := os.Stat(stored); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o640 {
		t.Fatalf("--edit changed the file's mode to %v", info.Mode())
	}
	c.press(t, "\x03")
	exited(t, done, 0)

	for name, file := range map[string]string{
		"title is required":             `{"title": "", "command": ["./echo.sh"]}`,
		"line 3: invalid character":     "{\n  \"title\": \"T\",\n  ,\n}",
		`unknown field "shell"`:         `{"title": "T", "command": ["./echo.sh"], "shell": true}`,
		"a task file is at most 64 KiB": `{"title": "` + strings.Repeat("x", maxTaskFile) + `", "command": ["./echo.sh"]}`,
	} {
		if err := os.WriteFile(stored, []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{script}, {"--edit", script}} {
			c = newConsole(t)
			exited(t, c.share(t.Context(), env, args...), 2)
			if errs := c.errs.String(); !strings.Contains(errs, "runlink: "+stored) || !strings.Contains(errs, name) {
				t.Errorf("%s %q: %s", name, args, errs)
			}
			if strings.Contains(c.screen(), "Title") {
				t.Errorf("%s %q: asked over the file", name, args)
			}
		}
	}
}

// Without a terminal, a script without a stored task file shares as text
// without input, asking nothing and writing nothing.
func TestShareScriptWithoutTerminal(t *testing.T) {
	stack := startStack(t)
	script := writeScript(t, "hello.sh", "echo hello")
	ctx, cancel := context.WithCancel(t.Context())
	var stdout, stderr lockedBuffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"share", script}, env(stack.Frontend), nil, &stdout, &stderr) }()
	for deadline := time.Now().Add(10 * time.Second); linkLine.FindString(stdout.String()) == ""; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no link: %q %q", stdout.String(), stderr.String())
		}
	}
	b, got := recipient(t, stdout.String())
	if want := `{"title":"hello.sh","output":{"kind":"text"}}`; got != want {
		t.Fatalf("form %s", got)
	}
	if _, output, failure := b.run(t, map[string]any{}); failure != nil || output != "hello\n" {
		t.Fatalf("%q %+v", output, failure)
	}
	cancel()
	exited(t, done, 0)
	if entries, err := os.ReadDir(filepath.Dir(script)); err != nil || len(entries) != 1 || strings.Contains(stdout.String(), "\x1b") {
		t.Fatalf("files %v %v, output %q", entries, err, stdout.String())
	}
	for _, args := range [][]string{{"--edit", script}, {"--edit", writeTask(t, "", `{"title": "T", "command": ["./task.sh"]}`)}} {
		var errs strings.Builder
		if code := run(t.Context(), append([]string{"share"}, args...), env(stack.Frontend), nil, io.Discard, &errs); code != 2 ||
			!strings.Contains(errs.String(), "--edit") {
			t.Errorf("%q: exit %d: %s", args, code, errs.String())
		}
	}
}

// NO_COLOR drops every style, and a dumb terminal every escape sequence; both
// still show the task, the link, connections, runs, and stderr.
func TestPlainTerminals(t *testing.T) {
	stack := startStack(t)
	stubClipboard(t)
	script := writeScript(t, "say.sh", `echo "saying $1" >&2; echo "$1"`)
	if err := os.WriteFile(storedPath(script), []byte(`{"title": "Say", "command": ["./say.sh", "{word}"],
		"inputs": [{"name": "word", "type": "text"}], "output": {"kind": "text"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, env := range map[string]func(string) string{
		"NO_COLOR": terminalEnv(stack.Frontend, "NO_COLOR", "1", "LANG", "C"),
		"dumb":     terminalEnv(stack.Frontend, "TERM", "dumb"),
	} {
		t.Run(name, func(t *testing.T) {
			c := newConsole(t)
			done := c.share(t.Context(), env, script)
			c.await(t, "Anyone with")
			b, _ := recipient(t, c.screen())
			if _, output, failure := b.run(t, map[string]any{"word": "hi"}); failure != nil || output != "hi\n" {
				t.Fatalf("%q %+v", output, failure)
			}
			shown := []string{"Say", "Sharing", "Ctrl-C", "Copied to the clipboard", "Anyone with"}
			if name == "dumb" {
				for deadline := time.Now().Add(10 * time.Second); !strings.Contains(c.errs.String(), "Run finished in"); time.Sleep(20 * time.Millisecond) {
					if time.Now().After(deadline) {
						t.Fatalf("no run in the log:\n%s", c.errs.String())
					}
				}
				for _, event := range []string{"A recipient connected.", "Run started.", "  saying hi"} {
					if !strings.Contains(c.errs.String(), event) {
						t.Fatalf("the log lacks %q:\n%s", event, c.errs.String())
					}
				}
			} else {
				shown = append(shown, "1 recipient connected", "Finished in", "| saying hi")
			}
			for _, text := range shown {
				c.await(t, text)
			}
			c.press(t, "\x03")
			exited(t, done, 0)
			screen := c.out.String()
			if name == "dumb" && strings.Contains(screen, "\x1b") || regexp.MustCompile(`\x1b\[[0-9;]*m`).MatchString(screen) ||
				name == "NO_COLOR" && strings.ContainsFunc(screen, func(r rune) bool { return r > 0x7e }) {
				t.Fatalf("styled output: %q", screen)
			}
		})
	}
}

// Enter starts the browser opener on its own, so one that hangs cannot keep
// Ctrl-C from stopping the share.
func TestHungOpenerDoesNotHoldSharing(t *testing.T) {
	stack := startStack(t)
	stubClipboard(t)
	dir := t.TempDir()
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexec sleep 60\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := newConsole(t)
	done := c.share(t.Context(), terminalEnv(stack.Frontend), writeTask(t, "echo ok", `{"title": "T", "command": ["./task.sh"]}`))
	c.await(t, "Enter opens it in your browser")
	c.press(t, "\r")
	time.Sleep(200 * time.Millisecond) // the opener starts
	c.press(t, "\x03")
	exited(t, done, 0)
}

// A terminal too narrow for the form's preview still completes the setup.
func TestSetupInANarrowTerminal(t *testing.T) {
	stack := startStack(t)
	stubClipboard(t)
	script := writeScript(t, "narrow.sh", "echo ok")
	c := newConsole(t)
	if err := unix.IoctlSetWinsize(int(c.tty.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 5}); err != nil {
		t.Fatal(err)
	}
	done := c.share(t.Context(), terminalEnv(stack.Frontend), script)
	c.await(t, "no task file yet")
	c.press(t, strings.Repeat("\r", 10)) // every suggestion, then share
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(storedPath(script)); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("not saved: %v\n%s", err, c.screen())
		}
	}
	c.press(t, "\x03")
	exited(t, done, 0)
}

// Commands and variables typed in the setup split as a POSIX shell splits
// them, and a command it shows reads back as the same arguments.
func TestWordsSplitAsAShell(t *testing.T) {
	for line, want := range map[string][]string{
		`./tool.sh "\d+" '\d' a\ b`:   {"./tool.sh", `\d+`, `\d`, "a b"},
		`"a\"b" "x\\y" "\$HOME" '' x`: {`a"b`, `x\y`, "$HOME", "", "x"},
	} {
		if got, ok := words(line); !ok || !slices.Equal(got, want) {
			t.Errorf("%s: %q %v", line, got, ok)
		}
	}
	for _, line := range []string{`"open`, `'open`, `end\`} {
		if _, ok := words(line); ok {
			t.Errorf("%s: accepted", line)
		}
	}
	argv := []string{"./a b.sh", `\d+`, "it's", `"q"`, "{name}", "", "two\nlines"}
	if got, ok := words(quoted(argv)); !ok || !slices.Equal(got, argv) {
		t.Errorf("round trip: %q", got)
	}
	if env, problem := parseEnv(`RE="\d+" A='b c'`, nil); problem != "" || env["RE"] != `\d+` || env["A"] != "b c" {
		t.Errorf("variables: %q %s", env, problem)
	}
	fixed := map[string]string{"NOTE": "two\nlines", "Q": "it's"}
	if env, problem := parseEnv(envLine(fixed), nil); problem != "" || !maps.Equal(env, fixed) {
		t.Errorf("variables round trip: %q %s", env, problem)
	}
}

func TestCommandLineQuotesForTheShell(t *testing.T) {
	got := commandLine([]string{"./a b.sh", "x;y", "$(id)", "`id`", "it's", "{name}", "--n={n}", "{a,b}", "~", ""})
	if want := `'./a b.sh' 'x;y' '$(id)' '` + "`id`" + `' 'it'\''s' {name} --n={n} '{a,b}' '~' ''`; got != want {
		t.Fatalf("%s\nwant %s", got, want)
	}
}

func TestFitKeepsLinesToOneRow(t *testing.T) {
	st := style{color: true, unicode: true}
	for _, c := range []struct {
		line string
		n    int
		want string
	}{
		{"short", 5, "short"},
		{"\x1b[2mabcdef\x1b[0m", 4, "\x1b[2mabc\x1b[0m…"},
		{"日本語です", 6, "日本\x1b[0m…"},
		{"a\tb", 4, "a\x1b[0m…"},
	} {
		if got := fit(c.line, c.n, st); got != c.want || cols(got) > c.n {
			t.Errorf("fit(%q, %d) = %q", c.line, c.n, got)
		}
	}
}

func TestEveryStderrLineStaysInTheScrollback(t *testing.T) {
	var out lockedBuffer
	st := style{live: true}
	v := newView(st, &region{w: &out, st: st, width: func() int { return 40 }}, &out, io.Discard)
	v.begin([]string{"header"})
	want := []string{"header", "> ", "Started"}
	v.started()
	for i := range 12 { // more than a screen region shows, each wider than the terminal
		line := strconv.Itoa(i) + " " + strings.Repeat("x", 60)
		v.stderr(line)
		want = append(want, "| "+line)
	}
	v.finished(time.Second, &runError{Code: "failed", cause: errors.New("exit status 1")})
	v.started() // a silent run stays one line
	v.finished(time.Second, nil)
	v.close()
	got := strings.Join(rendered(out.String()), "\n")
	for rest, i := got, 0; i < len(want); i++ {
		at := strings.Index(rest, want[i])
		if at < 0 {
			t.Fatalf("missing %q in order:\n%s", want[i], got)
		}
		rest = rest[at+len(want[i]):]
	}
	if !strings.Contains(got, "Failed after 1.0s: exit status 1\n  + ") || strings.Count(got, "Started") != 1 {
		t.Fatalf("runs:\n%s", got)
	}
}

// rendered is what a terminal holds after the region's writes, scrollback
// included: lines are not wrapped, so a line cut short shows.
func rendered(s string) []string {
	lines, row := []string{}, 0
	for _, part := range regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\r|\n|[^\x1b\r\n]+`).FindAllString(s, -1) {
		for len(lines) <= row {
			lines = append(lines, "")
		}
		switch {
		case part == "\n":
			row++
		case part == "\x1b[J":
			lines = lines[:row]
		case strings.HasPrefix(part, "\x1b[") && strings.HasSuffix(part, "A"):
			n, _ := strconv.Atoi(part[2 : len(part)-1])
			row -= n
		case part == "\r" || part[0] == '\x1b': // each draw starts a line; styles and the cursor change nothing here
		default:
			lines[row] += part
		}
	}
	return lines
}

// The setup adapts to the script: each positional argument goes where the
// owner says, a flag can have another for off, a field can take several files,
// variables can be fixed for every run, one that changes what the script runs
// can be passed when the owner allows it, and the command line itself can be
// edited. --edit keeps it all.
func TestSetupAdaptsToTheScript(t *testing.T) {
	stack := startStack(t)
	stubClipboard(t)
	script := writeScript(t, "tool.sh", `for a; do case $a in /*) cat "$a" ;; *) printf '[%s]' "$a" ;; esac; done
echo; echo "MODE=$MODE GREETING=$GREETING PATH=$PATH"`)
	env := terminalEnv(stack.Frontend)

	c := newConsole(t)
	done := c.share(t.Context(), env, script)
	c.await(t, "tool.sh has no task file yet")
	c.press(t, "\r\r\r"+"y"+"Alpha\r\r\r\r\r\r")
	c.await(t, "alpha        text, required, argument · Alpha")
	c.press(t, "y"+"Beta\r\r\r\r\r\r")
	c.await(t, "1st         before {alpha}")
	c.press(t, "1\r") // before alpha
	c.press(t, "y"+"Files\r\rf\ryn\r")
	c.await(t, "their paths as positional arguments")
	c.await(t, "3rd         after {alpha}")
	c.press(t, "\r")
	c.await(t, "each value its own argument")
	c.press(t, "\r")
	c.await(t, "files        file, several, argument · Files")
	c.press(t, "y"+"Color\r\rb\r\r\r--no-color\r")
	c.await(t, "color        boolean, --color|--no-color · Color")
	c.press(t, "y"+"Path\r\r\r\rnv\r\r")
	c.await(t, "PATH lets recipients run other code. Allow?")
	c.press(t, "y")
	c.await(t, "path         text, $PATH, unsafe · Path")
	c.press(t, "\r"+"MODE=prod 'GREETING=hi there'\r"+"\r\r\r\r")
	c.await(t, "Runs  'GREETING=hi there' MODE=prod [PATH={path}] ./tool.sh --color|--no-color {beta} {alpha} [{files}]...")
	c.await(t, "Unsafe by your choice: path sets PATH")
	// A command that cannot run is caught before anything is saved.
	c.press(t, "c")
	c.await(t, "Command       ./tool.sh {color} {beta} {alpha} {files}")
	c.press(t, "./missing.sh {color} {beta} {alpha} {files}\r")
	c.await(t, "This would not load: cannot run ./missing.sh")
	c.press(t, "c\x15./tool.sh run {color} {beta} {alpha} {files}\r")
	c.await(t, "Runs  'GREETING=hi there' MODE=prod [PATH={path}] ./tool.sh run --color|--no-color {beta} {alpha} [{files}]...")
	c.press(t, "\r")
	c.await(t, "Saved "+storedPath(script))
	c.await(t, "Sharing")
	if strings.Count(c.screen(), "Unsafe by your choice: path sets PATH") != 4 { // each summary, then sharing
		t.Fatalf("the unsafe input was not shown while sharing:\n%s", c.screen())
	}
	stored, err := os.ReadFile(storedPath(script))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"command": ["./tool.sh", "run", "{color}", "{beta}", "{alpha}", "{files}"],` + "\n" + `  "env": { "GREETING": "hi there", "MODE": "prod" },`,
		`{ "name": "files", "type": "file", "label": "Files", "multiple": true },`,
		`{ "name": "color", "type": "boolean", "label": "Color", "flag": "--color", "off": "--no-color" },`,
		`{ "name": "path", "type": "text", "label": "Path", "env": "PATH", "unsafe": true }`,
	} {
		if !strings.Contains(string(stored), want) {
			t.Fatalf("stored file lacks %s:\n%s", want, stored)
		}
	}
	b, got := recipient(t, c.screen())
	if strings.Contains(got, "unsafe") || strings.Contains(got, "PATH") || strings.Contains(got, "no-color") || !strings.Contains(got, `"multiple":true`) {
		t.Fatalf("recipients got %s", got)
	}
	files := []any{map[string]any{"name": "a.txt", "size": 2.0}, map[string]any{"name": "a.txt", "size": 2.0}}
	if _, output, failure := b.run(t, map[string]any{"alpha": "A", "beta": "B", "files": files, "color": false, "path": "/usr/bin:/bin"},
		[]byte("fa"), []byte("fb")); failure != nil || output != "[run][--no-color][B][A]fafb\nMODE=prod GREETING=hi there PATH=/usr/bin:/bin\n" {
		t.Fatalf("output %q, failure %+v", output, failure)
	}
	c.press(t, "\x03")
	exited(t, done, 0)

	c = newConsole(t)
	done = c.share(t.Context(), env, "--edit", script)
	c.await(t, "Editing")
	// Title, description, owner; alpha, beta, files, color, path; no more
	// inputs; the variables; output, label, timeout, progress; share.
	c.press(t, strings.Repeat("\r", 3+8+8+9+7+8+1+1+4+1))
	c.await(t, "Sharing")
	if again, err := os.ReadFile(storedPath(script)); err != nil || string(again) != string(stored) {
		t.Fatalf("--edit changed the file to:\n%s %v", again, err)
	}
	c.press(t, "\x03")
	exited(t, done, 0)
}

// A script's task file is named after all of its name, so greet.sh and greet.py
// each have their own. A script without one is offered another's only by
// asking: one set up for a script with the same contents, or for one now gone.
func TestStoredFilesFollowTheWholeScriptName(t *testing.T) {
	stack := startStack(t)
	stubClipboard(t)
	env := terminalEnv(stack.Frontend)
	greet := writeScript(t, "greet.sh", `echo "Hi $1"`)
	dir := filepath.Dir(greet)
	data, err := os.ReadFile(greet)
	if err != nil {
		t.Fatal(err)
	}
	file := `{"title": "Greet", "command": ["./greet.sh", "{name}"], "inputs": [{"name": "name", "type": "text", "required": true}],
		"output": {"kind": "text"}, "sha256": "` + fmt.Sprintf("%x", sha256.Sum256(data)) + `"}`
	if err := os.WriteFile(storedPath(greet), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	script := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}

	// Another script of the same name gets questions of its own.
	c := newConsole(t)
	done := c.share(t.Context(), env, script("greet.py", "#!/bin/sh\necho py\n"))
	c.await(t, "greet.py has no task file yet")
	c.press(t, "\x03")
	exited(t, done, 130)
	if strings.Contains(c.screen(), "Start from") {
		t.Fatalf("offered greet.sh's answers:\n%s", c.screen())
	}

	// A copy is offered the original's answers, which the owner takes as they are.
	hello := script("hello.sh", string(data))
	c = newConsole(t)
	done = c.share(t.Context(), env, hello)
	c.await(t, "greet.sh    its answers; it has the same contents as hello.sh")
	c.press(t, "\r")
	c.await(t, "Starting from "+storedPath(greet)+"; the answers go to "+storedPath(hello))
	c.press(t, strings.Repeat("\r", 3+7+1+1+4))
	c.await(t, "Runs  ./hello.sh {name}")
	c.press(t, "\r")
	c.await(t, "Sharing")
	b, _ := recipient(t, c.screen())
	if _, output, failure := b.run(t, map[string]any{"name": "Ada"}); failure != nil || output != "Hi Ada\n" {
		t.Fatalf("output %q, failure %+v", output, failure)
	}
	c.press(t, "\x03")
	exited(t, done, 0)
	if kept, err := os.ReadFile(storedPath(greet)); err != nil || string(kept) != file {
		t.Fatalf("the original's file changed: %s %v", kept, err)
	}

	// After a rename, the old name's file is offered; the owner can start afresh.
	if err := os.Remove(greet); err != nil {
		t.Fatal(err)
	}
	c = newConsole(t)
	done = c.share(t.Context(), env, script("renamed.sh", "#!/bin/sh\necho \"Hello $1\"\n"))
	c.await(t, "greet.sh    its answers; greet.sh is gone, perhaps renamed")
	c.press(t, "n\r")
	c.await(t, "renamed.sh has no task file yet")
	c.press(t, "\x03")
	exited(t, done, 130)
}

// A command taken from another script's file runs the new script in its
// place, keeping the program that runs it, and adding one when the new script
// is not executable.
func TestRetargetKeepsTheProgram(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plain.sh"), []byte("#!/bin/sh\necho hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		command             []string
		other, script, want string
		found               bool
	}{
		{[]string{"./greet.sh", "{name}"}, "greet.sh", "hello.sh", "./hello.sh {name}", true},
		{[]string{"python3", "./tool.py", "{x}"}, "tool", "hello.py", "python3 ./hello.py {x}", true}, // an older file, named without the extension
		{[]string{"python3", "tool.py"}, "tool.py", "hello.py", "python3 ./hello.py", true},           // a bare operand
		{[]string{"/usr/bin/env", "python3", "./old.py", "--in={x}"}, "gone", "hello.py", "/usr/bin/env python3 ./hello.py --in={x}", true},
		{[]string{"./greet.sh", "{name}"}, "greet.sh", "plain.sh", "/bin/sh ./plain.sh {name}", true}, // not executable
		{[]string{"python3", "-c", "print(1)"}, "gone", "hello.py", "python3 -c 'print(1)'", false},
	} {
		got, found := retarget(c.command, dir, dir, c.other, c.script)
		if commandLine(got) != c.want || found != c.found {
			t.Errorf("%q from %s: %s %v, want %s %v", c.command, c.other, commandLine(got), found, c.want, c.found)
		}
	}
}

// Only a task file that loads is offered to a script without one.
func TestMatchesOfferOnlyFilesThatLoad(t *testing.T) {
	dir := t.TempDir()
	for name, file := range map[string]string{
		"old.sh.runlink.json":    `{"title": "Old"}`,
		"broken.sh.runlink.json": `{"title": "B", "command": ["./broken.sh"], "inputs": [{"name": "a", "type": "html"}]}`,
		"gone.sh.runlink.json":   `{"title": "Gone", "command": ["./gone.sh"]}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if found := matches(filepath.Join(dir, "new.sh"), strings.Repeat("0", 64), nil); len(found) != 1 || filepath.Base(found[0].script) != "gone.sh" {
		t.Fatalf("offered %+v", found)
	}
}

// Enter through --edit keeps a separator exactly, whitespace and all.
func TestEditKeepsSeparators(t *testing.T) {
	stack := startStack(t)
	stubClipboard(t)
	script := writeScript(t, "join.sh", `printf '[%s]' "$@"; echo "$A"`)
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	draft := &task{definition: definition{Title: "J", Output: &output{Kind: "text"}, Inputs: []input{
		{Name: "a", Type: "text", Multiple: true, binding: binding{Env: "A", Join: "\n\t"}},
		{Name: "b", Type: "text", Multiple: true, binding: binding{Join: " | "}},
	}}, Command: []string{"./join.sh", "{b}"}, Timeout: 60, SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), dir: filepath.Dir(script)}
	stored, err := encodeTask(draft)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storedPath(script), stored, 0o600); err != nil {
		t.Fatal(err)
	}
	c := newConsole(t)
	done := c.share(t.Context(), terminalEnv(stack.Frontend), "--edit", script)
	c.await(t, "Editing")
	// Title, description, owner; a: keep, label, name, type, several,
	// required, passed as, variable, values, between; b: the same with no
	// variable; no more inputs; variables; output, label, timeout, progress.
	c.press(t, strings.Repeat("\r", 3+10+9+1+1+4))
	c.await(t, "Share now?")
	c.press(t, "\r")
	c.await(t, "Sharing")
	if again, err := os.ReadFile(storedPath(script)); err != nil || string(again) != string(stored) {
		t.Fatalf("--edit changed the file to:\n%s %v", again, err)
	}
	b, _ := recipient(t, c.screen())
	if _, output, failure := b.run(t, map[string]any{"a": []any{"x", "y"}, "b": []any{"p", "q"}}); failure != nil || output != "[p | q]x\n\ty\n" {
		t.Fatalf("output %q, failure %+v", output, failure)
	}
	c.press(t, "\x03")
	exited(t, done, 0)
}

// The setup asks for a count's flag and how it is written, and for an argument
// written around an input's value, with an option before it; inputs in one
// such argument go together as rows, which the recipient fills in.
func TestSetupCountsAndPairs(t *testing.T) {
	stack := startStack(t)
	stubClipboard(t)
	script := writeScript(t, "tool.sh", `for a; do printf '[%s]' "$a"; done`)
	env := terminalEnv(stack.Frontend)

	c := newConsole(t)
	done := c.share(t.Context(), env, script)
	c.await(t, "tool.sh has no task file yet")
	c.press(t, "\r\r\r"+"y"+"Verbosity\r\rn\r\rn"+"c\r")
	c.await(t, "Flag        -v")
	c.press(t, "\r")
	c.await(t, "stacked     -vvv")
	c.press(t, "s\r\r")
	c.await(t, "verbosity    number, [-vv]... · Verbosity")
	c.press(t, "y"+"Host\r\r\ryn"+"p\r"+"{host}:{port}\r"+"-p\r\r")
	c.await(t, "host         text, several, [-p {host}:{port}]... · Host")
	c.press(t, "y"+"Port\r\rn\ryn")
	c.await(t, "inside an argument you write")
	c.press(t, "\r\r\r\r")
	c.await(t, "port         number, several, [-p {host}:{port}]..., in rows with host · Port")
	c.press(t, "n"+"\r"+"\r\r\r\r")
	c.await(t, "Runs  ./tool.sh [-vv]... [-p {host}:{port}]...")
	c.await(t, "Host · Port")
	c.press(t, "\r")
	c.await(t, "Sharing")
	stored, err := os.ReadFile(storedPath(script))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"command": ["./tool.sh", "{verbosity}", "{host}:{port}"],`,
		`{ "name": "verbosity", "type": "number", "label": "Verbosity", "max": 3, "flag": "-v", "count": true, "stack": true },`,
		`{ "name": "host", "type": "text", "label": "Host", "multiple": true, "flag": "-p" },`,
		`{ "name": "port", "type": "number", "label": "Port", "multiple": true }`,
	} {
		if !strings.Contains(string(stored), want) {
			t.Fatalf("stored file lacks %s:\n%s", want, stored)
		}
	}
	b, got := recipient(t, c.screen())
	if !strings.Contains(got, `"pairs":[["host","port"]]`) || strings.Contains(got, `"count"`) {
		t.Fatalf("recipients got %s", got)
	}
	if _, output, failure := b.run(t, map[string]any{"verbosity": 2.0, "host": []any{"a", "b"}, "port": []any{80.0, 443.0}}); failure != nil ||
		output != "[-vv][-p][a:80][-p][b:443]" {
		t.Fatalf("output %q, failure %+v", output, failure)
	}
	c.press(t, "\x03")
	exited(t, done, 0)

	// Title, description, owner; verbosity, host, port; no more inputs; the
	// variables; output, label, timeout, progress; share.
	c = newConsole(t)
	done = c.share(t.Context(), env, "--edit", script)
	c.await(t, "Editing")
	c.press(t, strings.Repeat("\r", 3+10+10+10+1+1+4+1))
	c.await(t, "Sharing")
	if again, err := os.ReadFile(storedPath(script)); err != nil || string(again) != string(stored) {
		t.Fatalf("--edit changed the file to:\n%s %v", again, err)
	}
	c.press(t, "\x03")
	exited(t, done, 0)
}

// A script moved to another folder without its task file finds it by its
// contents, which share notes at each share, and asks before using it; a copy
// elsewhere is offered the moved one's file the same way. Unreadable notes are
// reported, and sharing goes on.
func TestMovedScriptFindsItsFile(t *testing.T) {
	stack := startStack(t)
	stubClipboard(t)
	config := t.TempDir()
	env := terminalEnv(stack.Frontend, "XDG_CONFIG_HOME", config, "HOME", config)
	notes := notesFile(env)
	greet := writeScript(t, "greet.sh", `echo "Hi $1"`)
	sum, err := digest(greet)
	if err != nil {
		t.Fatal(err)
	}
	file := `{"title": "Greet", "command": ["./greet.sh", "{name}"], "inputs": [{"name": "name", "type": "text", "required": true}],
		"output": {"kind": "text"}}`
	if err := os.WriteFile(storedPath(greet), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newConsole(t)
	wide := func(c *console) { // for the paths the choices show
		if err := unix.IoctlSetWinsize(int(c.tty.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 50, Col: 240}); err != nil {
			t.Fatal(err)
		}
	}
	done := c.share(t.Context(), env, greet)
	c.await(t, "Sharing")
	c.press(t, "\x03")
	exited(t, done, 0)
	if known, err := recall(notes); err != nil || known[storedPath(greet)] != sum {
		t.Fatalf("notes %v %v", known, err)
	}

	moved := filepath.Join(t.TempDir(), "greet.sh")
	if err := os.Rename(greet, moved); err != nil {
		t.Fatal(err)
	}
	c = newConsole(t)
	wide(c)
	done = c.share(t.Context(), env, moved)
	c.await(t, "greet.sh    its answers; same contents, perhaps moved here from "+filepath.Dir(greet))
	c.await(t, "new         answers of its own, from the start")
	c.press(t, "\r")
	c.await(t, "Starting from "+storedPath(greet)+"; the answers go to "+storedPath(moved))
	c.press(t, strings.Repeat("\r", 3+7+1+1+4))
	c.await(t, "Runs  ./greet.sh {name}")
	c.press(t, "\r")
	c.await(t, "Sharing")
	b, _ := recipient(t, c.screen())
	if _, output, failure := b.run(t, map[string]any{"name": "Ada"}); failure != nil || output != "Hi Ada\n" {
		t.Fatalf("output %q, failure %+v", output, failure)
	}
	c.press(t, "\x03")
	exited(t, done, 0)

	// The moved script's file is now its own: a copy in a third folder is
	// offered that one, and no longer the one it came from.
	script := func(dir, name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho \"Hi $1\""), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	c = newConsole(t)
	wide(c)
	done = c.share(t.Context(), env, script(t.TempDir(), "tool.sh"))
	c.await(t, "greet.sh    its answers; the same contents, in "+filepath.Dir(moved))
	c.await(t, "new         answers of its own")
	if strings.Contains(c.screen(), filepath.Dir(greet)) {
		t.Fatalf("offered the file the moved script took:\n%s", c.screen())
	}
	c.press(t, "\x03")
	exited(t, done, 130)
	// Nor is it offered beside where it was.
	c = newConsole(t)
	wide(c)
	done = c.share(t.Context(), env, script(filepath.Dir(greet), "again.sh"))
	c.await(t, "greet.sh    its answers; the same contents, in "+filepath.Dir(moved))
	c.await(t, "new         answers of its own")
	if strings.Contains(c.screen(), "perhaps renamed") {
		t.Fatalf("offered the file the moved script took:\n%s", c.screen())
	}
	c.press(t, "\x03")
	exited(t, done, 130)

	if err := os.WriteFile(notes, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	c = newConsole(t)
	done = c.share(t.Context(), env, moved)
	c.await(t, "Sharing")
	c.press(t, "\x03")
	exited(t, done, 0)
	if !strings.Contains(c.errs.String(), notes+": unexpected end of JSON input; fix or delete it so moved scripts find their task files") {
		t.Fatalf("stderr %q", c.errs.String())
	}
}

// Moving an argument written around a value keeps the others where they are.
func TestPositionMovesAnArgument(t *testing.T) {
	tk := &task{Command: []string{"./x", "{a}", "{b}.txt", "{c}"}}
	a := &asker{ctx: t.Context(), keys: readKeys(strings.NewReader("3\r")), r: &region{w: io.Discard, width: func() int { return 80 }}}
	if err := a.position(tk, "b", func(name string) bool { return name != "b" }); err != nil ||
		!slices.Equal(tk.Command, []string{"./x", "{a}", "{c}", "{b}.txt"}) {
		t.Fatalf("%q %v", tk.Command, err)
	}
}

// Notes stay whole however many shares change them at once, and notes that
// say null are none.
func TestNotesTakeEveryShare(t *testing.T) {
	dir := t.TempDir()
	notes := filepath.Join(dir, "config", "runlink", "scripts.json")
	var shares sync.WaitGroup
	for i := range 20 {
		stored := filepath.Join(dir, fmt.Sprintf("s%d.sh.runlink.json", i))
		if err := os.WriteFile(stored, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		shares.Go(func() {
			if err := remember(notes, stored, strings.Repeat("a", 64), ""); err != nil {
				t.Error(err)
			}
		})
	}
	shares.Wait()
	if known, err := recall(notes); err != nil || len(known) != 20 {
		t.Fatalf("%d notes %v", len(known), err)
	}
	if err := os.WriteFile(notes, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	stored := filepath.Join(dir, "s0.sh.runlink.json")
	if err := remember(notes, stored, strings.Repeat("b", 64), ""); err != nil {
		t.Fatal(err)
	}
	if known, err := recall(notes); err != nil || len(known) != 1 || known[stored] != strings.Repeat("b", 64) {
		t.Fatalf("%v %v", known, err)
	}
}
