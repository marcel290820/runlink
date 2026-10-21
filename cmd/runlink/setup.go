package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// storedPath is where the guided setup keeps a script's task file: beside it,
// named after it, as greet.sh's greet.runlink.json.
func storedPath(script string) string {
	name := filepath.Base(script)
	return filepath.Join(filepath.Dir(script), cmp.Or(strings.TrimSuffix(name, filepath.Ext(name)), name)+".runlink.json")
}

// taskFor returns the task path shares. A script loads its stored task file.
// Without one, an owner at a terminal answers the setup questions first, as
// with --edit; otherwise the script runs without input. from is the stored file
// when it loaded without questions.
func taskFor(path string, edit bool, a *asker) (t *task, from string, err error) {
	if filepath.Ext(path) == ".json" {
		if edit {
			return nil, "", fmt.Errorf("--edit sets up a script's task file; edit %s by hand", path)
		}
		t, err = loadTask(path)
		return t, "", err
	}
	stored := storedPath(path)
	_, err = os.Stat(stored)
	exists := !errors.Is(err, fs.ErrNotExist)
	switch {
	case exists && !edit:
		t, err = loadTask(path)
		return t, stored, err
	case a == nil && edit:
		return nil, "", errors.New("--edit asks questions, so it needs a terminal")
	case a == nil:
		t, err = loadTask(path)
		return t, "", err
	}
	// Before any question: the script must run, and a stored file must load.
	if t, err = loadTask(path); err != nil {
		return nil, "", err
	}
	draft := &task{definition: definition{Title: suggestedTitle(path), Output: &output{Kind: "text"}}, Timeout: 60}
	if exists {
		// Unlike t, the file as decoded holds only what the owner wrote.
		data, err := os.ReadFile(stored)
		if err != nil {
			return nil, "", err
		}
		if draft = new(task); json.Unmarshal(data, draft) != nil {
			return nil, "", fmt.Errorf("%s changed while loading", stored)
		}
	}
	draft.dir = t.dir
	data, err := a.setup(draft, path, stored, exists)
	if err != nil {
		return nil, "", err
	}
	if err := save(stored, data, exists); err != nil {
		for inner := errors.Unwrap(err); inner != nil; inner = errors.Unwrap(err) {
			err = inner // the cause alone, as in "permission denied"
		}
		return nil, "", fmt.Errorf("could not save %s: %w", stored, err)
	}
	a.show("  " + a.st.sgr(green, a.st.sym("✓", "+")) + " Saved " + escape(stored))
	t, err = loadTask(path)
	return t, "", err
}

// save writes a task file whole or not at all, through a temporary file beside
// it, and replaces an existing one only when told to, keeping its permissions.
// A new file is private to the owner.
func save(path string, data []byte, replace bool) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if info, statErr := os.Stat(path); replace && statErr == nil {
		err = errors.Join(err, f.Chmod(info.Mode().Perm()))
	}
	if err = errors.Join(err, f.Sync(), f.Close()); err != nil {
		return err
	}
	if replace {
		return os.Rename(f.Name(), path)
	}
	// ponytail: a hard link is the portable rename that never replaces; file
	// systems without links (FAT) fail here, where renameat2 or renamex_np would not.
	return os.Link(f.Name(), path)
}

// encodeTask writes t as a task file laid out as the examples are, one field
// and one input per line, and checks it as the loader does.
func encodeTask(t *task) ([]byte, error) {
	raw, err := compact(t)
	var fields map[string]json.RawMessage
	if err == nil {
		err = json.Unmarshal(raw, &fields)
	}
	if err != nil {
		return nil, err
	}
	order := []string{"title", "description", "owner", "command", "inputs", "output", "timeout", "progress"}
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if !slices.Contains(order, name) {
			order = append(order, name)
		}
	}
	var b strings.Builder
	sep := "{\n"
	for _, name := range order {
		value, ok := fields[name]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "%s  %q: ", sep, name)
		sep = ",\n"
		var inputs []json.RawMessage
		if name != "inputs" || json.Unmarshal(value, &inputs) != nil {
			b.WriteString(spaced(value))
			continue
		}
		for i, in := range inputs {
			b.WriteString([]string{"[", ","}[min(i, 1)] + "\n    " + spaced(in))
		}
		b.WriteString("\n  ]")
	}
	b.WriteString("\n}\n")
	data := []byte(b.String())
	return data, (&task{dir: t.dir}).parse(data)
}

