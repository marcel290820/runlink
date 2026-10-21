package main

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// storedPath is where the guided setup keeps a script's task file: beside it,
// named after all of its name, as greet.sh's greet.sh.runlink.json.
func storedPath(script string) string { return script + storedSuffix }

const storedSuffix = ".runlink.json"

// taskFor returns the task path shares. A script loads its stored task file.
// Without one, an owner at a terminal answers the setup questions first, as
// with --edit, starting from another script's file if the owner says it fits;
// otherwise the script runs without input. from is the stored file when it
// loaded without questions. Each script's file is noted in notes, a file of
// notes or "" for none; warn hears what goes wrong with them.
func taskFor(path string, edit bool, a *asker, notes string, warn func(error)) (t *task, from string, err error) {
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
		if t, err = loadTask(path); err == nil {
			// The script's contents now, so it finds its file after a move.
			abs, _ := filepath.Abs(path) // loadTask made it
			if sum, err := digest(abs); err != nil {
				warn(err)
			} else if err := remember(notes, storedPath(abs), sum, ""); err != nil {
				warn(err)
			}
		}
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
	abs, _ := filepath.Abs(path) // loadTask made it
	sum, err := digest(abs)
	if err != nil {
		return nil, "", err
	}
	draft := &task{definition: definition{Title: suggestedTitle(path), Output: &output{Kind: "text"}}, Timeout: 60,
		Command: append(interpreter(abs), "./"+filepath.Base(path))}
	start := stored
	if !exists {
		known, err := recall(notes)
		if err != nil {
			warn(err)
		}
		if start, err = a.adopt(path, matches(abs, sum, known)); err != nil {
			return nil, "", err
		}
	}
	if start != "" {
		// Unlike t, the file as decoded holds only what the owner wrote.
		data, err := os.ReadFile(start)
		if err != nil {
			return nil, "", err
		}
		if draft = new(task); json.Unmarshal(data, draft) != nil {
			return nil, "", fmt.Errorf("%s changed while loading", start)
		}
		if start != stored {
			var found bool
			other := strings.TrimSuffix(filepath.Base(start), storedSuffix)
			if draft.Command, found = retarget(draft.Command, filepath.Dir(start), t.dir, other, filepath.Base(path)); !found {
				a.show("", "  "+escape(filepath.Base(start))+"'s command names no script beside it; check what it runs below, and press c to change it.")
			}
		}
	}
	draft.dir, draft.SHA256 = t.dir, sum
	data, err := a.setup(draft, path, stored, start)
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
	// A file whose script is gone, now this one's, is no other script's.
	adopted := ""
	if _, err := os.Stat(strings.TrimSuffix(start, storedSuffix)); start != stored && errors.Is(err, fs.ErrNotExist) {
		adopted = start
	}
	if err := remember(notes, storedPath(abs), sum, adopted); err != nil {
		warn(err)
	}
	t, err = loadTask(path)
	return t, "", err
}

// digest is the SHA-256 of a file's contents, in hex.
func digest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// notesFile is where share notes each script's task file, with the script's
// SHA-256 at its last share, in the config directory os.UserConfigDir names;
// "" when there is none.
func notesFile(getenv func(string) string) string {
	dir, home := getenv("XDG_CONFIG_HOME"), getenv("HOME")
	switch {
	case runtime.GOOS == "darwin" && home != "":
		dir = filepath.Join(home, "Library", "Application Support")
	case runtime.GOOS == "darwin":
		dir = ""
	case dir == "" && home != "":
		dir = filepath.Join(home, ".config")
	}
	if !filepath.IsAbs(dir) {
		return ""
	}
	return filepath.Join(dir, "runlink", "scripts.json")
}

// recall reads the notes: each task file's path, and its script's SHA-256,
// or "" for a file that a moved script took, which no other script is offered.
func recall(notes string) (map[string]string, error) {
	known := map[string]string{}
	if notes == "" {
		return known, nil
	}
	data, err := os.ReadFile(notes)
	if errors.Is(err, fs.ErrNotExist) {
		return known, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &known)
	}
	if err != nil {
		return map[string]string{}, fmt.Errorf("%s: %w; fix or delete it so moved scripts find their task files", notes, err)
	}
	if known == nil {
		known = map[string]string{} // the notes said null
	}
	return known, nil
}

