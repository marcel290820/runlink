package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLoadTaskRejectsInvalidFiles(t *testing.T) {
	for name, file := range map[string]string{
		"unknown field":    `{"title": "T", "command": ["./task.sh"], "shell": true}`,
		"no title":         `{"command": ["./task.sh"]}`,
		"no command":       `{"title": "T", "command": []}`,
		"missing program":  `{"title": "T", "command": ["./missing.sh"]}`,
		"two objects":      `{"title": "T", "command": ["./task.sh"]} {}`,
		"trailing bracket": `{"title": "T", "command": ["./task.sh"]} ]`,
		"trailing brace":   `{"title": "T", "command": ["./task.sh"]} } x`,
		"long timeout":     `{"title": "T", "command": ["./task.sh"], "timeout": 1e300}`,
		"output kind":      `{"title": "T", "command": ["./task.sh"], "output": {"kind": "html"}}`,
		"output path":      `{"title": "T", "command": ["./task.sh"], "output": {"kind": "file", "name": "../x"}}`,
		"file default":     `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "f", "type": "file", "default": "x"}]}`,
		"file limit":       `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "f", "type": "file", "maxBytes": -1}]}`,
		"output name":      `{"title": "T", "command": ["./task.sh"], "output": {"kind": "file", "name": ".."}}`,
		"input name":       `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a b", "type": "text"}]}`,
		"duplicate input":  `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "text"}, {"name": "a", "type": "text"}]}`,
		"choice options":   `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "choice", "options": [{"value": {}}]}]}`,
		"number bound":     `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "number", "min": "1"}]}`,
		"date bound":       `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "date", "max": "2026-02-30"}]}`,
		"bad default":      `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "choice", "options": ["x"], "default": "y"}]}`,
		"flag unplaced":    `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "text", "flag": "--a"}]}`,
		"flag in text":     `{"title": "T", "command": ["./task.sh", "x{a}"], "inputs": [{"name": "a", "type": "text", "flag": "--a"}]}`,
		"env name":         `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "text", "env": "1A"}]}`,
		"env PATH":         `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "text", "env": "PATH"}]}`,
		"env loader":       `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "text", "env": "LD_PRELOAD"}]}`,
		"env twice":        `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "text", "env": "A"}, {"name": "b", "type": "text", "env": "A"}]}`,
		"file stdin":       `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "file", "stdin": true}, {"name": "b", "type": "text", "stdin": true}]}`,
		"unplaced input":   `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "text", "stdin": true}, {"name": "b", "type": "text"}]}`,
		"on for text":      `{"title": "T", "command": ["./task.sh", "{a}"], "inputs": [{"name": "a", "type": "text", "on": "x"}]}`,
		"flag and on":      `{"title": "T", "command": ["./task.sh", "{a}"], "inputs": [{"name": "a", "type": "boolean", "flag": "-a", "on": "-b"}]}`,
		"several booleans": `{"title": "T", "command": ["./task.sh", "{a}"], "inputs": [{"name": "a", "type": "boolean", "multiple": true}]}`,
		"join alone":       `{"title": "T", "command": ["./task.sh", "{a}"], "inputs": [{"name": "a", "type": "text", "join": ","}]}`,
		"two lists":        `{"title": "T", "command": ["./task.sh", "{a}{b}"], "inputs": [{"name": "a", "type": "text", "multiple": true}, {"name": "b", "type": "text", "multiple": true}]}`,
		"list default":     `{"title": "T", "command": ["./task.sh", "{a}"], "inputs": [{"name": "a", "type": "text", "multiple": true, "default": "x"}]}`,
		"list item":        `{"title": "T", "command": ["./task.sh", "{a}"], "inputs": [{"name": "a", "type": "number", "multiple": true, "default": [1, "2"]}]}`,
		"list null":        `{"title": "T", "command": ["./task.sh", "{a}"], "inputs": [{"name": "a", "type": "text", "multiple": true, "default": ["x", null]}]}`,
		"fixed env name":   `{"title": "T", "command": ["./task.sh"], "env": {"A-B": "x"}}`,
		"fixed env null":   `{"title": "T", "command": ["./task.sh"], "env": {"A": "x\u0000"}}`,
		"fixed and input":  `{"title": "T", "command": ["./task.sh"], "env": {"A": "x"}, "inputs": [{"name": "a", "type": "text", "env": "A"}]}`,
		"sha256":           `{"title": "T", "command": ["./task.sh"], "sha256": "ABC"}`,
	} {
		if _, err := loadTask(writeTask(t, "", file)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	dir := t.TempDir()
	// A script shares directly: no inputs, its output as text. One that is not
	// executable runs with the program its #! line names, or else the usual one
	// for its extension; with neither, it is refused.
	for name, body := range map[string]string{"hello.sh": "echo hello\n", "hello.py": "print('hello')\n", "hello": "#!/bin/sh\necho hello\n"} {
		if name == "hello.py" {
			if _, err := exec.LookPath("python3"); err != nil {
				continue
			}
		}
		script := filepath.Join(dir, name)
		if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		tk, err := loadTask(script)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := tk.run(t.Context(), map[string]any{}, nil, func(string) {}); err != nil || string(out) != "hello\n" || tk.Title != name {
			t.Fatalf("%s: %q %v %+v", name, out, err, tk.definition)
		}
	}
	// A #! line's argument reaches its program as the system would pass it.
	quotedLine := filepath.Join(dir, "quoted")
	if err := os.WriteFile(quotedLine, []byte("#!/usr/bin/env -S sh -c 'echo \"[$1]\"' fixed\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	direct, directErr := exec.Command(quotedLine).Output()
	if err := os.Chmod(quotedLine, 0o600); err != nil {
		t.Fatal(err)
	}
	tk, err := loadTask(quotedLine)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tk.run(t.Context(), map[string]any{}, nil, func(string) {})
	if (err == nil) != (directErr == nil) || err == nil && string(out) != string(direct) ||
		runtime.GOOS == "linux" && string(out) != "["+quotedLine+"]\n" {
		t.Fatalf("a quoted #! line: %q %v, run directly %q %v", out, err, direct, directErr)
	}
	unknown := filepath.Join(dir, "hello.unknown")
	if err := os.WriteFile(unknown, []byte("echo hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTask(unknown); err == nil || !strings.Contains(err.Error(), "chmod +x") {
		t.Fatalf("a script that runs with no known program: %v", err)
	}
}

func TestCheckValidatesRecipientValues(t *testing.T) {
	tk, err := loadTask(writeTask(t, "", `{"title": "T", "command": ["./task.sh", "{text}", "--day={day}"], "inputs": [
		{"name": "text", "type": "text", "required": true, "maxLength": 3},
		{"name": "day", "type": "date", "min": "2026-01-01", "max": "2026-12-31"},
		{"name": "n", "type": "number", "min": 0.5, "step": 0.25},
		{"name": "flag", "type": "boolean"},
		{"name": "pick", "type": "choice", "options": ["a", {"value": 2, "label": "Two"}, true]},
		{"name": "doc", "type": "file", "maxBytes": 10}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	doc := func(name string, size any) map[string]any { return map[string]any{"name": name, "size": size} }
	args, problems := tk.check(map[string]any{"text": "abc", "day": "2026-10-13", "n": 1.25, "flag": false, "pick": 2.0, "doc": doc("-q3 report.csv", 10.0)})
	if len(problems) != 0 || fmt.Sprint(args["text"], args["n"], args["flag"], args["pick"]) != "[abc] [1.25] [false] [2]" {
		t.Fatalf("valid values: %v %v", args, problems)
	}
	for name, values := range map[string]map[string]any{
		"missing required": {},
		"blank required":   {"text": "  "},
		"too long":         {"text": "abcd"},
		"option-like":      {"text": "-rf"},
		"null byte":        {"text": "a\x00"},
		"wrong type":       {"text": 1.0},
		"impossible date":  {"text": "a", "day": "2026-02-30"},
		"early date":       {"text": "a", "day": "2025-12-31"},
		"off step":         {"text": "a", "n": 0.6},
		"below minimum":    {"text": "a", "n": 0.25},
		"not boolean":      {"text": "a", "flag": "true"},
		"not an option":    {"text": "a", "pick": "b"},
		"option as object": {"text": "a", "pick": map[string]any{"value": 2.0}},
		"unknown field":    {"text": "a", "other": "x"},
		"file too large":   {"text": "a", "doc": doc("a.csv", 11.0)},
		"file size":        {"text": "a", "doc": doc("a.csv", 1.5)},
		"negative size":    {"text": "a", "doc": doc("a.csv", -1.0)},
		"file path":        {"text": "a", "doc": doc("../a.csv", 1.0)},
		"file dots":        {"text": "a", "doc": doc("..", 1.0)},
		"file no name":     {"text": "a", "doc": doc("", 1.0)},
		"file control":     {"text": "a", "doc": doc("a\nb", 1.0)},
		"file extra key":   {"text": "a", "doc": map[string]any{"name": "a", "size": 1.0, "path": "/etc/passwd"}},
		"file no size":     {"text": "a", "doc": map[string]any{"name": "a", "x": 1.0}},
		"file as text":     {"text": "a", "doc": "/etc/passwd"},
	} {
		if _, problems := tk.check(values); len(problems) != 1 {
			t.Errorf("%s: %v", name, problems)
		}
	}
}

func TestRunOutcomes(t *testing.T) {
	for name, tc := range map[string]struct {
		script, output string
		timeout        float64
		code, message  string
	}{
		"exit status": {script: "exit 3", code: "failed"},
		"timeout":     {script: "sleep 5", timeout: 0.2, code: "timeout"},
		"too large":   {script: "head -c 1048577 /dev/zero", code: "failed", message: "larger than 1 MB"},
		"not JSON":    {script: "echo nope", output: "json", code: "failed", message: "valid JSON"},
	} {
		t.Run(name, func(t *testing.T) {
			tk, err := loadTask(writeTask(t, tc.script, `{"title": "T", "command": ["./task.sh"]}`))
			if err != nil {
				t.Fatal(err)
			}
			tk.Timeout = tc.timeout
			if tc.output != "" {
				tk.Output = &output{Kind: tc.output}
			}
			_, err = tk.run(t.Context(), map[string]any{}, nil, func(string) {})
			var failure *runError
			if !errors.As(err, &failure) || failure.Code != tc.code || !strings.Contains(failure.Message, tc.message) {
				t.Fatalf("%v", err)
			}
		})
	}

	// Stopping sharing stops the run and everything it started.
	tk, err := loadTask(writeTask(t, "sleep 30 & echo started >&2; wait", `{"title": "T", "command": ["./task.sh"]}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	began := time.Now()
	_, err = tk.run(ctx, map[string]any{}, nil, func(line string) {
		if line == "started" {
			cancel()
		}
	})
	var failure *runError
	if !errors.As(err, &failure) || failure.Code != "stopped" || time.Since(began) > 5*time.Second {
		t.Fatalf("%v after %v", err, time.Since(began))
	}
}

// Nothing a run starts outlives it, even when its command exits first.
func TestRunEndsEverythingItStarted(t *testing.T) {
	tk, err := loadTask(writeTask(t, "sleep 30 & echo $! >&2; echo done", `{"title": "T", "command": ["./task.sh"], "output": {"kind": "text"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var child string
	out, err := tk.run(t.Context(), map[string]any{}, nil, func(line string) { child = line })
	if err != nil || string(out) != "done\n" {
		t.Fatalf("%q %v", out, err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		// ps prints nothing for a reaped process and Z for one not yet reaped.
		state, _ := exec.Command("ps", "-o", "stat=", "-p", child).Output()
		if s := strings.TrimSpace(string(state)); s == "" || strings.HasPrefix(s, "Z") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %s outlived its run", child)
		}
	}
}

// Recipient input fills arguments only; the program is fixed.
func TestProgramTakesNoInput(t *testing.T) {
	dir := filepath.Dir(writeTask(t, "", "{}"))
	for name, script := range map[string]string{"run_{name}": "echo fixed", "run_evil": "echo evil"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "program.json")
	if err := os.WriteFile(path, []byte(`{"title": "T", "command": ["./run_{name}", "{name}"], "inputs": [{"name": "name", "type": "text"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tk, err := loadTask(path)
	if err != nil {
		t.Fatal(err)
	}
	args, problems := tk.check(map[string]any{"name": "evil"})
	if out, err := tk.run(t.Context(), map[string]any{"name": "evil"}, args, func(string) {}); len(problems) != 0 || err != nil || string(out) != "fixed\n" {
		t.Fatalf("%q %v %v", out, err, problems)
	}
}

func TestLinesSplitsAndBounds(t *testing.T) {
	var got []string
	l := &lines{emit: func(line string) { got = append(got, line) }}
	_, _ = l.Write([]byte("one\r\ntw"))
	_, _ = l.Write([]byte("o\n" + strings.Repeat("x", 5000)))
	l.flush()
	if len(got) != 4 || got[0] != "one" || got[1] != "two" || len(got[2]) != 4096 || len(got[3]) != 904 {
		t.Fatalf("%q", got)
	}
}

// Each input reaches the script as the task file says: as an argument, an
// option, a flag, a variable, or a line of stdin. An absent value leaves its
// argument and variable out.
func TestInputsReachTheScriptAsItTakesThem(t *testing.T) {
	t.Setenv("TOKEN", "the owner's")
	tk, err := loadTask(writeTask(t, `for a; do printf '[%s]' "$a"; done; echo; echo "TOKEN=${TOKEN-unset}"; cat`, `{"title": "T",
		"command": ["./task.sh", "{verbose}", "{color}", "{dest}", "--level={level}", "{first}", "{second}", "{third}"],
		"inputs": [
			{"name": "verbose", "type": "boolean", "flag": "-v"},
			{"name": "color", "type": "boolean", "flag": "--color="},
			{"name": "dest", "type": "text", "flag": "--dest"},
			{"name": "level", "type": "number"},
			{"name": "first", "type": "text"}, {"name": "second", "type": "text"}, {"name": "third", "type": "text"},
			{"name": "token", "type": "text", "env": "TOKEN"},
			{"name": "note", "type": "text", "stdin": true}, {"name": "more", "type": "multiline", "stdin": true}
		], "output": {"kind": "text"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		values map[string]any
		want   string
	}{
		{map[string]any{"verbose": true, "color": false, "dest": "/tmp/out", "level": 3.0, "first": "a", "second": "b", "third": "c",
			"token": "s3cret", "note": "hi", "more": "l1\nl2\n"}, "[-v][--color=false][--dest][/tmp/out][--level=3][a][b][c]\nTOKEN=s3cret\nhi\nl1\nl2\n"},
		// The first argument holds the second's place; the absent third is left out.
		{map[string]any{"verbose": false, "color": true, "second": "b"}, "[--color=true][][b]\nTOKEN=unset\n\n\n"},
	} {
		args, problems := tk.check(tc.values)
		if out, err := tk.run(t.Context(), tc.values, args, func(string) {}); len(problems) != 0 || err != nil || string(out) != tc.want {
			t.Errorf("%v: %q %v %v", tc.values, out, err, problems)
		}
	}

	// An empty argument also holds the place of a later one with more text.
	tk, err = loadTask(writeTask(t, `for a; do printf '[%s]' "$a"; done`, `{"title": "T", "command": ["./task.sh", "{first}", "out-{second}"],
		"inputs": [{"name": "first", "type": "text"}, {"name": "second", "type": "text"}], "output": {"kind": "text"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := tk.run(t.Context(), map[string]any{"second": "x"}, map[string][]string{"second": {"x"}}, func(string) {}); err != nil || string(out) != "[][out-x]" {
		t.Errorf("a later argument with more text: %q %v", out, err)
	}

	// A value that starts an argument, alone, after a separate flag, or before
	// more text, cannot look like an option; joined to its flag, or after --, it can.
	tk, err = loadTask(writeTask(t, "", `{"title": "T", "command": ["./task.sh", "{sep}", "{joined}", "{name}.txt", "{a}{b}", "--", "{rest}"], "inputs": [
		{"name": "sep", "type": "text", "flag": "--sep"}, {"name": "joined", "type": "text", "flag": "--joined="}, {"name": "name", "type": "text"},
		{"name": "a", "type": "text"}, {"name": "b", "type": "text"}, {"name": "rest", "type": "text"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range []string{"sep", "name", "a"} {
		if _, problems := tk.check(map[string]any{option: "-x"}); len(problems) != 1 {
			t.Errorf("an option-like %s: %v", option, problems)
		}
	}
	if _, problems := tk.check(map[string]any{"joined": "-x", "b": "-y", "rest": "-z"}); len(problems) != 0 {
		t.Errorf("option-like values that cannot be options: %v", problems)
	}
	values := map[string]any{"a": "", "b": "-rf"}
	args, _ := tk.check(values)
	if _, err := tk.run(t.Context(), values, args, func(string) {}); err == nil || !strings.Contains(err.Error(), "option") {
		t.Errorf("an empty value let the next one start an argument: %v", err)
	}
	// Nor can an empty value expose the command's own dash, unless it is unsafe.
	for unsafe, want := range map[string]string{"false": "option", "true": ""} {
		tk, err := loadTask(writeTask(t, `printf '[%s]' "$@"`, `{"title": "T", "command": ["./task.sh", "{prefix}-{name}"], "inputs": [
			{"name": "prefix", "type": "text", "unsafe": `+unsafe+`}, {"name": "name", "type": "text"}], "output": {"kind": "text"}}`))
		if err != nil {
			t.Fatal(err)
		}
		values := map[string]any{"prefix": "", "name": "rf"}
		args, _ := tk.check(values)
		if out, err := tk.run(t.Context(), values, args, func(string) {}); want == "" && (err != nil || string(out) != "[-rf]") ||
			want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("an empty prefix, unsafe %s: %q %v", unsafe, out, err)
		}
	}

	// A file marked for stdin gives the script its contents.
	path := writeTask(t, "cat", `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "doc", "type": "file", "stdin": true}], "output": {"kind": "text"}}`)
	if tk, err = loadTask(path); err != nil {
		t.Fatal(err)
	}
	doc := filepath.Join(filepath.Dir(path), "doc.txt")
	if err := os.WriteFile(doc, []byte("contents\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := tk.run(t.Context(), map[string]any{"doc": doc}, map[string][]string{"doc": {doc}}, func(string) {}); err != nil || string(out) != "contents\n" {
		t.Fatalf("a file on stdin: %q %v", out, err)
	}
}

// Several values pass apart or joined, a boolean passes the text it says for
// each state, fixed variables reach every run, and an input marked unsafe may
// set any variable and start with -.
func TestScriptsGetValuesTheirOwnWay(t *testing.T) {
	path := writeTask(t, `for a; do printf '[%s]' "$a"; done; echo; echo "TAGS=${TAGS-unset} DEBUG=${DEBUG-unset} MODE=$MODE"; cat`, `{"title": "T",
		"command": ["./task.sh", "{color}", "{verify}", "{tag}", "{only}", "{names}", "out-{day}.csv", "{files}"],
		"env": {"MODE": "prod", "LANG": "C"},
		"inputs": [
			{"name": "color", "type": "boolean", "flag": "--color", "off": "--no-color"},
			{"name": "verify", "type": "boolean", "on": "", "off": "--no-verify"},
			{"name": "debug", "type": "boolean", "env": "DEBUG", "on": "1", "off": ""},
			{"name": "tag", "type": "text", "multiple": true, "flag": "--tag"},
			{"name": "only", "type": "choice", "options": ["a", "b", "c"], "multiple": true, "flag": "--only=", "join": ","},
			{"name": "names", "type": "text", "multiple": true},
			{"name": "day", "type": "date", "multiple": true},
			{"name": "files", "type": "file", "multiple": true},
			{"name": "tags", "type": "text", "multiple": true, "env": "TAGS"},
			{"name": "lines", "type": "text", "multiple": true, "stdin": true}
		], "output": {"kind": "text"}}`)
	tk, err := loadTask(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		values map[string]any
		want   string
	}{
		{map[string]any{"color": true, "verify": true, "debug": true, "tag": []any{"x", "y"}, "only": []any{"a", "c"}, "names": []any{"Ada", "Bo"},
			"day": []any{"2026-10-20", "2026-10-21"}, "tags": []any{"t1", "t2"}, "lines": []any{"one", "two"}},
			"[--color][--tag][x][--tag][y][--only=a,c][Ada][Bo][out-2026-10-20.csv][out-2026-10-21.csv]\nTAGS=t1\nt2 DEBUG=1 MODE=prod\none\ntwo\n"},
		{map[string]any{"color": false, "verify": false, "debug": false, "tags": []any{}},
			"[--no-color][--no-verify]\nTAGS=unset DEBUG=unset MODE=prod\n"},
	} {
		args, problems := tk.check(tc.values)
		if out, err := tk.run(t.Context(), tc.values, args, func(string) {}); len(problems) != 0 || err != nil || string(out) != tc.want {
			t.Errorf("%v: %q %v %v", tc.values, out, err, problems)
		}
	}
	for name, values := range map[string]map[string]any{
		"not a list":    {"tag": "x"},
		"a bad value":   {"day": []any{"2026-10-21", "yesterday"}},
		"not an option": {"only": []any{"d"}},
		"option-like":   {"names": []any{"Ada", "-rf"}},
	} {
		if _, problems := tk.check(values); len(problems) != 1 {
			t.Errorf("%s: %v", name, problems)
		}
	}
	// Joined, the values are one text, which may not start with -.
	if _, problems := tk.check(map[string]any{"only": []any{"a"}, "tag": []any{"x"}}); len(problems) != 0 {
		t.Errorf("%v", problems)
	}
	joined, err := loadTask(writeTask(t, "", `{"title": "T", "command": ["./task.sh", "{a}"], "inputs": [{"name": "a", "type": "text", "multiple": true, "join": "-"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, problems := joined.check(map[string]any{"a": []any{"", "rf"}}); problems["a"] != dashProblem {
		t.Errorf("a joined list starting with -: %v", problems)
	}
	if _, problems := joined.check(map[string]any{"a": []any{"x", "", "y"}}); len(problems) != 0 {
		t.Errorf("a joined list holding -: %v", problems)
	}

	// Several files on stdin arrive one after another.
	path = writeTask(t, `cat; echo "$DOCS"`, `{"title": "T", "command": ["./task.sh"], "env": {"DOCS": ""}, "inputs": [
		{"name": "docs", "type": "file", "multiple": true, "stdin": true, "join": ","}], "output": {"kind": "text"}}`)
	if tk, err = loadTask(path); err != nil {
		t.Fatal(err)
	}
	var docs []string
	for i, text := range []string{"first\n", "second\n"} {
		doc := filepath.Join(filepath.Dir(path), fmt.Sprint(i))
		if err := os.WriteFile(doc, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		docs = append(docs, doc)
	}
	if out, err := tk.run(t.Context(), map[string]any{}, map[string][]string{"docs": docs}, func(string) {}); err != nil || string(out) != "first\nsecond\n\n" {
		t.Fatalf("files on stdin, joined elsewhere: %q %v", out, err)
	}

	// Unsafe, an input may set a variable that changes what programs run, and
	// its text may start with -; a required list needs a value.
	if _, err := loadTask(writeTask(t, "", `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "p", "type": "text", "env": "PATH"}]}`)); err == nil {
		t.Fatal("PATH without unsafe")
	}
	tk, err = loadTask(writeTask(t, `for a; do printf '[%s]' "$a"; done; echo "$BASH_ENV"`, `{"title": "T", "command": ["./task.sh", "{opt}"],
		"inputs": [{"name": "opt", "type": "text", "unsafe": true}, {"name": "env", "type": "text", "env": "BASH_ENV", "unsafe": true},
			{"name": "need", "type": "text", "multiple": true, "required": true, "stdin": true}], "output": {"kind": "text"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, problems := tk.check(map[string]any{"opt": "-x", "need": []any{}}); len(problems) != 1 || problems["need"] == "" {
		t.Errorf("an empty required list: %v", problems)
	}
	values := map[string]any{"opt": "-x", "env": "/tmp/rc", "need": []any{"n"}}
	args, problems := tk.check(values)
	if out, err := tk.run(t.Context(), values, args, func(string) {}); len(problems) != 0 || err != nil || string(out) != "[-x]/tmp/rc\n" {
		t.Errorf("unsafe values: %q %v %v", out, err, problems)
	}
}
