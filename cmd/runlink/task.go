package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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
	Multiple    bool    `json:"multiple,omitempty"` // a list of values
	binding

	whole bool   // the value can start a command argument
	how   string // the setup's answer for how the script gets it
}

// binding is how an input reaches the command besides its placeholders, which
// recipients never see.
type binding struct {
	Flag   string  `json:"flag,omitempty"`   // passes a lone {name} as flag and value
	Env    string  `json:"env,omitempty"`    // a variable set to the value
	Stdin  bool    `json:"stdin,omitempty"`  // the value, or a file's contents, on stdin
	On     *string `json:"on,omitempty"`     // a boolean's text when on: true, or its flag
	Off    *string `json:"off,omitempty"`    // and when off: false, or nothing after a flag
	Join   string  `json:"join,omitempty"`   // passes several values as one text
	Count  bool    `json:"count,omitempty"`  // a number passes its flag that many times
	Stack  bool    `json:"stack,omitempty"`  // and as one argument, -vvv
	Unsafe bool    `json:"unsafe,omitempty"` // lets the value set any variable and start with -
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
	Command  []string          `json:"command"`
	Env      map[string]string `json:"env,omitempty"`      // fixed variables for every run
	Timeout  float64           `json:"timeout,omitempty"`  // seconds
	Progress bool              `json:"progress,omitempty"` // show stderr lines to recipients
	SHA256   string            `json:"sha256,omitempty"`   // the script's when the setup saved the file

	dir string // relative commands run from here
}

const (
	maxTaskFile    = 64 << 10
	maxOutput      = 1 << 20
	maxUpload      = 16 << 20 // largest file a recipient can send, unless the owner says more
	maxProgress    = 500      // lines per run
	defaultTimeout = time.Minute
)

var (
	inputName   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	envName     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	sha256Hex   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	placeholder = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// loadTask reads a task file. An executable loads the task file the guided
// setup stored for it or, without one, makes a task without inputs whose
// output is text.
func loadTask(path string) (*task, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	t := &task{dir: filepath.Dir(abs)}
	if filepath.Ext(path) == ".json" {
		if err := t.read(abs); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	} else {
		stored := storedPath(path)
		switch err := t.read(storedPath(abs)); {
		case errors.Is(err, fs.ErrNotExist):
			t.Title, t.Command, t.Output = filepath.Base(path), append(interpreter(abs), abs), &output{Kind: "text"}
		case err != nil:
			return nil, fmt.Errorf("%s: %w; fix it by hand, or delete it to answer the setup questions again", stored, err)
		}
	}
	if err := t.runnable(); err != nil {
		return nil, err
	}
	return t, nil
}

// runnable says why t's program cannot run, if it cannot.
func (t *task) runnable() error {
	command := t.Command[0]
	if strings.ContainsRune(command, filepath.Separator) && !filepath.IsAbs(command) {
		command = filepath.Join(t.dir, command)
	}
	if _, err := exec.LookPath(command); errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%s is not executable; run chmod +x %[1]s, or give it a #! line", t.Command[0])
	} else if err != nil {
		return fmt.Errorf("cannot run %s: %w", t.Command[0], err)
	}
	return nil
}

// interpreter is the program that runs a script without the executable bit:
// the one its #! line names or, without one, the usual one for its extension.
// It is nil for an executable or an unknown kind of script.
func interpreter(abs string) []string {
	if _, err := exec.LookPath(abs); !errors.Is(err, fs.ErrPermission) {
		return nil
	}
	head := make([]byte, 256) // as much of a #! line as Linux reads
	f, err := os.Open(abs)
	if err != nil {
		return nil
	}
	n, _ := io.ReadFull(f, head)
	f.Close()
	if line, ok := bytes.CutPrefix(head[:n], []byte("#!")); ok {
		line, _, _ = bytes.Cut(line, []byte("\n"))
		program := strings.Fields(string(line)) // macOS splits the rest at spaces
		if runtime.GOOS == "linux" && len(program) > 2 {
			// Linux passes the rest of the line as one argument.
			rest := strings.TrimLeft(string(line), " \t")
			rest = strings.Trim(rest[len(program[0]):], " \t\r")
			program = []string{program[0], rest}
		}
		if len(program) > 0 {
			return program
		}
	}
	program, ok := map[string]string{".sh": "sh", ".bash": "bash", ".zsh": "zsh", ".py": "python3", ".js": "node", ".mjs": "node",
		".cjs": "node", ".ts": "node", ".rb": "ruby", ".pl": "perl", ".php": "php", ".lua": "lua", ".r": "Rscript", ".ps1": "pwsh",
	}[strings.ToLower(filepath.Ext(abs))]
	if !ok {
		return nil
	}
	return []string{program}
}