// remember notes stored as the task file of a script with SHA-256 sum, marks
// adopted, if not "", as taken, and forgets the files that are gone. Shares
// change the notes one at a time, holding a lock beside them.
func remember(notes, stored, sum, adopted string) error {
	if notes == "" {
		return nil
	}
	failed := func(err error) error {
		return fmt.Errorf("could not note %s for when the script moves: %w", stored, err)
	}
	if err := os.MkdirAll(filepath.Dir(notes), 0o700); err != nil {
		return failed(err)
	}
	lock, err := os.OpenFile(notes+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return failed(err)
	}
	defer lock.Close() // and with it the lock
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return failed(err)
	}
	known, err := recall(notes)
	if err != nil {
		return err
	}
	changed := known[stored] != sum
	if was, ok := known[adopted]; adopted != "" && (!ok || was != "") {
		known[adopted], changed = "", true
	}
	for file := range known {
		if _, err := os.Stat(file); errors.Is(err, fs.ErrNotExist) {
			delete(known, file)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	known[stored] = sum
	data, err := json.MarshalIndent(known, "", "  ")
	if err == nil {
		err = save(notes, append(data, '\n'), true)
	}
	if err != nil {
		return failed(err)
	}
	return nil
}

// A match is another script's task file that may fit a script without one:
// the file, the script it was set up for, and why it may fit.
type match struct{ file, script, why string }

// matches are the task files that may still be a script's without one of its
// own: set up for a script with the same contents, wherever known notes them,
// whether that one is gone, as after a move or a rename, or still there, as
// for a copy; or beside it, for a script that is gone. The likeliest come
// first, and those beside it before others.
func matches(abs, sum string, known map[string]string) []match {
	dir := filepath.Dir(abs)
	files := map[string]bool{}
	entries, _ := os.ReadDir(dir) // a folder that cannot be listed has none to offer
	for _, entry := range entries {
		if name, ok := strings.CutSuffix(entry.Name(), storedSuffix); ok && name != "" && !entry.IsDir() {
			files[filepath.Join(dir, entry.Name())] = true
		}
	}
	for file, noted := range known {
		files[file] = files[file] || noted == sum
	}
	var found []match
	for _, file := range slices.Sorted(maps.Keys(files)) {
		// Only a file that loads is offered.
		saved := &task{dir: filepath.Dir(file)}
		data, err := os.ReadFile(file)
		noted, ok := known[file]
		if !files[file] || ok && noted == "" || file == storedPath(abs) || err != nil || saved.parse(data) != nil {
			continue
		}
		script := strings.TrimSuffix(file, storedSuffix)
		_, err = os.Stat(script)
		gone, same := errors.Is(err, fs.ErrNotExist), saved.SHA256 == sum || known[file] == sum
		switch {
		case same && gone:
			found = append(found, match{file, script, "moved"})
		case same:
			found = append(found, match{file, script, "copy"})
		case gone && filepath.Dir(file) == dir:
			found = append(found, match{file, script, "gone"})
		}
	}
	rank := map[string]int{"moved": 0, "copy": 1, "gone": 2}
	away := func(m match) int {
		if filepath.Dir(m.file) != dir {
			return 1
		}
		return 0
	}
	slices.SortStableFunc(found, func(a, b match) int { return cmp.Or(rank[a.why]-rank[b.why], away(a)-away(b)) })
	return found
}

// adopt asks whether another script's task file is this one's, and returns the
// file to start from, or "" to answer the questions from the start.
func (a *asker) adopt(script string, found []match) (string, error) {
	if len(found) == 0 {
		return "", nil
	}
	abs, _ := filepath.Abs(script) // taskFor's loadTask made it
	name := escape(filepath.Base(script))
	a.show("", "  "+name+" has no task file yet, but one was set up for a script that may be this one.", "")
	var list []option
	for _, m := range found {
		shown, there := escape(filepath.Base(m.script)), escape(tilde(filepath.Dir(m.script)))
		about := map[string]string{"copy": "its answers; it has the same contents as " + name,
			"moved": "its answers; it had the same contents and is gone, perhaps renamed",
			"gone":  "its answers; " + shown + " is gone, perhaps renamed"}[m.why]
		if filepath.Dir(m.script) != filepath.Dir(abs) {
			about = map[string]string{"copy": "its answers; the same contents, in " + there,
				"moved": "its answers; same contents, perhaps moved here from " + there}[m.why]
		}
		list = append(list, option{shown, about})
	}
	at, err := a.choose("Start from", append(list, option{"new", "answers of its own, from the start"}), 0)
	if err != nil || at == len(found) {
		return "", err
	}
	return found[at].file, nil
}

// tilde shortens a path in the home directory to start with ~.
func tilde(path string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
			return "~" + string(filepath.Separator) + rest
		}
	}
	return path
}