func compact(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	err := enc.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), err
}

// spaced spaces out compact JSON as the examples are written:
// { "kind": "text" } and ["warm", "formal"].
func spaced(raw []byte) string {
	var b strings.Builder
	quoted, escaped := false, false
	for _, c := range raw {
		if !quoted && c == '}' {
			b.WriteByte(' ')
		}
		b.WriteByte(c)
		switch {
		case escaped:
			escaped = false
		case quoted && c == '\\':
			escaped = true
		case c == '"':
			quoted = !quoted
		case !quoted && (c == ':' || c == ',' || c == '{'):
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// setup asks for a task file's fields, starting from t, and returns the file
// once the owner confirms it; the loader's checks have passed by then.
func (a *asker) setup(t *task, script, file string, editing bool) ([]byte, error) {
	file = escape(file)
	if editing {
		a.show("", "  Editing "+file+". Enter keeps each answer; Ctrl-C stops without saving.", "")
	} else {
		a.show("", "  "+escape(filepath.Base(script))+" has no task file yet. A few questions give it a form,",
			"  saved as "+file+". Enter takes each suggestion; Ctrl-C stops without saving.", "")
	}
	for {
		if err := a.questions(t, script); err != nil {
			return nil, err
		}
		data, err := encodeTask(t)
		st := a.st
		lines := append(append([]string{""}, a.card(t)...), "", "  "+st.sgr(dim, "Runs  ")+commandShown(t))
		if stdin := stdinShown(t); stdin != "" {
			lines = append(lines, "  "+st.sgr(dim, "Stdin ")+stdin)
		}
		a.show(append(lines, "  "+st.sgr(dim, "Saves ")+file+st.sgr(dim, "; change it by hand or with ")+"runlink share --edit "+commandLine([]string{script}), "")...)
		dot := " " + st.sym("·", "-") + " "
		hint := "Enter shares" + dot + "e changes answers" + dot + "Ctrl-C cancels"
		if err != nil {
			a.show("  " + st.sgr(red, st.sym("✗", "x")+" This would not load: "+err.Error()))
			hint = "e changes answers" + dot + "Ctrl-C cancels"
		}
		for again := false; !again; {
			a.frame([]string{a.prompt("Share now?") + st.sgr(dim, hint)}, -1, 0)
			switch k := a.keys.next(a.ctx); {
			case k.kind == keyCancel:
				return nil, errCanceled
			case k.kind == keyEnter && err == nil:
				a.r.draw(nil, nil, -1, 0)
				return data, nil
			case k.kind == keyRune && unicode.ToLower(k.r) == 'e':
				again = true
				a.show("")
			}
		}
	}
}

// questions asks for each field the setup covers, suggesting t's values, and
// sets them on t. Other fields stay as the owner wrote them.
func (a *asker) questions(t *task, script string) error {
	var err error
	if t.Title, err = a.text("Title", t.Title, "The page's heading.", needed("Give the task a title.")); err != nil {
		return err
	}
	if t.Description, err = a.text("Description", t.Description, "Optional: a sentence under the title.", nil); err != nil {
		return err
	}
	if t.Owner, err = a.text("Owner", t.Owner, `Optional: your name, so the page says "Dana's machine".`, nil); err != nil {
		return err
	}
	for i := range t.Inputs {
		t.Inputs[i].how = passed(t, t.Inputs[i])
	}
	before := t.Inputs
	if t.Inputs, err = a.inputs(t.Inputs); err != nil {
		return err
	}
	// A command the setup made follows the inputs; one written by hand stays.
	if len(t.Command) == 0 || slices.Equal(t.Command, command(script, before)) {
		t.Command = command(script, t.Inputs)
	}
	if err = a.output(t); err != nil {
		return err
	}
	timeout, err := a.text("Timeout", strconv.FormatFloat(t.timeout().Seconds(), 'f', -1, 64),
		"Seconds a run may take, at most 3600.", timeoutProblem)
	if err != nil {
		return err
	}
	t.Timeout, _ = strconv.ParseFloat(timeout, 64) // timeoutProblem parsed it
	t.Progress, err = a.confirm("Progress", "Show recipients each stderr line?", t.Progress)
	return err
}

// command is how the setup runs a script: options and flags first, as getopts
// and Go's flag package need, then arguments, each group in the inputs' order.
func command(script string, inputs []input) []string {
	argv := []string{"./" + filepath.Base(script)}
	for _, how := range []string{"option", "argument"} {
		for _, in := range inputs {
			if in.how == how || how == "option" && in.how == "flag" {
				argv = append(argv, "{"+in.Name+"}")
			}
		}
	}
	return argv
}

// passed is how the setup says in reaches t's script, as in passings.
func passed(t *task, in input) string {
	switch {
	case in.Flag != "" && in.Type == "boolean":
		return "flag"
	case in.Flag != "":
		return "option"
	case in.Env != "":
		return "variable"
	case in.Stdin:
		return "stdin"
	case slices.ContainsFunc(t.Command[1:], func(arg string) bool { return strings.Contains(arg, "{"+in.Name+"}") }):
		return "argument"
	}
	return "json"
}

// passings are the ways in can reach the script, given the inputs before it:
// stdin carries the inputs marked for it or else the JSON of all values, and a
// file there takes it alone.
func passings(in input, others []input) []option {
	flag, env := suggestedFlag(in.Name), strings.ToUpper(in.Name)
	list := []option{{"argument", "a positional argument, in order: $1, $2, ..."}, {"option", flag + " VALUE or " + flag + "=VALUE"}}
	if in.Type == "boolean" {
		list = []option{{"flag", flag + " when on, nothing when off"}, {"argument", "true or false as a positional argument"}}
	} else if in.Type == "file" {
		list[0].about = "its path as a positional argument"
	}
	list = append(list, option{"variable", "an environment variable, " + env + "=VALUE"})
	stdin := slices.ContainsFunc(others, func(o input) bool { return o.how == "stdin" })
	onlyJSON := slices.ContainsFunc(others, func(o input) bool { return o.how == "json" })
	switch {
	case in.Type == "file" && !stdin && !onlyJSON:
		list = append(list, option{"stdin", "its contents as standard input"})
	case in.Type != "file" && !onlyJSON && !slices.ContainsFunc(others, func(o input) bool { return o.how == "stdin" && o.Type == "file" }):
		list = append(list, option{"stdin", "a line of standard input"})
	}
	if !stdin {
		list = append(list, option{"json", "only in the JSON on stdin"})
	}
	return list
}

// pass asks how in reaches the script and sets its binding to match.
func (a *asker) pass(in input, others []input) (input, error) {
	list := passings(in, others)
	how := cmp.Or(in.how, list[0].name)
	at, err := a.choose("  Passed as", list, max(slices.IndexFunc(list, func(o option) bool { return o.name == how }), 0))
	if err != nil {
		return in, err
	}
	// The same answer keeps the input's other ways in, as a file written by
	// hand may give it; another answer replaces them.
	if list[at].name != in.how {
		in.binding = binding{Stdin: list[at].name == "stdin"}
	}
	in.how = list[at].name
	switch in.how {
	case "option":
		in.Flag, err = a.text("  Option", cmp.Or(in.Flag, suggestedFlag(in.Name)),
			"As the script spells it; end it with = to join the value, as in --tone=warm.", flagProblem)
	case "flag":
		in.Flag, err = a.text("  Flag", cmp.Or(in.Flag, suggestedFlag(in.Name)),
			"Passed alone when on; end it with = to pass "+suggestedFlag(in.Name)+"=true or false.", flagProblem)
	case "variable":
		in.Env, err = a.text("  Variable", cmp.Or(in.Env, strings.ToUpper(in.Name)),
			"Set for the run when the field has a value, and unset when it has none.", envProblem(others))
	}
	return in, err
}

// suggestedFlag spells a name as an option: -n, or --dry-run for dry_run.
func suggestedFlag(name string) string {
	if len(name) == 1 {
		return "-" + name
	}
	return "--" + strings.ReplaceAll(name, "_", "-")
}

var inputTypes = []option{
	{"text", "one line"}, {"multiline", "several lines"}, {"number", "a number"}, {"date", "a calendar date"},
	{"boolean", "a yes-or-no switch"}, {"choice", "one of a list you give"}, {"file", "an upload; the script gets its path"},
}

// inputs goes through the current inputs, each kept or dropped, then adds new
// ones until the owner has no more. Each input's answers end as one line.
func (a *asker) inputs(current []input) ([]input, error) {
	var inputs []input
	for _, in := range current {
		a.hold = true
		keep, err := a.confirm("Input", "Keep "+escape(in.Name)+"?", true)
		if err == nil && keep {
			in, err = a.input(in, inputs)
			inputs = append(inputs, in)
		}
		if err != nil {
			return nil, err
		}
		a.recap(inputs, keep)
	}
	for {
		a.hold = true
		question := "Add one?"
		if len(inputs) > 0 {
			question = "Add another?"
		}
		more, err := a.confirm("Input", question, false)
		if err == nil && more {
			var in input
			in, err = a.input(input{Type: "text", Required: true}, inputs)
			inputs = append(inputs, in)
		}
		if err != nil {
			return nil, err
		}
		a.recap(inputs, more)
		if !more {
			break
		}
	}
	if len(inputs) == 0 {
		a.answer("Inputs", a.st.sgr(dim, "none"))
	}
	return inputs, nil
}

// recap replaces the answers held about the last input with one line.
func (a *asker) recap(inputs []input, added bool) {
	a.held, a.hold = nil, false
	if !added {
		return
	}
	label, in := "", inputs[len(inputs)-1]
	if len(inputs) == 1 {
		label = "Inputs"
	}
	about := in.Type
	if in.Type == "choice" {
		about += " of " + optionsText(in.Options)
	}
	if in.Required {
		about += ", required"
	}
	about += ", " + map[string]string{"argument": "argument", "option": in.Flag, "flag": in.Flag, "variable": "$" + in.Env,
		"stdin": "stdin", "json": "JSON only"}[in.how]
	a.answer(label, a.st.sgr(bold, pad(in.Name, 12))+" "+a.st.sgr(dim, escape(about+" "+a.st.sym("·", "-")+" "+fieldLabel(in))))
}

// input asks about one input, starting from in; others are the inputs before it.
func (a *asker) input(in input, others []input) (input, error) {
	// A new input's name comes from its label; a stored one may go without, as
	// the page then shows its name.
	check := needed("Give the field a label.")
	if in.Name != "" {
		check = nil
	}
	label, err := a.text("  Label", in.Label, "What the page shows by the field.", check)
	if err != nil {
		return in, err
	}
	name, err := a.text("  Name", cmp.Or(in.Name, suggestedName(label, others)),
		"Its key in the task file and in the JSON on stdin.", nameProblem(others))
	if err != nil {
		return in, err
	}
	kind, err := a.choose("  Type", inputTypes, max(slices.IndexFunc(inputTypes, func(o option) bool { return o.name == in.Type }), 0))
	if err != nil {
		return in, err
	}
	if inputTypes[kind].name != in.Type {
		// The old type's settings would not fit the new one.
		in = input{Help: in.Help, Required: in.Required && inputTypes[kind].name != "boolean"}
	}
	in.Label, in.Name, in.Type = label, name, inputTypes[kind].name
	if in.Type == "choice" {
		listed := optionsText(in.Options)
		answer, err := a.text("  Options", listed, "Separated by commas, as in warm, formal.", func(answer string) string {
			if answer == listed && len(in.Options) > 0 {
				return "" // unchanged options keep their values, which a list of text may not hold
			}
			return optionsProblem(answer)
		})
		if err != nil {
			return in, err
		}
		if answer != listed || len(in.Options) == 0 {
			in.Options = parseOptions(answer)
			if !slices.Contains(in.Options, in.Default) {
				in.Default = nil
			}
		}
	}
	if in.Type != "boolean" { // a switch always has a value
		if in.Required, err = a.confirm("  Required", "", in.Required); err != nil {
			return in, err
		}
	}
	return a.pass(in, others)
}

var outputKinds = []option{
	{"text", "stdout, shown as text"}, {"json", "stdout, shown as data"},
	{"file", "stdout, offered as a download"}, {"none", "only that the run finished"},
}

func (a *asker) output(t *task) error {
	at := len(outputKinds) - 1
	if t.Output != nil {
		at = max(slices.IndexFunc(outputKinds, func(o option) bool { return o.name == t.Output.Kind }), 0)
	}
	kind, err := a.choose("Output", outputKinds, at)
	if err != nil || outputKinds[kind].name == "none" {
		t.Output = nil
		return err
	}
	out := output{Kind: outputKinds[kind].name}
	if t.Output != nil {
		out.Label, out.Name = t.Output.Label, t.Output.Name
	}
	if out.Label, err = a.text("Output label", out.Label, `Optional: what recipients get, as in "a greeting".`, nil); err != nil {
		return err
	}
	if out.Kind == "file" {
		if out.Name, err = a.text("File name", out.Name, "Optional: the download's name, as in report.csv.", fileNameProblem); err != nil {
			return err
		}
	}
	t.Output = &out
	return nil
}

// card is the form as the recipient page lays it out.
func (a *asker) card(t *task) []string {
	st := a.st
	w := max(min(64, a.r.width()-4), 16) // a narrower terminal wraps the box
	inner := w - 4
	body := append([]string{st.sgr(bold, escape(t.Title))}, wrap(escape(t.Description), inner)...)
	body = append(body, "")
	labels := 0
	for _, in := range t.Inputs {
		labels = max(labels, cols(escape(fieldLabel(in))))
	}
	labels = min(labels, inner/3)
	for _, in := range t.Inputs {
		body = append(body, pad(fit(escape(fieldLabel(in)), labels, st), labels)+"  "+a.control(in))
	}
	if len(t.Inputs) == 0 {
		body = append(body, st.sgr(dim, "This task needs no input."))
	}
	body = append(body, "", st.tint("[ Run ]")+"  "+expect(t)+st.sgr(dim, " "+st.sym("·", "-")+" on "+machine(t)))
	rule := strings.Repeat(st.sym("─", "-"), w-2)
	side := st.sgr(dim, st.sym("│", "|"))
	lines := []string{"  " + st.sgr(dim, st.sym("╭", "+")+rule+st.sym("╮", "+"))}
	for _, line := range body {
		lines = append(lines, "  "+side+" "+pad(fit(line, inner, st), inner)+" "+side)
	}
	return append(lines, "  "+st.sgr(dim, st.sym("╰", "+")+rule+st.sym("╯", "+")))
}

// control sketches an input's control as the page draws it.
func (a *asker) control(in input) string {
	st := a.st
	box := func(text string, n int) string { return st.sgr(dim, "[ "+pad(text, n)+" ]") }
	shown := escape(cmp.Or(plain(in.Default), in.Placeholder))
	optional := !in.Required
	var c string
	switch in.Type {
	case "text":
		c = box(shown, 20)
	case "multiline":
		c = box(cmp.Or(shown, "several lines"), 20)
	case "number":
		c = box(shown, 8)
	case "date":
		c = box(cmp.Or(shown, "yyyy-mm-dd"), 10)
	case "boolean":
		c, optional = st.sgr(dim, "[ off ]"), false
		if in.Default == true {
			c = st.sgr(dim, "[ ") + st.tint("on") + st.sgr(dim, " ]")
		}
	case "choice":
		var labels []string
		chosen := -1
		for i, o := range in.Options {
			value, label := o, ""
			if m, ok := o.(map[string]any); ok {
				value = m["value"]
				label, _ = m["label"].(string)
			}
			if value == in.Default && in.Default != nil {
				chosen = i
			}
			labels = append(labels, escape(cmp.Or(label, plain(value))))
		}
		// As on the page: a preset choice always has a value, and an optional
		// one without a preset offers None.
		if optional = optional && chosen < 0; optional {
			labels, chosen = append([]string{"None"}, labels...), 0
		}
		if len(labels) <= 4 && !slices.ContainsFunc(labels, func(l string) bool { return utf8.RuneCountInString(l) > 16 }) {
			for i, l := range labels {
				labels[i] = st.sgr(dim, l)
				if i == chosen {
					labels[i] = st.tint(l)
				}
			}
			c = st.sgr(dim, "( ") + strings.Join(labels, st.sgr(dim, " | ")) + st.sgr(dim, " )")
		} else if chosen >= 0 {
			c = box(labels[chosen]+" "+st.sym("▾", "v"), 16)
		} else {
			c = box("Choose one "+st.sym("▾", "v"), 16)
		}
	case "file":
		c = box("Choose a file", 20)
		if in.Accept != "" {
			c += st.sgr(dim, " "+escape(in.Accept))
		}
	}
	if optional {
		c += st.sgr(dim, "  optional")
	}
	return c
}

func expect(t *task) string {
	if t.Output == nil {
		return "You'll see when it's done"
	}
	nouns := map[string]string{"text": "text", "json": "data", "file": "a file"}
	return "You'll get " + escape(cmp.Or(strings.TrimSpace(t.Output.Label), nouns[t.Output.Kind]))
}

func machine(t *task) string {
	if owner := strings.TrimSpace(t.Owner); owner != "" {
		return escape(owner) + "'s machine"
	}
	return "the owner's machine"
}

func fieldLabel(in input) string { return cmp.Or(strings.TrimSpace(in.Label), in.Name) }

// plain is a value as the page shows it; nil is empty.
func plain(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case bool:
		return map[bool]string{true: "Yes", false: "No"}[v]
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return fmt.Sprint(value)
}

// commandShown is t's command as the summary shows it, with variables before
// it, flags with their values, and in brackets each part a run can leave out.
func commandShown(t *task) string {
	var parts []string
	// As on the page: a switch, or a choice with a preset, always has a value.
	optional := func(in *input) bool {
		return in != nil && !in.Required && in.Type != "boolean" && (in.Type != "choice" || in.Default == nil)
	}
	for _, in := range t.Inputs {
		if part := commandLine([]string{in.Env + "={" + in.Name + "}"}); in.Env != "" && optional(&in) {
			parts = append(parts, "["+part+"]")
		} else if in.Env != "" {
			parts = append(parts, part)
		}
	}
	parts = append(parts, commandLine(t.Command[:1]))
	for _, arg := range t.Command[1:] {
		part, left := commandLine([]string{arg}), false
		for _, m := range placeholder.FindAllStringSubmatch(arg, -1) {
			left = left || optional(t.input(m[1]))
		}
		if in := t.input(strings.TrimSuffix(strings.TrimPrefix(arg, "{"), "}")); in != nil && in.Flag != "" && arg == "{"+in.Name+"}" {
			switch {
			case strings.HasSuffix(in.Flag, "="):
				part = commandLine([]string{in.Flag + arg})
			case in.Type == "boolean":
				part, left = commandLine([]string{in.Flag}), true
			default:
				part = commandLine([]string{in.Flag, arg})
			}
		}
		if left {
			part = "[" + part + "]"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " ")
}

// stdinShown says what the command reads, if anything.
func stdinShown(t *task) string {
	var marked []string
	for _, in := range t.Inputs {
		if in.Stdin {
			marked = append(marked, "{"+escape(in.Name)+"}")
		}
	}
	switch {
	case len(marked) > 1:
		return strings.Join(marked, " ") + ", a line each"
	case len(marked) == 1:
		return marked[0]
	case len(t.Inputs) > 0:
		return "the values as JSON"
	}
	return ""
}

// commandLine shows a command as a POSIX shell would take it, single-quoting
// each argument that is not plainly safe to paste.
func commandLine(argv []string) string {
	shown := make([]string, len(argv))
	for i, arg := range argv {
		shown[i] = arg
		if arg == "" || strings.ContainsFunc(arg, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_@%+=:./-{}", r))
		}) {
			shown[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
		}
		shown[i] = escape(shown[i])
	}
	return strings.Join(shown, " ")
}

// wrap breaks text into lines of at most n columns at spaces.
func wrap(text string, n int) []string {
	var lines []string
	for _, word := range strings.Fields(text) {
		if last := len(lines) - 1; last >= 0 && cols(lines[last])+1+cols(word) <= n {
			lines[last] += " " + word
		} else {
			lines = append(lines, word)
		}
	}
	return lines
}

// suggestedTitle makes a title of a script's name: backup-photos.sh, Backup photos.
func suggestedTitle(script string) string {
	name := filepath.Base(script)
	title := strings.Join(strings.FieldsFunc(strings.TrimSuffix(name, filepath.Ext(name)), func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || unicode.IsSpace(r)
	}), " ")
	if title == "" {
		return name
	}
	first, size := utf8.DecodeRuneInString(title)
	return string(unicode.ToUpper(first)) + title[size:]
}

// suggestedName makes an input name of its label, Your name, your_name, that
// no other input has.
func suggestedName(label string, others []input) string {
	var b strings.Builder
	for _, r := range strings.ToLower(label) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "_"):
			b.WriteByte('_')
		}
	}
	base := strings.TrimSuffix(b.String(), "_")
	if base == "" || base[0] <= '9' {
		base = strings.TrimSuffix("input_"+base, "_")
	}
	base = base[:min(len(base), 60)]
	name := base
	for i := 2; nameProblem(others)(name) != ""; i++ {
		name = base + "_" + strconv.Itoa(i)
	}
	return name
}