func (t *task) read(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return t.parse(data)
}

func (t *task) parse(data []byte) error {
	if len(data) > maxTaskFile {
		return errors.New("a task file is at most 64 KiB")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(t); err != nil {
		return located(data, err)
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("a task file holds one JSON object")
	}
	switch {
	case strings.TrimSpace(t.Title) == "":
		return errors.New("title is required")
	case len(t.Command) == 0 || t.Command[0] == "":
		return errors.New("command needs at least the program to run")
	case t.Timeout < 0 || t.Timeout > maxTimeout:
		return errors.New("timeout must be a number of seconds")
	case t.SHA256 != "" && !sha256Hex.MatchString(t.SHA256):
		return errors.New("sha256 must be 64 lowercase hex digits")
	case t.Output != nil && !slices.Contains([]string{"text", "json", "file"}, t.Output.Kind):
		return errors.New("output kind must be text, json or file")
	case t.Output != nil && t.Output.Name != "" && !plainName(t.Output.Name):
		return errors.New("output name must be a plain file name")
	}
	for _, name := range slices.Sorted(maps.Keys(t.Env)) {
		switch {
		case !envName.MatchString(name):
			return fmt.Errorf("env %q: a name is letters, digits and _, not starting with a digit", name)
		case strings.ContainsRune(t.Env[name], 0):
			return fmt.Errorf("env %s: a value cannot hold a null character", name)
		}
	}
	// After --, an argument cannot be an option, and a flag ending in = makes
	// one of the argument after it.
	used, lone, whole := map[string]bool{}, map[string]bool{}, map[string]bool{}
	dashes := false
	for _, arg := range t.Command[1:] {
		dashes = dashes || arg == "--"
		flag, err := t.argFlag(arg)
		if err != nil {
			return err
		}
		for _, m := range placeholder.FindAllStringSubmatchIndex(arg, -1) {
			name := arg[m[2]:m[3]]
			used[name], lone[name] = true, lone[name] || m[1]-m[0] == len(arg)
			whole[name] = whole[name] || m[0] == 0 && !dashes && !strings.HasSuffix(flag, "=")
		}
	}
	for _, group := range t.pairs() {
		if slices.ContainsFunc(group, func(name string) bool { return t.input(name).Type == "file" }) {
			return fmt.Errorf("inputs %s go together row by row in an argument, which a file input cannot", strings.Join(group, " and "))
		}
	}
	seen, envs, stdin, files := map[string]bool{}, map[string]bool{}, 0, 0
	for i := range t.Inputs {
		in := &t.Inputs[i]
		if !inputName.MatchString(in.Name) || seen[in.Name] {
			return fmt.Errorf("input %d needs a unique name of letters, digits and _", i+1)
		}
		seen[in.Name], in.whole = true, whole[in.Name]
		if err := in.validate(); err != nil {
			return fmt.Errorf("input %s: %w", in.Name, err)
		}
		_, fixed := t.Env[in.Env]
		low, _ := in.Min.(float64)
		switch {
		case in.Flag != "" && !used[in.Name]:
			return fmt.Errorf("input %s: a flag needs {%[1]s} in an argument of command", in.Name)
		case in.Flag != "" && (in.Type == "boolean" || in.Count) && !lone[in.Name]:
			return fmt.Errorf("input %s: this flag needs {%[1]s} as a whole argument of command", in.Name)
		case in.Env != "" && !envName.MatchString(in.Env):
			return fmt.Errorf("input %s: env must be letters, digits and _, not starting with a digit", in.Name)
		case in.Env != "" && unsafeEnv(in.Env) && !in.Unsafe:
			return fmt.Errorf(`input %s: %s can make the script run other code; add "unsafe": true to let recipients set it anyway`, in.Name, in.Env)
		case envs[in.Env]:
			return fmt.Errorf("input %s: another input sets %s", in.Name, in.Env)
		case fixed:
			return fmt.Errorf("input %s: env also sets %s for every run", in.Name, in.Env)
		case (in.On != nil || in.Off != nil) && in.Type != "boolean":
			return fmt.Errorf("input %s: on and off are a boolean's texts", in.Name)
		case in.On != nil && in.Flag != "" && !strings.HasSuffix(in.Flag, "="):
			return fmt.Errorf("input %s: a boolean's flag is its text when on; give flag or on, not both", in.Name)
		case in.Multiple && in.Type == "boolean":
			return fmt.Errorf("input %s: a boolean has one value, not several", in.Name)
		case in.Join != "" && !in.Multiple:
			return fmt.Errorf("input %s: join is for several values", in.Name)
		case in.Count && in.Type != "number":
			return fmt.Errorf("input %s: a count is a number, of times to pass its flag", in.Name)
		case in.Count && (in.Flag == "" || strings.HasSuffix(in.Flag, "=")):
			return fmt.Errorf("input %s: a count needs the flag it passes, as -v", in.Name)
		case in.Count && in.Max == nil:
			return fmt.Errorf("input %s: a count needs max, the most times it passes its flag", in.Name)
		case in.Count && (low < 0 || low != math.Trunc(low) || in.Step != math.Trunc(in.Step)):
			return fmt.Errorf("input %s: a count's min and step are whole numbers, and min is 0 or more", in.Name)
		case in.Count && (in.Multiple || in.Env != "" || in.Stdin):
			return fmt.Errorf("input %s: a count passes only its flag, once per one", in.Name)
		case in.Stack && (!in.Count || len(in.Flag) != 2 || in.Flag[1] == '-'):
			return fmt.Errorf("input %s: stack writes a count of a one-letter flag as one argument, as -vvv", in.Name)
		}
		envs[in.Env] = in.Env != ""
		if in.Stdin {
			stdin++
		}
		if in.Stdin && in.Type == "file" {
			files++
		}
	}
	if files > 0 && stdin > 1 {
		return errors.New("a file on stdin takes it alone; pass the other inputs another way")
	}
	for _, in := range t.Inputs {
		if stdin > 0 && !used[in.Name] && in.Env == "" && !in.Stdin {
			return fmt.Errorf("input %s reaches the script nowhere: stdin carries other inputs instead of the JSON", in.Name)
		}
	}
	return nil
}

// unsafeEnv reports whether a variable changes how a shell, an interpreter, or
// the loader starts a program; an input sets one only when marked unsafe.
// ponytail: the usual suspects, not every tool's; the docs say to name a variable only the script reads.
func unsafeEnv(name string) bool {
	return slices.Contains([]string{"PATH", "IFS", "ENV", "BASH_ENV", "SHELLOPTS", "BASHOPTS", "PS4", "PROMPT_COMMAND",
		"NODE_OPTIONS", "PYTHONPATH", "PYTHONSTARTUP", "PYTHONHOME", "PERL5OPT", "PERL5LIB", "RUBYOPT", "RUBYLIB"}, name) ||
		strings.HasPrefix(name, "LD_") || strings.HasPrefix(name, "DYLD_")
}

// validate checks what the runner enforces; gui.js checks the rest of the spec.
func (in *input) validate() error {
	switch in.Type {
	case "text", "multiline", "boolean":
	case "number":
		if !isNumber(in.Min) || !isNumber(in.Max) || in.Step < 0 {
			return errors.New("min and max must be numbers and step positive")
		}
		if in.Count && in.Min == nil {
			in.Min = 0.0 // so the page shows whole numbers from none
		}
		if in.Count && in.Step == 0 {
			in.Step = 1
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
		case in.MaxBytes < 0:
			return errors.New("maxBytes is a number of bytes")
		case in.MaxBytes == 0:
			in.MaxBytes = maxUpload // so the GUI shows the limit
		}
	default:
		return errors.New("type must be text, multiline, number, date, boolean, choice or file")
	}
	var defaults []any
	switch list, ok := in.Default.([]any); {
	case in.Default == nil:
	case !in.Multiple:
		defaults = []any{in.Default}
	case !ok:
		return errors.New("default: several values take a list")
	default:
		defaults = list
	}
	for _, value := range defaults {
		if _, problem := in.check(value); problem != "" {
			return errors.New("default: " + problem)
		}
	}
	return nil
}

// located gives a JSON error the line it is on, for an owner fixing the file.
func located(data []byte, err error) error {
	var syntax *json.SyntaxError
	var kind *json.UnmarshalTypeError
	offset := int64(-1)
	switch {
	case errors.As(err, &syntax):
		offset = syntax.Offset
	case errors.As(err, &kind):
		offset = kind.Offset
	}
	text := strings.TrimPrefix(err.Error(), "json: ")
	if offset < 0 {
		return errors.New(text)
	}
	return fmt.Errorf("line %d: %s", 1+bytes.Count(data[:min(offset, int64(len(data)))], []byte("\n")), text)
}

func isNumber(v any) bool { _, ok := v.(float64); return v == nil || ok }

func isDate(v any) bool {
	s, ok := v.(string)
	_, err := time.Parse(time.DateOnly, s)
	return v == nil || ok && err == nil
}

// dashProblem refuses text that the script could take as an option.
const dashProblem = "Don't start with -."

// check validates recipient values, the trust boundary for task input. It
// returns the texts each input passes, none for an absent value or a boolean
// whose text is empty, or a problem per field.
func (t *task) check(values map[string]any) (args map[string][]string, problems map[string]string) {
	args, problems = map[string][]string{}, map[string]string{}
	for name := range values {
		if t.input(name) == nil {
			problems[name] = "This task has no such field."
		}
	}
	for _, in := range t.Inputs {
		value, ok := values[in.Name]
		list := []any{value}
		if in.Multiple && ok {
			if list, ok = value.([]any); !ok {
				problems[in.Name] = "Enter a list of values."
				continue
			}
		}
		if !ok || len(list) == 0 {
			if in.Required {
				problems[in.Name] = "Enter a value."
			}
			continue
		}
		texts := []string{}
		for _, value := range list {
			text, problem := in.check(value)
			if problem != "" {
				problems[in.Name] = problem
				break
			}
			if text != "" || in.Type != "boolean" {
				texts = append(texts, text)
			}
		}
		// A text that starts an argument would reach the command as an option;
		// joined, the values are one text.
		starts := texts
		if in.Join != "" {
			starts = []string{strings.Join(texts, in.Join)}
		}
		if (in.Type == "text" || in.Type == "multiline") && in.whole && !in.Unsafe &&
			slices.ContainsFunc(starts, func(text string) bool { return strings.HasPrefix(text, "-") }) {
			problems[in.Name] = dashProblem
		}
		if in.Count && problems[in.Name] == "" {
			// check made it a whole number, at most max.
			texts = slices.Repeat([]string{in.Flag}, int(value.(float64)))
			if in.Stack && len(texts) > 0 {
				texts = []string{"-" + strings.Repeat(in.Flag[1:], len(texts))}
			}
		}
		if problems[in.Name] == "" {
			args[in.Name] = texts
		}
	}
	// Values that go together come in rows, each with all of them.
	for _, group := range t.pairs() {
		if rows := len(args[group[0]]); slices.ContainsFunc(group, func(name string) bool { return len(args[name]) != rows }) {
			for _, name := range group {
				problems[name] = cmp.Or(problems[name], "Fill in every field of each row.")
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
		on, ok := value.(bool)
		if !ok {
			return "", "Choose yes or no."
		}
		text, off := in.switchTexts()
		if !on {
			text = off
		}
		return text, ""
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

// switchTexts are what a boolean passes when on and when off: true and false,
// or its flag and nothing, unless on and off say otherwise.
func (in *input) switchTexts() (on, off string) {
	on, off = "true", "false"
	if in.Flag != "" && !strings.HasSuffix(in.Flag, "=") {
		on, off = in.Flag, ""
	}
	return *cmp.Or(in.On, &on), *cmp.Or(in.Off, &off)
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

func (t *task) input(name string) *input {
	if i := slices.IndexFunc(t.Inputs, func(in input) bool { return in.Name == name }); i >= 0 {
		return &t.Inputs[i]
	}
	return nil
}

// shown is what recipients get of a task: its definition, without how inputs
// reach the command, and which inputs they fill in together, as rows.
type shown struct {
	definition
	Pairs [][]string `json:"pairs,omitempty"`
}

func (t *task) public() shown {
	d := t.definition
	d.Inputs = slices.Clone(d.Inputs)
	for i := range d.Inputs {
		d.Inputs[i].binding = binding{}
	}
	return shown{d, t.pairs()}
}

// lists are the inputs of several values that arg holds unjoined. The
// argument repeats once per value, taking the k-th of each, so theirs go
// together.
func (t *task) lists(arg string) []string {
	var names []string
	for _, m := range placeholder.FindAllStringSubmatch(arg, -1) {
		if in := t.input(m[1]); in != nil && in.Multiple && in.Join == "" && !slices.Contains(names, in.Name) {
			names = append(names, in.Name)
		}
	}
	return names
}

// pairs are the inputs whose values go together row by row, as an argument
// holds two or more of them, in input order.
func (t *task) pairs() [][]string {
	var groups [][]string
	for _, arg := range t.Command[1:] {
		group := t.lists(arg)
		if len(group) < 2 {
			continue
		}
		// An input in two arguments joins their groups.
		groups = slices.DeleteFunc(groups, func(g []string) bool {
			if !slices.ContainsFunc(g, func(name string) bool { return slices.Contains(group, name) }) {
				return false
			}
			for _, name := range g {
				if !slices.Contains(group, name) {
					group = append(group, name)
				}
			}
			return true
		})
		groups = append(groups, group)
	}
	order := func(name string) int {
		return slices.IndexFunc(t.Inputs, func(in input) bool { return in.Name == name })
	}
	for _, g := range groups {
		slices.SortFunc(g, func(a, b string) int { return order(a) - order(b) })
	}
	slices.SortFunc(groups, func(a, b []string) int { return order(a[0]) - order(b[0]) })
	return groups
}

// argFlag is the flag passed before arg, or joined to it when it ends in =:
// that of the one input in arg that passes its value after a flag. A
// boolean's flag and a count's are their own texts instead.
func (t *task) argFlag(arg string) (string, error) {
	var flagged *input
	for _, m := range placeholder.FindAllStringSubmatch(arg, -1) {
		in := t.input(m[1])
		if in == nil || in.Flag == "" || in.Type == "boolean" || in.Count || in == flagged {
			continue
		}
		if flagged != nil {
			return "", fmt.Errorf("inputs %s and %s both give the argument %q a flag", flagged.Name, in.Name, arg)
		}
		flagged = in
	}
	if flagged == nil {
		return "", nil
	}
	return flagged.Flag, nil
}

// texts are what in passes for checked values: one text per value, or all
// of them as one when it has a join. ok is false when it has no value.
func texts(in *input, args map[string][]string) (passed []string, ok bool) {
	passed, ok = args[in.Name]
	if in.Join != "" && len(passed) > 0 {
		passed = []string{strings.Join(passed, in.Join)}
	}
	return passed, ok
}

// argv is the command line for checked values, args holding each one's texts.
// The program is fixed; only its arguments take values, in one pass so a
// value's own braces stay literal. An argument holding an input with several
// values repeats once per value. An argument whose value is absent is left
// out, but a lone {name} passed alone stays as empty text while a later
// positional argument is passed, so that one keeps its place. An argument
// holding an input with a flag gets the flag before it, or joined to it.
func (t *task) argv(args map[string][]string) ([]string, error) {
	argv, holes, dashes := []string{t.Command[0]}, []int{}, false
	for _, arg := range t.Command[1:] {
		dashes = dashes || arg == "--"
		if m := placeholder.FindStringSubmatch(arg); m != nil && m[0] == arg {
			if in := t.input(m[1]); in != nil {
				values, ok := texts(in, args)
				switch {
				case !ok && in.Flag == "" && !in.Multiple && in.Type != "boolean":
					argv, holes = append(argv, ""), append(holes, len(argv))
				case in.Flag == "" || in.Type == "boolean" && !strings.HasSuffix(in.Flag, "=") || in.Count:
					// A boolean's text, as its flag, is an option unless it says
					// otherwise; a count's are its flag.
					for _, value := range values {
						if argv = append(argv, value); in.Type != "boolean" && !in.Count || dashes || !strings.HasPrefix(value, "-") {
							holes = holes[:0]
						}
					}
				case strings.HasSuffix(in.Flag, "="):
					for _, value := range values {
						argv = append(argv, in.Flag+value)
					}
				default:
					for _, value := range values {
						argv = append(argv, in.Flag, value)
					}
				}
				continue
			}
		}
		repeats := 1
		if lists := t.lists(arg); len(lists) > 0 {
			repeats = len(args[lists[0]]) // check made theirs as many
		}
		flag, err := t.argFlag(arg)
		if err != nil {
			return nil, err
		}
		for k := range repeats {
			filled, leads, absent := t.fill(arg, args, k)
			if absent {
				continue
			}
			// check refuses such text where a value starts the argument; an
			// empty one before more text could still make an option, unless
			// every input up to the dash is the owner's own text or unsafe.
			if !dashes && !strings.HasSuffix(flag, "=") && !strings.HasPrefix(arg, "-") && strings.HasPrefix(filled, "-") &&
				slices.ContainsFunc(leads, func(in *input) bool { return in.Type != "boolean" && in.Type != "choice" && !in.Unsafe }) {
				return nil, &runError{Code: "failed", Message: "A value would have made an option of an argument."}
			}
			switch {
			case strings.HasSuffix(flag, "="):
				argv = append(argv, flag+filled)
			case flag != "":
				argv = append(argv, flag, filled)
			default:
				if argv = append(argv, filled); !strings.HasPrefix(arg, "-") || dashes && arg != "--" {
					holes = holes[:0] // a positional argument, not an option
				}
			}
		}
	}
	for _, i := range slices.Backward(holes) {
		argv = slices.Delete(argv, i, i+1)
	}
	return argv, nil
}

// fill replaces arg's placeholders with their values, the k-th of those with
// several apart. leads are the inputs up to the result's first character,
// empty ones included, and absent says some input has none.
func (t *task) fill(arg string, args map[string][]string, k int) (filled string, leads []*input, absent bool) {
	var b strings.Builder
	at := 0
	for _, m := range placeholder.FindAllStringSubmatchIndex(arg, -1) {
		b.WriteString(arg[at:m[0]])
		at = m[1]
		in := t.input(arg[m[2]:m[3]])
		if in == nil {
			b.WriteString(arg[m[0]:m[1]])
			continue
		}
		values, _ := texts(in, args)
		if len(values) == 0 {
			absent = true
			continue
		}
		value := values[0]
		if in.Multiple && in.Join == "" {
			if k >= len(values) {
				absent = true
				continue
			}
			value = values[k]
		}
		if b.Len() == 0 {
			leads = append(leads, in)
		}
		b.WriteString(value)
	}
	b.WriteString(arg[at:])
	return b.String(), leads, absent
}

// stdin is what the command reads: the inputs marked for it, files' contents
// or each value on a line, or else all values as one JSON object. done closes
// the files.
func (t *task) stdin(values map[string]any, args map[string][]string) (r io.Reader, done func(), err error) {
	var text strings.Builder
	var files []io.Reader
	var opened []*os.File
	done = func() {
		for _, f := range opened {
			f.Close()
		}
	}
	marked := false
	for _, in := range t.Inputs {
		if !in.Stdin {
			continue
		}
		marked = true
		passed, _ := texts(&in, args)
		if in.Type == "file" { // an absent file is empty stdin
			for _, path := range args[in.Name] {
				f, err := os.Open(path)
				if err != nil {
					done()
					return nil, nil, err
				}
				opened, files = append(opened, f), append(files, f)
			}
			continue
		}
		if !in.Multiple {
			passed = []string{strings.Join(passed, "")} // one line, empty without a value
		}
		for _, value := range passed {
			if text.WriteString(value); !strings.HasSuffix(value, "\n") {
				text.WriteByte('\n')
			}
		}
	}
	switch {
	case len(files) > 0:
		return io.MultiReader(files...), done, nil
	case marked:
		return strings.NewReader(text.String()), done, nil
	}
	data, err := json.Marshal(values)
	return bytes.NewReader(data), done, err
}

// run executes the command for checked values, with args holding each one's
// text, and returns its bounded stdout. Each stderr line goes to line. The
// command and everything it starts run in one process group, which ctx's end
// or the timeout kills.
func (t *task) run(ctx context.Context, values map[string]any, args map[string][]string, line func(string)) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, t.timeout())
	defer cancel()
	stdin, done, err := t.stdin(values, args)
	if err != nil {
		return nil, err
	}
	defer done()
	argv, err := t.argv(args)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Stdin = t.dir, stdin
	setenv := func(name, value string, set bool) {
		if cmd.Env == nil {
			cmd.Env = cmd.Environ() // with PWD as Dir
		}
		cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool { return strings.HasPrefix(kv, name+"=") })
		if set {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(t.Env)) {
		setenv(name, t.Env[name], true)
	}
	for _, in := range t.Inputs {
		// An absent value, or a boolean's empty text, leaves the variable
		// unset, even if the owner's shell sets it; several go a line each.
		if passed, _ := texts(&in, args); in.Env != "" {
			setenv(in.Env, strings.Join(passed, "\n"), len(passed) > 0)
		}
	}
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

// maxTimeout is the longest timeout a time.Duration holds, in seconds.
const maxTimeout = float64(math.MaxInt64 / time.Second)

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