// retarget points a command taken from another script's task file in from at
// script in dir. The argument naming the other script, bare or as a path and,
// as older files are named, perhaps without its extension, becomes script; or
// else the first path to a file in from, as a bare word such as python3 names
// a program. When that argument is the program and script is not executable,
// script's interpreter runs it. found is false when no argument fits.
func retarget(command []string, from, dir, other, script string) (retargeted []string, found bool) {
	command = slices.Clone(command)
	in := func(arg string) bool {
		path := arg
		if !filepath.IsAbs(path) {
			path = filepath.Join(from, path)
		}
		return !placeholder.MatchString(arg) && filepath.Dir(path) == from
	}
	at := slices.IndexFunc(command, func(arg string) bool {
		name := filepath.Base(arg)
		return in(arg) && (name == other || strings.TrimSuffix(name, filepath.Ext(name)) == other)
	})
	if at < 0 {
		at = slices.IndexFunc(command, func(arg string) bool { return strings.ContainsRune(arg, filepath.Separator) && in(arg) })
	}
	if at < 0 {
		return command, false
	}
	command[at] = "./" + script
	if at == 0 {
		command = append(interpreter(filepath.Join(dir, script)), command...)
	}
	return command, true
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
	order := []string{"title", "description", "owner", "command", "env", "inputs", "output", "timeout", "progress", "sha256"}
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
// once the owner confirms it; the loader's checks have passed by then. from is
// the file t came from: file itself when editing, or another script's.
func (a *asker) setup(t *task, script, file, from string) ([]byte, error) {
	file = escape(file)
	switch from {
	case file:
		a.show("", "  Editing "+file+". Enter keeps each answer; Ctrl-C stops without saving.", "")
	case "":
		a.show("", "  "+escape(filepath.Base(script))+" has no task file yet. A few questions give it a form,",
			"  saved as "+file+". Enter takes each suggestion; Ctrl-C stops without saving.", "")
	default:
		a.show("", "  Starting from "+escape(from)+"; the answers go to "+file+".",
			"  Enter keeps each answer; Ctrl-C stops without saving.", "")
	}
	for ask := true; ; {
		if ask {
			if err := a.questions(t); err != nil {
				return nil, err
			}
		}
		data, err := encodeTask(t)
		if err == nil {
			err = t.runnable()
		}
		st := a.st
		lines := append(append([]string{""}, a.card(t)...), "")
		if risk := risks(t); risk != "" {
			lines = append(lines, "  "+st.sgr(yellow, st.sym("⚠", "!")+" Unsafe by your choice: "+risk))
		}
		lines = append(lines, "  "+st.sgr(dim, "Runs  ")+commandShown(t))
		if stdin := stdinShown(t); stdin != "" {
			lines = append(lines, "  "+st.sgr(dim, "Stdin ")+stdin)
		}
		a.show(append(lines, "  "+st.sgr(dim, "Saves ")+file+st.sgr(dim, "; change it by hand or with ")+"runlink share --edit "+commandLine([]string{script}), "")...)
		dot := " " + st.sym("·", "-") + " "
		hint := "Enter shares" + dot + "e changes answers" + dot + "c edits the command" + dot + "Ctrl-C cancels"
		if err != nil {
			a.show("  " + st.sgr(red, st.sym("✗", "x")+" This would not load: "+err.Error()))
			hint = strings.TrimPrefix(hint, "Enter shares"+dot)
		}
		ask = false
		for again := false; !again; {
			a.frame([]string{a.prompt("Share now?") + st.sgr(dim, hint)}, -1, 0)
			switch k := a.keys.next(a.ctx); {
			case k.kind == keyCancel:
				return nil, errCanceled
			case k.kind == keyEnter && err == nil:
				a.r.draw(nil, nil, -1, 0)
				return data, nil
			case k.kind == keyRune && unicode.ToLower(k.r) == 'e':
				ask, again = true, true
				a.show("")
			case k.kind == keyRune && unicode.ToLower(k.r) == 'c':
				// The summary shows again, without the questions.
				if err := a.command(t); err != nil {
					return nil, err
				}
				again = true
			}
		}
	}
}

// command lets the owner edit the command line itself, for what the questions
// do not cover: a program to run the script with, fixed arguments, an order.
func (a *asker) command(t *task) error {
	line := quoted(t.Command)
	answer, err := a.text("Command", line, "Split as a shell would; {name} takes the input's value.", func(answer string) string {
		switch argv, ok := words(answer); {
		case !ok:
			return `Close the quote, or end the line without a lone \.`
		case len(argv) == 0:
			return "Give the program to run."
		}
		return ""
	})
	if err == nil && answer != line {
		t.Command, _ = words(answer)
	}
	return err
}

// questions asks for each field the setup covers, suggesting t's values, and
// sets them on t. Other fields stay as the owner wrote them.
func (a *asker) questions(t *task) error {
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
		t.Inputs[i].how = how(t, t.Inputs[i])
	}
	if err = a.inputs(t); err != nil {
		return err
	}
	if err = a.variables(t); err != nil {
		return err
	}
	if err = a.output(t); err != nil {
		return err
	}
	timeout, err := a.text("Timeout", strconv.FormatFloat(t.timeout().Seconds(), 'f', -1, 64),
		"Seconds a run may take.", timeoutProblem)
	if err != nil {
		return err
	}
	t.Timeout, _ = strconv.ParseFloat(timeout, 64) // timeoutProblem parsed it
	t.Progress, err = a.confirm("Progress", "Show recipients each stderr line?", t.Progress)
	return err
}

// variables asks for the variables every run gets, written as a shell would
// take them.
func (a *asker) variables(t *task) error {
	shown := envLine(t.Env)
	answer, err := a.text("Variables", shown, "Optional: set for every run, as in LANG=C MODE=prod.", func(answer string) string {
		_, problem := parseEnv(answer, t.Inputs)
		return problem
	})
	if err == nil && answer != shown {
		t.Env, _ = parseEnv(answer, t.Inputs)
	}
	return err
}

// how is how the setup says in reaches t's script, as in passings.
func how(t *task, in input) string {
	on, off := switchArgs(in)
	switch {
	case in.Count:
		return "count"
	case in.Type == "boolean" && in.Flag != "":
		return "flag"
	case in.Flag != "" && lone(t.Command, in.Name) > 0:
		return "option"
	case in.Flag != "":
		return "part"
	case in.Env != "":
		return "variable"
	case in.Stdin:
		return "stdin"
	case in.Type == "boolean" && lone(t.Command, in.Name) > 0 && (strings.HasPrefix(on, "-") || strings.HasPrefix(off, "-")):
		return "flag"
	case lone(t.Command, in.Name) > 0:
		return "argument"
	case placed(t.Command, in.Name) > 0:
		return "part"
	}
	return "json"
}

// passings are the ways in can reach the script, given the inputs before it:
// stdin carries the inputs marked for it or else the JSON of all values, and
// files there take it alone.
func passings(in input, others []input) []option {
	flag, env := suggestedFlag(in.Name), strings.ToUpper(in.Name)
	list := []option{{"argument", "a positional argument: $1, $2, ..."}, {"option", flag + " VALUE or " + flag + "=VALUE"}}
	switch {
	case in.Type == "boolean":
		list = []option{{"flag", flag + " when on; nothing, or another flag, when off"}, {"argument", "a positional argument, true or false"}}
	case in.Type == "file" && in.Multiple:
		list[0].about = "their paths as positional arguments"
	case in.Type == "file":
		list[0].about = "its path as a positional argument"
	case in.Multiple:
		list[0].about = "positional arguments, one per value"
	}
	if in.Type == "number" && !in.Multiple {
		list = append(list, option{"count", countFlag(in) + " once per one, as in -v -v -v"})
	}
	list = append(list, option{"part", "inside an argument you write, as in out-{" + in.Name + "}.csv or {a}:{b}"},
		option{"variable", "an environment variable, " + env + "=VALUE"})
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
	if in.Multiple {
		plural := strings.NewReplacer("its contents", "their contents, one after another", "a line of", "lines of")
		for i := range list {
			list[i].about = plural.Replace(list[i].about)
		}
	}
	return list
}