func needed(problem string) func(string) string {
	return func(answer string) string {
		if answer == "" {
			return problem
		}
		return ""
	}
}

func nameProblem(others []input) func(string) string {
	return func(name string) string {
		switch {
		case !inputName.MatchString(name):
			return "Use letters, digits and _, up to 64, not starting with a digit."
		case slices.ContainsFunc(others, func(in input) bool { return in.Name == name }):
			return "Another input has this name."
		}
		return ""
	}
}

func optionsText(options []any) string {
	listed := make([]string, len(options))
	for i, o := range options {
		if m, ok := o.(map[string]any); ok {
			o = m["value"]
		}
		listed[i] = plain(o)
	}
	return strings.Join(listed, ", ")
}

func parseOptions(text string) []any {
	var options []any
	for part := range strings.SplitSeq(text, ",") {
		if part = strings.TrimSpace(part); part != "" {
			options = append(options, part)
		}
	}
	return options
}

func optionsProblem(text string) string {
	options := parseOptions(text)
	for i, o := range options {
		if slices.Contains(options[:i], o) {
			return "List each option once."
		}
	}
	if len(options) == 0 {
		return "Options are needed for a choice."
	}
	return ""
}

func timeoutProblem(text string) string {
	if s, err := strconv.ParseFloat(text, 64); err != nil || !(s > 0 && s <= 3600) {
		return "Use a number of seconds up to 3600."
	}
	return ""
}

func flagProblem(flag string) string {
	switch {
	case flag == "":
		return "Give the option as the script spells it."
	case strings.ContainsFunc(flag, unicode.IsSpace):
		return "Use one word, without spaces."
	}
	return ""
}

func envProblem(others []input) func(string) string {
	return func(name string) string {
		switch {
		case !envName.MatchString(name):
			return "Use letters, digits and _, not starting with a digit."
		case unsafeEnv(name):
			return name + " changes how programs start; pick a name only the script reads."
		case slices.ContainsFunc(others, func(in input) bool { return in.Env == name }):
			return "Another input sets this variable."
		}
		return ""
	}
}

func fileNameProblem(name string) string {
	if name != "" && !plainName(name) {
		return "Use a plain file name, without folders."
	}
	return ""
}
