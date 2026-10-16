package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
		"long timeout":     `{"title": "T", "command": ["./task.sh"], "timeout": 3601}`,
		"output kind":      `{"title": "T", "command": ["./task.sh"], "output": {"kind": "html"}}`,
		"output path":      `{"title": "T", "command": ["./task.sh"], "output": {"kind": "file", "name": "../x"}}`,
		"file default":     `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "f", "type": "file", "default": "x"}]}`,
		"file limit":       `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "f", "type": "file", "maxBytes": 16777217}]}`,
		"output name":      `{"title": "T", "command": ["./task.sh"], "output": {"kind": "file", "name": ".."}}`,
		"input name":       `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a b", "type": "text"}]}`,
		"duplicate input":  `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "text"}, {"name": "a", "type": "text"}]}`,
		"choice options":   `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "choice", "options": [{"value": {}}]}]}`,
		"number bound":     `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "number", "min": "1"}]}`,
		"date bound":       `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "date", "max": "2026-02-30"}]}`,
		"bad default":      `{"title": "T", "command": ["./task.sh"], "inputs": [{"name": "a", "type": "choice", "options": ["x"], "default": "y"}]}`,
	} {
		if _, err := loadTask(writeTask(t, "", file)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "hello.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTask(script); err == nil {
		t.Fatal("a script that is not executable was accepted")
	}
	if err := os.Chmod(script, 0o700); err != nil {
		t.Fatal(err)
	}
	// A script shares directly: no inputs, its output as text.
	tk, err := loadTask(script)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := tk.run(t.Context(), map[string]any{}, nil, func(string) {}); err != nil || string(out) != "hello\n" || tk.Title != "hello.sh" {
		t.Fatalf("%q %v %+v", out, err, tk.definition)
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
	if len(problems) != 0 || args["text"] != "abc" || args["n"] != "1.25" || args["flag"] != "false" || args["pick"] != "2" {
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