// pass asks how in reaches the script, sets its binding to match, and puts
// its placeholder where the answer needs it in t's command. others are the
// inputs before it, and later those still to come.
func (a *asker) pass(t *task, in input, others, later []input) (input, error) {
	list := passings(in, others)
	at, err := a.choose("  Passed as", list, max(slices.IndexFunc(list, func(o option) bool { return o.name == cmp.Or(in.how, list[0].name) }), 0))
	if err != nil {
		return in, err
	}
	// The same answer keeps the input's other ways in, as a file written by
	// hand may give it; another answer replaces them.
	if list[at].name != in.how {
		in.binding = binding{Stdin: list[at].name == "stdin"}
		t.Command = unplace(t.Command, in.Name)
	}
	in.how = list[at].name
	// A positional argument takes an input's value without a flag before it.
	arguments := func(name string) bool {
		o := slices.IndexFunc(others, func(o input) bool { return o.Name == name })
		if j := slices.IndexFunc(later, func(o input) bool { return o.Name == name }); o < 0 && j >= 0 {
			return later[j].how == "argument" || later[j].how == "part" && later[j].Flag == ""
		}
		return o >= 0 && (others[o].how == "argument" || others[o].how == "part" && others[o].Flag == "")
	}
	switch in.how {
	case "argument":
		err = a.position(t, in.Name, arguments)
	case "option", "flag", "count":
		// Options go before the positional arguments, as getopts needs.
		if lone(t.Command, in.Name) < 0 {
			t.Command = slices.Insert(t.Command, append(positions(t.Command, arguments), len(t.Command))[0], "{"+in.Name+"}")
		}
		switch in.how {
		case "option":
			in.Flag, err = a.text("  Option", cmp.Or(in.Flag, suggestedFlag(in.Name)),
				"As the script spells it; end it with = to join the value, as in --tone=warm.", flagProblem)
		case "count":
			err = a.count(&in)
		}
	case "part":
		err = a.part(t, &in, arguments)
	case "variable":
		err = a.variable(&in, others)
	}
	if err == nil && in.Type == "boolean" && in.how != "json" {
		err = a.switches(&in)
	}
	if err == nil && in.Multiple && in.how != "json" && (in.how != "stdin" || in.Type != "file") {
		err = a.join(&in)
	}
	return in, err
}

// countFlag suggests the flag a count passes: -v for verbose.
func countFlag(in input) string {
	if in.Flag != "" {
		return in.Flag
	}
	if in.Name[0] == '_' {
		return suggestedFlag(in.Name)
	}
	return "-" + strings.ToLower(in.Name[:1])
}

// count asks for the flag a count passes once per one, whether a one-letter
// flag stacks, as -vvv, and the most times a recipient can pass it.
func (a *asker) count(in *input) error {
	flag, err := a.text("  Flag", countFlag(*in), "Passed once per one, as the script spells it.", func(flag string) string {
		if strings.HasSuffix(flag, "=") {
			return "A count passes the flag alone; leave out the =."
		}
		return flagProblem(flag)
	})
	if err != nil {
		return err
	}
	stacks := len(flag) == 2 && flag[0] == '-' && flag[1] != '-'
	in.Flag, in.Count, in.Stack = flag, true, in.Stack && stacks
	if stacks {
		letter := flag[1:]
		at, err := a.choose("  Written", []option{{"apart", flag + " " + flag + " " + flag}, {"stacked", flag + letter + letter}}, map[bool]int{true: 1}[in.Stack])
		if err != nil {
			return err
		}
		in.Stack = at == 1
	}
	most, err := a.text("  At most", cmp.Or(plain(in.Max), "3"), "The most times a recipient can pass it.", func(most string) string {
		if n, err := strconv.Atoi(most); err != nil || n < 0 {
			return "Use a whole number."
		}
		return ""
	})
	if n, _ := strconv.Atoi(most); err == nil && most != plain(in.Max) {
		in.Max = float64(n)
	}
	return err
}

// part asks for the argument in's value goes inside, written as the script
// takes it, and an option to pass before it. Without one it is positional, and
// goes where the owner says; with one it goes before those, as options do.
// Another input's {name} in it may be one still to come.
func (a *asker) part(t *task, in *input, arguments func(string) bool) error {
	self, current := placed(t.Command, in.Name), "{"+in.Name+"}"
	if self > 0 {
		current = t.Command[self]
	}
	arg, err := a.line("  Argument", current, "As the script takes it: {"+in.Name+"} for the value, with text or other inputs' {names}.",
		func(arg string) string {
			if !holds(arg, in.Name) {
				return "Put {" + in.Name + "} in it."
			}
			return ""
		}, false)
	if err != nil {
		return err
	}
	flag, err := a.text("  Option", in.Flag, "Optional: passed before the argument, as in -p; end it with = to join them.", func(flag string) string {
		if flag == "" {
			return ""
		}
		return flagProblem(flag)
	})
	if err != nil {
		return err
	}
	in.Flag = flag
	switch {
	case self > 0:
		t.Command[self] = arg
	case flag != "":
		t.Command = slices.Insert(t.Command, append(positions(t.Command, arguments), len(t.Command))[0], arg)
	default:
		t.Command = append(t.Command, arg)
	}
	if flag != "" {
		return nil
	}
	return a.position(t, in.Name, arguments)
}

