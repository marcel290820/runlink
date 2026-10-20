package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
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

func startStack(t *testing.T) *devstack.Stack {
	t.Helper()
	stack, err := devstack.Start(t.Context(), devstack.Config{
		Frontend: "127.0.0.1:0", App: "127.0.0.1:0", TURN: "127.0.0.1:0", StateDir: t.TempDir(),
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

func form(t *testing.T, def definition) string {
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
	stored := filepath.Join(filepath.Dir(script), "greet.runlink.json")
	env := terminalEnv(stack.Frontend)

	c := newConsole(t)
	done := c.share(t.Context(), env, script)
	c.await(t, "greet.sh has no task file yet")
	// A title replacing the suggestion, no description, an owner, then a text
	// input and a choice whose options are first left out.
	c.press(t, "Greet someone\r\rDana\r"+"y"+"Your name\rname\r\r\r"+"y"+"Tone\r\rc\r\r")
	c.await(t, "Options are needed for a choice.")
	// Optional; no more inputs; text output with a label; the default timeout; progress.
	c.press(t, "warm, formal\r"+"n"+"\r"+"\ra greeting\r"+"\r"+"y")
	c.await(t, "Share now?")
	for _, shown := range []string{
		"Inputs        name         text, required · Your name",
		"tone         choice of warm, formal · Tone",
		"( None | warm | formal )  optional",
		"[ Run ]  You'll get a greeting · on Dana's machine",
		"Runs  ./greet.sh {name} {tone}, with the values as JSON on stdin too",
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
	if err != nil || form(t, loaded.definition) != want || !slices.Equal(loaded.Command, []string{"./greet.sh", "{name}", "{tone}"}) ||
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
	// options no list of text could hold, kept as it is; a five-second timeout.
	c.press(t, "Echo it\r\r\r"+"\r"+"\rwords\r\r\r"+"\r"+"\r\r\r\r\r"+"\r"+"\r\r"+"5\r"+"\r")
	c.await(t, "Runs  ./echo.sh {words} {pick}")
	c.press(t, "\r")
	c.await(t, "Sharing")
	edited, err := loadTask(script)
	if err != nil || edited.Title != "Echo it" || len(edited.Inputs) != 2 || edited.Inputs[0].Name != "words" ||
		edited.Inputs[0].Help != "Echoed back" || !slices.Equal(edited.Command, []string{"./echo.sh", "{words}", "{pick}"}) || edited.Timeout != 5 ||
		form(t, definition{Inputs: edited.Inputs[1:]}) != `{"title":"","inputs":[{"name":"pick","type":"choice","default":1,"options":[1,"1","a,b","b","warm ","a","a ",""]}]}` {
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
		"is for another script":         `{"title": "T", "command": ["/bin/echo"]}`,
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
	c.press(t, strings.Repeat("\r", 9)) // every suggestion, then share
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
