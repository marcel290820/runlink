package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

// definition is what recipients see of a task, in the form gui.js documents.
type definition struct {
	Title       string  `json:"title"`
	Description string  `json:"description,omitempty"`
	Owner       string  `json:"owner,omitempty"`
	Inputs      []input `json:"inputs,omitempty"`
	Output      *output `json:"output,omitempty"`
}

type input struct {
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Label       string  `json:"label,omitempty"`
	Help        string  `json:"help,omitempty"`
	Placeholder string  `json:"placeholder,omitempty"`
	Required    bool    `json:"required,omitempty"`
	Default     any     `json:"default,omitempty"`
	Min         any     `json:"min,omitempty"`
	Max         any     `json:"max,omitempty"`
	Step        float64 `json:"step,omitempty"`
	MaxLength   int     `json:"maxLength,omitempty"`
	Options     []any   `json:"options,omitempty"`
	Accept      string  `json:"accept,omitempty"`   // a hint for the file picker
	MaxBytes    int64   `json:"maxBytes,omitempty"` // a file's size limit

	whole bool // the input fills a whole command argument
}

type output struct {
	Kind  string `json:"kind"`
	Label string `json:"label,omitempty"`
	Name  string `json:"name,omitempty"` // a file output's download name
}

// task is a task file: the definition plus how the owner's machine runs it,
// which recipients never see.
type task struct {
	definition
	Command  []string `json:"command"`
	Timeout  float64  `json:"timeout,omitempty"`  // seconds
	Progress bool     `json:"progress,omitempty"` // show stderr lines to recipients

	dir string // relative commands run from here
}

const (
	maxTaskFile    = 64 << 10
	maxOutput      = 1 << 20
	maxUpload      = 16 << 20 // largest file a recipient can send
	maxProgress    = 500      // lines per run
	defaultTimeout = time.Minute
)