// variable asks for in's variable. One that can change what the script runs
// is let through only when the owner says so, which marks in unsafe.
func (a *asker) variable(in *input, others []input) error {
	for {
		name, err := a.text("  Variable", cmp.Or(in.Env, strings.ToUpper(in.Name)),
			"Set for the run when the field has a value, and unset when it has none.", envProblem(others))
		if err != nil {
			return err
		}
		if !unsafeEnv(name) || in.Unsafe && name == in.Env {
			in.Env = name
			return nil
		}
		allow, err := a.confirm("  Unsafe", escape(name)+" lets recipients run other code. Allow?", false)
		if err != nil || allow {
			in.Env, in.Unsafe = name, allow
			return err
		}
	}
}

// switches asks what a boolean passes when on and when off: whole arguments
// for a flag, or else its texts.
func (a *asker) switches(in *input) error {
	flag := in.how == "flag"
	on, off := switchArgs(*in)
	if flag && in.Flag == "" && in.On == nil {
		on, off = suggestedFlag(in.Name), "" // newly a flag
	}
	hints := [2]string{"What the script gets when on, as in yes or 1.", "And when off; empty passes nothing, or unsets the variable."}
	if flag {
		hints = [2]string{"The flag passed when on; empty passes nothing.",
			"Optional: a flag passed when off, as in --no-" + strings.TrimLeft(suggestedFlag(in.Name), "-") + "."}
	}
	on, err := a.text("  When on", on, hints[0], nil)
	if err != nil {
		return err
	}
	off, err = a.text("  When off", off, hints[1], func(off string) string {
		if flag && on == "" && off == "" {
			return "Give a flag for on, off, or both."
		}
		return ""
	})
	if err != nil {
		return err
	}
	if wasOn, wasOff := switchArgs(*in); on == wasOn && off == wasOff {
		return nil // as stored, however the file spells it
	}
	in.Flag, in.On, in.Off = "", nil, nil
	if flag && on != "" && !strings.HasSuffix(on, "=") {
		in.Flag = on
	}
	if defaultOn, _ := in.switchTexts(); on != defaultOn {
		in.On = &on
	}
	if _, defaultOff := in.switchTexts(); off != defaultOff {
		in.Off = &off
	}
	return nil
}

// switchArgs are the arguments a boolean passes when on and off.
func switchArgs(in input) (on, off string) {
	on, off = in.switchTexts()
	if strings.HasSuffix(in.Flag, "=") {
		for _, text := range []*string{&on, &off} {
			if *text != "" {
				*text = in.Flag + *text
			}
		}
	}
	return on, off
}

// join asks how several values reach the script: apart, or as one text.
func (a *asker) join(in *input) error {
	apart := map[string]string{"argument": "each value its own argument", "option": "the option before each value",
		"variable": "each value on a line of its own", "stdin": "each value on a line of its own",
		"part": "the argument once per value, beside the others' in it"}[in.how]
	list := []option{{"apart", apart}, {"comma", "one text, as in a,b"}, {"space", "one text, as in a b"}, {"other", "one text, with what you type between values"}}
	at := slices.Index([]string{"", ",", " "}, in.Join)
	if at < 0 {
		at = 3
	}
	at, err := a.choose("  Values", list, at)
	if err != nil {
		return err
	}
	if at < 3 {
		in.Join = []string{"", ",", " "}[at]
		return nil
	}
	// Spaces count here, and a saved separator is suggested exactly as it is.
	in.Join, err = a.line("  Between", cmp.Or(in.Join, ";"), "Put between the values, as in ; or | with spaces.",
		needed("Give the text between values."), false)
	return err
}

// position asks where the argument holding name goes among the other
// positional arguments of t's command, and moves it there.
func (a *asker) position(t *task, name string, arguments func(string) bool) error {
	self, text := placed(t.Command, name), "{"+name+"}"
	if self > 0 {
		text = t.Command[self]
	}
	at := slices.DeleteFunc(positions(t.Command, arguments), func(i int) bool { return i == self })
	if len(at) == 0 {
		if self < 0 {
			t.Command = append(t.Command, text)
		}
		return nil
	}
	current := len(at)
	if self > 0 {
		current = len(slices.DeleteFunc(slices.Clone(at), func(i int) bool { return i > self }))
	}
	list := make([]option, len(at)+1)
	for k := range list {
		about := "before " + t.Command[at[0]]
		switch {
		case k == len(at):
			about = "after " + t.Command[at[k-1]]
		case k > 0:
			about = "between " + t.Command[at[k-1]] + " and " + t.Command[at[k]]
		}
		list[k] = option{ordinal(k + 1), escape(about)}
	}
	chosen, err := a.choose("  Position", list, current)
	if err != nil || chosen == current && self > 0 {
		return err // where it was, the command stays as it is
	}
	if self > 0 {
		t.Command = slices.Delete(t.Command, self, self+1)
	}
	at = positions(t.Command, arguments)
	where := at[len(at)-1] + 1
	if chosen < len(at) {
		where = at[chosen]
	}
	t.Command = slices.Insert(t.Command, where, text)
	return nil
}

func ordinal(n int) string {
	if suffix, ok := map[int]string{1: "st", 2: "nd", 3: "rd"}[n%10]; ok && n%100/10 != 1 {
		return strconv.Itoa(n) + suffix
	}
	return strconv.Itoa(n) + "th"
}

// lone is the index of {name} as a whole argument of command, or -1.
func lone(command []string, name string) int {
	if i := slices.Index(command[1:], "{"+name+"}"); i >= 0 {
		return i + 1
	}
	return -1
}

// placed is the index of the first argument of command holding name, or -1.
func placed(command []string, name string) int {
	if i := slices.IndexFunc(command[1:], func(arg string) bool { return holds(arg, name) }); i >= 0 {
		return i + 1
	}
	return -1
}

// holds reports whether arg takes name's value.
func holds(arg, name string) bool {
	return slices.ContainsFunc(placeholder.FindAllStringSubmatch(arg, -1), func(m []string) bool { return m[1] == name })
}

// unplace takes name's value out of command: an argument of its own goes,
// and one holding other {names} keeps them.
func unplace(command []string, name string) []string {
	kept := command[:1:1]
	for _, arg := range command[1:] {
		rest := strings.ReplaceAll(arg, "{"+name+"}", "")
		if rest == arg || placeholder.MatchString(rest) {
			kept = append(kept, rest)
		}
	}
	return kept
}

// positions are the indexes in command of the positional arguments: those
// holding an input's value that arguments says is one, unless they start
// with -, as an option does.
func positions(command []string, arguments func(string) bool) []int {
	var at []int
	for i, arg := range command {
		if i > 0 && !strings.HasPrefix(arg, "-") &&
			slices.ContainsFunc(placeholder.FindAllStringSubmatch(arg, -1), func(m []string) bool { return arguments(m[1]) }) {
			at = append(at, i)
		}
	}
	return at
}