var (
	inputName   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	placeholder = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// loadTask reads a task file, or makes a task without inputs from an
// executable whose output is text.
func loadTask(path string) (*task, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	t := &task{dir: filepath.Dir(abs)}
	if filepath.Ext(path) != ".json" {
		t.Title, t.Command, t.Output = filepath.Base(path), []string{abs}, &output{Kind: "text"}
	} else if err := t.read(abs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	command := t.Command[0]
	if strings.ContainsRune(command, filepath.Separator) && !filepath.IsAbs(command) {
		command = filepath.Join(t.dir, command)
	}
	if _, err := exec.LookPath(command); err != nil {
		return nil, fmt.Errorf("cannot run %s: %w", t.Command[0], err)
	}
	return t, nil
}

func (t *task) read(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > maxTaskFile {
		return errors.New("a task file is at most 64 KiB")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(t); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("a task file holds one JSON object")
	}
	switch {
	case strings.TrimSpace(t.Title) == "":
		return errors.New("title is required")
	case len(t.Command) == 0 || t.Command[0] == "":
		return errors.New("command needs at least the program to run")
	case t.Timeout < 0 || t.Timeout > 3600:
		return errors.New("timeout is at most 3600 seconds")
	case t.Output != nil && !slices.Contains([]string{"text", "json", "file"}, t.Output.Kind):
		return errors.New("output kind must be text, json or file")
	case t.Output != nil && t.Output.Name != "" && !plainName(t.Output.Name):
		return errors.New("output name must be a plain file name")
	}
	whole := map[string]bool{}
	for _, arg := range t.Command[1:] {
		if m := placeholder.FindStringSubmatch(arg); m != nil && m[0] == arg {
			whole[m[1]] = true
		}
	}
	seen := map[string]bool{}
	for i := range t.Inputs {
		in := &t.Inputs[i]
		if !inputName.MatchString(in.Name) || seen[in.Name] {
			return fmt.Errorf("input %d needs a unique name of letters, digits and _", i+1)
		}
		seen[in.Name], in.whole = true, whole[in.Name]
		if err := in.validate(); err != nil {
			return fmt.Errorf("input %s: %w", in.Name, err)
		}
	}
	return nil
}

// validate checks what the runner enforces; gui.js checks the rest of the spec.
func (in *input) validate() error {
	switch in.Type {
	case "text", "multiline", "boolean":
	case "number":
		if !isNumber(in.Min) || !isNumber(in.Max) || in.Step < 0 {
			return errors.New("min and max must be numbers and step positive")
		}
	case "date":
		if !isDate(in.Min) || !isDate(in.Max) {
			return errors.New("min and max must be YYYY-MM-DD dates")
		}
	case "choice":
		if len(in.Options) == 0 {
			return errors.New("a choice needs options")
		}
		for _, option := range in.Options {
			if o, ok := option.(map[string]any); ok {
				option = o["value"]
			}
			switch option.(type) {
			case string, float64, bool:
			default:
				return errors.New("options must be strings, numbers or booleans, or {value, label}")
			}
		}
	case "file":
		switch {
		case in.Default != nil:
			return errors.New("a file input takes no default")
		case in.MaxBytes < 0 || in.MaxBytes > maxUpload:
			return errors.New("maxBytes is at most 16 MiB")
		case in.MaxBytes == 0:
			in.MaxBytes = maxUpload // so the GUI shows the limit
		}
	default:
		return errors.New("type must be text, multiline, number, date, boolean, choice or file")
	}
	if in.Default != nil {
		if _, problem := in.check(in.Default); problem != "" {
			return errors.New("default: " + problem)
		}
	}
	return nil
}

func isNumber(v any) bool { _, ok := v.(float64); return v == nil || ok }

func isDate(v any) bool {
	s, ok := v.(string)
	_, err := time.Parse(time.DateOnly, s)
	return v == nil || ok && err == nil
}

// check validates recipient values, the trust boundary for task input. It
// returns each input's argument text, empty when absent, or a problem per field.
func (t *task) check(values map[string]any) (args, problems map[string]string) {
	args, problems = map[string]string{}, map[string]string{}
	for name := range values {
		if !slices.ContainsFunc(t.Inputs, func(in input) bool { return in.Name == name }) {
			problems[name] = "This task has no such field."
		}
	}
	for _, in := range t.Inputs {
		value, ok := values[in.Name]
		switch {
		case !ok && in.Required:
			problems[in.Name] = "Enter a value."
		case ok:
			if text, problem := in.check(value); problem != "" {
				problems[in.Name] = problem
			} else {
				args[in.Name] = text
			}
		}
	}
	return args, problems
}

// check returns value's argument text or what is wrong with it, with the
// GUI's wording.
func (in *input) check(value any) (text, problem string) {
	switch in.Type {
	case "text", "multiline":
		s, ok := value.(string)
		switch {
		case !ok:
			return "", "Enter text."
		case in.Required && strings.TrimSpace(s) == "":
			return "", "Enter a value."
		case in.MaxLength > 0 && utf8.RuneCountInString(s) > in.MaxLength:
			return "", fmt.Sprintf("Use %d characters or fewer.", in.MaxLength)
		case strings.ContainsRune(s, 0):
			return "", "Remove the null character."
		case in.whole && strings.HasPrefix(s, "-"):
			// A whole argument starting with - would reach the command as an option.
			return "", "Don't start with -."
		}
		return s, ""
	case "number":
		f, ok := value.(float64)
		low, hasLow := in.Min.(float64)
		high, hasHigh := in.Max.(float64)
		switch {
		case !ok:
			return "", "Enter a number."
		case hasLow && f < low, hasHigh && f > high:
			return "", "Use a number in the allowed range."
		case in.Step > 0 && math.Abs(math.Remainder(f-low, in.Step)) > 1e-9*in.Step:
			return "", fmt.Sprintf("Use steps of %v.", in.Step)
		}
		return argument(f), ""
	case "date":
		s, ok := value.(string)
		low, _ := in.Min.(string)
		high, _ := in.Max.(string)
		switch {
		case !ok || !isDate(s):
			return "", "Choose a date."
		case low != "" && s < low, high != "" && s > high:
			return "", "Choose a date in the allowed range."
		}
		return s, ""
	case "boolean":
		if _, ok := value.(bool); !ok {
			return "", "Choose yes or no."
		}
		return argument(value), ""
	case "choice":
		for _, option := range in.Options {
			if o, ok := option.(map[string]any); ok {
				option = o["value"]
			}
			if option == value {
				return argument(value), ""
			}
		}
		return "", "Choose one of the options."
	case "file":
		// The browser announces a file as {name, size} and sends its bytes once
		// the run is accepted; its argument is the path they are stored at.
		f, ok := value.(map[string]any)
		name, _ := f["name"].(string)
		size, isSize := f["size"].(float64)
		switch {
		case !ok || len(f) != 2 || !plainName(name) || !isSize || size < 0 || size != math.Trunc(size):
			return "", "Choose a file."
		case size > float64(in.MaxBytes):
			return "", fmt.Sprintf("Choose a file of at most %d bytes.", in.MaxBytes)
		}
		return "", ""
	}
	return "", "This field can't be used."
}

// plainName reports whether name can be a file name in a directory of its own.
func plainName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && utf8.ValidString(name) &&
		!strings.ContainsFunc(name, func(r rune) bool { return r == '/' || r == '\\' || unicode.IsControl(r) })
}

// argument is a checked value's text: numbers in plain decimal, booleans as true or false.
func argument(value any) string {
	switch v := value.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return value.(string)
}

// runError is how a run failed, in the codes gui.js shows. Recipients see the
// message; the cause stays on the owner's machine.
type runError struct {
	Code    string            `json:"code"`
	Message string            `json:"message,omitempty"`
	Fields  map[string]string `json:"fields,omitempty"`
	cause   error
}

func (e *runError) Error() string {
	switch {
	case e.cause != nil:
		return e.cause.Error()
	case e.Message != "":
		return e.Message
	case e.Code == "timeout":
		return "It reached its time limit."
	}
	return "Sharing stopped."
}

// run executes the command with placeholders replaced by args and the values as
// JSON on stdin, and returns its bounded stdout. Each stderr line goes to line.
// The command and everything it starts run in one process group, which ctx's end
// or the timeout kills.
func (t *task) run(ctx context.Context, values map[string]any, args map[string]string, line func(string)) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, t.timeout())
	defer cancel()
	// The program is fixed; only its arguments take values, in one pass so a
	// value's own braces stay literal.
	argv := slices.Clone(t.Command)
	for i, arg := range argv[1:] {
		argv[i+1] = placeholder.ReplaceAllStringFunc(arg, func(m string) string {
			name := m[1 : len(m)-1]
			if slices.ContainsFunc(t.Inputs, func(in input) bool { return in.Name == name }) {
				return args[name] // empty for an absent optional input
			}
			return m
		})
	}
	stdin, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Stdin = t.dir, bytes.NewReader(stdin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	tooLarge := false
	cmd.Stdout = writerFunc(func(p []byte) (int, error) {
		if stdout.Len()+len(p) > maxOutput {
			tooLarge = true
			cancel()
			return 0, io.ErrShortWrite
		}
		return stdout.Write(p)
	})
	stderr := &lines{emit: line}
	cmd.Stderr = stderr
	err = cmd.Run()
	if cmd.Process != nil {
		// The run ends with its command: nothing it started outlives it.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	stderr.flush()
	switch {
	case tooLarge:
		return nil, &runError{Code: "failed", Message: "The task's output is larger than 1 MB."}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, &runError{Code: "timeout"}
	case ctx.Err() != nil:
		return nil, &runError{Code: "stopped"}
	case err != nil && !errors.Is(err, exec.ErrWaitDelay): // that one: exit 0, output held open
		return nil, &runError{Code: "failed", cause: err}
	case t.Output != nil && t.Output.Kind == "json" && !json.Valid(stdout.Bytes()):
		return nil, &runError{Code: "failed", Message: "The task's output isn't valid JSON."}
	}
	return stdout.Bytes(), nil
}

func (t *task) timeout() time.Duration {
	if t.Timeout > 0 {
		return time.Duration(t.Timeout * float64(time.Second))
	}
	return defaultTimeout
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// lines splits a stream into lines for emit. A line longer than 4 KiB is cut.
type lines struct {
	buf  []byte
	emit func(string)
}

func (l *lines) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		switch {
		case i >= 0 && i <= 4096:
			l.emit(strings.TrimSuffix(string(l.buf[:i]), "\r"))
			l.buf = l.buf[i+1:]
		case len(l.buf) >= 4096:
			l.emit(string(l.buf[:4096]))
			l.buf = l.buf[4096:]
		default:
			return len(p), nil
		}
	}
}

func (l *lines) flush() {
	if len(l.buf) > 0 {
		l.emit(string(l.buf))
	}
}