// rename makes the arguments of command that take old's value take new's.
func rename(command []string, old, new string) []string {
	command = slices.Clone(command)
	for i := 1; i < len(command); i++ {
		command[i] = placeholder.ReplaceAllStringFunc(command[i], func(m string) string {
			if m == "{"+old+"}" {
				return "{" + new + "}"
			}
			return m
		})
	}
	return command
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

// inputs goes through t's inputs, each kept or dropped, then adds new ones
// until the owner has no more, keeping t's command in step. Each input's
// answers end as one line.
func (a *asker) inputs(t *task) error {
	current := t.Inputs
	t.Inputs = nil
	for i, in := range current {
		a.hold = true
		keep, err := a.confirm("Input", "Keep "+escape(in.Name)+"?", true)
		switch {
		case err == nil && keep:
			in, err = a.input(t, in, current[i+1:])
			t.Inputs = append(t.Inputs, in)
		case err == nil:
			t.Command = unplace(t.Command, in.Name)
		}
		if err != nil {
			return err
		}
		a.recap(t, keep)
	}
	for {
		a.hold = true
		question := "Add one?"
		if len(t.Inputs) > 0 {
			question = "Add another?"
		}
		more, err := a.confirm("Input", question, false)
		if err == nil && more {
			var in input
			in, err = a.input(t, input{Type: "text", Required: true}, nil)
			t.Inputs = append(t.Inputs, in)
		}
		if err != nil {
			return err
		}
		a.recap(t, more)
		if !more {
			break
		}
	}
	if len(t.Inputs) == 0 {
		a.answer("Inputs", a.st.sgr(dim, "none"))
	}
	return nil
}

// recap replaces the answers held about t's last input with one line.
func (a *asker) recap(t *task, added bool) {
	a.held, a.hold = nil, false
	if !added {
		return
	}
	label, in := "", t.Inputs[len(t.Inputs)-1]
	if len(t.Inputs) == 1 {
		label = "Inputs"
	}
	about := in.Type
	if in.Type == "choice" {
		about += " of " + optionsText(in.Options)
	}
	if in.Multiple {
		about += ", several"
	}
	if in.Required {
		about += ", required"
	}
	on, off := switchArgs(in)
	shown := ""
	if at := placed(t.Command, in.Name); at > 0 {
		shown = argShown(t, t.Command[at])
	}
	about += ", " + map[string]string{"argument": "argument", "option": in.Flag, "flag": strings.Trim(on+"|"+off, "|"), "count": shown,
		"variable": "$" + in.Env, "stdin": "stdin", "json": "JSON only", "part": shown}[in.how]
	for _, group := range t.pairs() {
		if i := slices.Index(group, in.Name); i >= 0 {
			about += ", in rows with " + strings.Join(slices.Delete(slices.Clone(group), i, i+1), " and ")
		}
	}
	line := a.st.sgr(bold, pad(in.Name, 12)) + " " + a.st.sgr(dim, escape(about))
	if in.Unsafe {
		line += a.st.sgr(yellow, ", unsafe")
	}
	a.answer(label, line+a.st.sgr(dim, " "+a.st.sym("·", "-")+" "+escape(fieldLabel(in))))
}

// input asks about one input, starting from in, and keeps t's command in step;
// later are the inputs still to come.
func (a *asker) input(t *task, in input, later []input) (input, error) {
	others := t.Inputs
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
		"Its key in the task file and in the JSON on stdin.", nameProblem(append(slices.Clone(others), later...)))
	if err != nil {
		return in, err
	}
	if in.Name != "" && name != in.Name {
		t.Command = rename(t.Command, in.Name, name)
	}
	kind, err := a.choose("  Type", inputTypes, max(slices.IndexFunc(inputTypes, func(o option) bool { return o.name == in.Type }), 0))
	if err != nil {
		return in, err
	}
	if inputTypes[kind].name != in.Type {
		// The old type's settings would not fit the new one; how it is passed may.
		in = input{Help: in.Help, Required: in.Required && inputTypes[kind].name != "boolean", how: in.how,
			binding: binding{Flag: in.Flag, Env: in.Env, Stdin: in.Stdin, Unsafe: in.Unsafe}}
		if in.how == "flag" || in.how == "option" || in.how == "count" {
			in.how, in.Flag = "", "" // a boolean's flag, an option and a count differ
		}
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
	if in.Type != "boolean" { // a switch always has one value
		several, err := a.confirm("  Several", "values?", in.Multiple)
		if err != nil {
			return in, err
		}
		if several != in.Multiple {
			// A default changes shape with the field: one value, or a list of one.
			if list, ok := in.Default.([]any); ok {
				in.Default = nil
				if len(list) > 0 {
					in.Default = list[0]
				}
			} else if in.Default != nil {
				in.Default = []any{in.Default}
			}
			in.Multiple, in.Join = several, ""
		}
		if in.Required, err = a.confirm("  Required", "", in.Required); err != nil {
			return in, err
		}
	}
	if in.how == "" && placed(t.Command, in.Name) > 0 {
		in.how = how(t, in) // an argument written for another input holds it already
	}
	return a.pass(t, in, others, later)
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
	// Inputs that go together show as one field of rows, where the first is.
	type field struct {
		label string
		in    input
		group []string
	}
	var fields []field
	groups := t.pairs()
	for _, in := range t.Inputs {
		f := field{escape(fieldLabel(in)), in, nil}
		if g := slices.IndexFunc(groups, func(g []string) bool { return slices.Contains(g, in.Name) }); g >= 0 {
			if groups[g][0] != in.Name {
				continue
			}
			var names []string
			for _, name := range groups[g] {
				names = append(names, escape(fieldLabel(*t.input(name))))
			}
			f.label, f.group = strings.Join(names, st.sym(" · ", ", ")), groups[g]
		}
		fields = append(fields, f)
	}
	labels := 0
	for _, f := range fields {
		labels = max(labels, cols(f.label))
	}
	labels = min(labels, inner/3)
	for _, f := range fields {
		control := a.control(f.in)
		if f.group != nil {
			// Each cell as wide as the rest of the line allows.
			more, cells := " + more", []string{}
			width := max(3, (inner-labels-2-len(more))/len(f.group)-5)
			for _, name := range f.group {
				cells = append(cells, a.cell(*t.input(name), width))
			}
			control = strings.Join(cells, " ") + st.sgr(dim, more)
		}
		body = append(body, pad(fit(f.label, labels, st), labels)+"  "+control)
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
	switch {
	case in.Multiple && in.Type == "choice":
		// As on the page: a box for each option, the preset ones ticked.
		var boxes []string
		for _, o := range in.Options {
			value, label := o, ""
			if m, ok := o.(map[string]any); ok {
				value = m["value"]
				label, _ = m["label"].(string)
			}
			tick := st.sgr(dim, st.sym("☐", "[ ]"))
			if list, _ := in.Default.([]any); slices.Contains(list, value) {
				tick = st.tint(st.sym("☑", "[x]"))
			}
			boxes = append(boxes, tick+" "+st.sgr(dim, escape(cmp.Or(label, plain(value)))))
		}
		c = strings.Join(boxes, "  ")
	case in.Multiple && in.Type == "file":
		c = box("Choose files", 20)
	case in.Multiple:
		var values []string
		list, _ := in.Default.([]any)
		for _, v := range list {
			values = append(values, plain(v))
		}
		c = box(escape(cmp.Or(strings.Join(values, ", "), in.Placeholder)), 20) + st.sgr(dim, " + more")
	}
	kind := in.Type
	if c != "" && kind != "file" {
		kind = "" // sketched above
	}
	switch kind {
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
		c = cmp.Or(c, box("Choose a file", 20))
		if in.Accept != "" {
			c += st.sgr(dim, " "+escape(in.Accept))
		}
	}
	if optional {
		c += st.sgr(dim, "  optional")
	}
	return c
}

// cell sketches one value, n columns wide, of an input that goes in rows
// with others.
func (a *asker) cell(in input, n int) string {
	shown := escape(in.Placeholder)
	if list, _ := in.Default.([]any); len(list) > 0 {
		shown = escape(plain(list[0]))
	}
	if in.Type == "choice" {
		shown = pad(fit(shown, n-2, a.st), n-2) + " " + a.st.sym("▾", "v")
	}
	return a.st.sgr(dim, "[ "+pad(fit(shown, n, a.st), n)+" ]")
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
// it and each argument as argShown has it.
func commandShown(t *task) string {
	var parts []string
	if env := envLine(t.Env); env != "" {
		parts = append(parts, escape(env))
	}
	for _, in := range t.Inputs {
		if part := commandLine([]string{in.Env + "={" + in.Name + "}"}); in.Env != "" && omissible(&in) {
			parts = append(parts, "["+part+"]")
		} else if in.Env != "" {
			parts = append(parts, part)
		}
	}
	parts = append(parts, commandLine(t.Command[:1]))
	for _, arg := range t.Command[1:] {
		parts = append(parts, argShown(t, arg))
	}
	return strings.Join(parts, " ")
}

// omissible reports whether a run can pass in without a value or text. As on
// the page, a switch or a choice with a preset always has a value, though a
// boolean's empty text passes nothing, and a count of none passes nothing.
func omissible(in *input) bool {
	if in == nil {
		return false
	}
	on, off := in.switchTexts()
	return in.Count || in.Type == "boolean" && (on == "" || off == "") ||
		!in.Required && in.Type != "boolean" && (in.Type != "choice" || in.Default == nil)
}

// argShown is an argument of t's command as the summary shows it: with the
// flag before it, a boolean's texts, a count's flag, ... after what repeats
// per value, and in brackets when a run can leave it out.
func argShown(t *task, arg string) string {
	part, left, repeats := commandLine([]string{arg}), false, ""
	for _, m := range placeholder.FindAllStringSubmatch(arg, -1) {
		in := t.input(m[1])
		left = left || omissible(in)
		if in != nil && in.Multiple && in.Join == "" {
			repeats = "..."
		}
	}
	in := t.input(strings.TrimSuffix(strings.TrimPrefix(arg, "{"), "}"))
	flag, _ := t.argFlag(arg) // parse refuses two
	switch {
	case in != nil && arg == "{"+in.Name+"}" && in.Count && in.Stack:
		part, repeats = commandLine([]string{in.Flag + in.Flag[1:]}), "..."
	case in != nil && arg == "{"+in.Name+"}" && in.Count:
		part, repeats = commandLine([]string{in.Flag}), "..."
	case in != nil && arg == "{"+in.Name+"}" && in.Type == "boolean":
		on, off := switchArgs(*in)
		shown := slices.DeleteFunc([]string{on, off}, func(text string) bool { return text == "" })
		for i, text := range shown {
			shown[i] = commandLine([]string{text})
		}
		part = strings.Join(shown, "|")
	case strings.HasSuffix(flag, "="):
		part = commandLine([]string{flag + arg})
	case flag != "":
		part = commandLine([]string{flag, arg})
	}
	if left {
		part = "[" + part + "]"
	}
	return part + repeats
}

// stdinShown says what the command reads, if anything.
func stdinShown(t *task) string {
	var marked []string
	for _, in := range t.Inputs {
		if in.Stdin {
			marked = append(marked, "{"+escape(in.Name)+"}"+map[bool]string{true: "...", false: ""}[in.Multiple && in.Join == ""])
		}
	}
	switch {
	case len(marked) > 1 || len(marked) == 1 && strings.HasSuffix(marked[0], "..."):
		return strings.Join(marked, " ") + ", a line each"
	case len(marked) == 1:
		return marked[0]
	case len(t.Inputs) > 0:
		return "the values as JSON"
	}
	return ""
}

// risks says which of t's inputs pass recipient text past a guard, as the
// owner chose with unsafe.
func risks(t *task) string {
	var parts []string
	for _, in := range t.Inputs {
		if in.Unsafe && in.Env != "" && unsafeEnv(in.Env) {
			parts = append(parts, in.Name+" sets "+in.Env)
		}
		if in.Unsafe && in.whole && (in.Type == "text" || in.Type == "multiline") {
			parts = append(parts, in.Name+" may start with -")
		}
	}
	return escape(strings.Join(parts, ", "))
}

// envLine writes variables as a shell would set them, in name order, for
// parseEnv to read back.
func envLine(env map[string]string) string {
	var parts []string
	for _, name := range slices.Sorted(maps.Keys(env)) {
		parts = append(parts, quoted([]string{name + "=" + env[name]}))
	}
	return strings.Join(parts, " ")
}

// parseEnv reads variables as envLine shows them, or says what is wrong.
func parseEnv(line string, inputs []input) (map[string]string, string) {
	pairs, ok := words(line)
	if !ok {
		return nil, `Close the quote, or end the line without a lone \.`
	}
	var env map[string]string
	for _, pair := range pairs {
		name, value, ok := strings.Cut(pair, "=")
		switch _, twice := env[name]; {
		case !ok || !envName.MatchString(name):
			return nil, "Use NAME=value, as in LANG=C, with a name of letters, digits and _."
		case twice:
			return nil, "Set " + name + " once."
		case slices.ContainsFunc(inputs, func(in input) bool { return in.Env == name }):
			return nil, "An input sets " + name + " already."
		}
		if env == nil {
			env = map[string]string{}
		}
		env[name] = value
	}
	return env, ""
}

// words splits a line as a POSIX shell splits a simple command: at spaces,
// with '...' and "..." quoting and \ escaping, which inside "..." applies
// only to $ ` " \ and a newline. ok is false for an open quote or a trailing \.
func words(line string) (argv []string, ok bool) {
	var word strings.Builder
	quote, escaped, started := rune(0), false, false
	for _, r := range line {
		switch {
		case escaped:
			if quote == '"' && !strings.ContainsRune("$`\"\\\n", r) {
				word.WriteByte('\\') // literal there
			}
			word.WriteRune(r)
			escaped = false
		case quote == '\'' && r != '\'', quote == '"' && r != '"' && r != '\\':
			word.WriteRune(r)
		case quote != 0 && r != '\\':
			quote = 0
		case r == '\\':
			escaped, started = true, true
		case r == '\'' || r == '"':
			quote, started = r, true
		case unicode.IsSpace(r):
			if started {
				argv, started = append(argv, word.String()), false
				word.Reset()
			}
		default:
			word.WriteRune(r)
			started = true
		}
	}
	if started {
		argv = append(argv, word.String())
	}
	return argv, quote == 0 && !escaped
}

// commandLine shows a command as a POSIX shell would take it, with control
// characters escaped for the terminal.
func commandLine(argv []string) string { return escape(quoted(argv)) }

// quoted writes a command as a POSIX shell would take it, single-quoting each
// argument that is not plainly safe to paste; words reads it back as it was.
func quoted(argv []string) string {
	shown := make([]string, len(argv))
	for i, arg := range argv {
		shown[i] = arg
		if arg == "" || strings.ContainsFunc(arg, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_@%+=:./-{}", r))
		}) {
			shown[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
		}
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
	if s, err := strconv.ParseFloat(text, 64); err != nil || !(s > 0 && s <= maxTimeout) {
		return "Use a number of seconds."
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
