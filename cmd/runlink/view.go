package main

import (
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"
)

// view shows the owner what sharing is doing. With cursor control, a status
// region at the bottom redraws in place: connections and the latest run. Above
// it, earlier runs stay as one line each, and every stderr line goes to the
// scrollback under the start of its run. Otherwise each event is a timestamped
// line on stderr.
type view struct {
	mu  sync.Mutex
	st  style
	out io.Writer
	log *log.Logger
	r   *region // nil without cursor control

	origin     string // being connected to, until begin
	recipients int
	run        *runView
	frame      int
	stopped    chan struct{}
}

type runView struct {
	started time.Time
	took    time.Duration
	failure *runError
	over    bool
	wrote   bool // to stderr, under a line marking the start
}

func newView(st style, r *region, stdout, stderr io.Writer) *view {
	v := &view{st: st, out: stdout, log: log.New(terminal{stderr}, "", log.Ltime)}
	if st.live {
		v.r, v.stopped = r, make(chan struct{})
		go v.animate()
	}
	return v
}

// terminal writes log entries with their control characters escaped, so a
// script's stderr, which may echo recipient input, cannot drive the owner's
// terminal or fake a line.
type terminal struct{ w io.Writer }

func (t terminal) Write(p []byte) (int, error) {
	_, err := io.WriteString(t.w, escape(strings.TrimSuffix(string(p), "\n"))+"\n")
	return len(p), err
}

// animate redraws ten times a second while something moves.
func (v *view) animate() {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-v.stopped:
			return
		case <-tick.C:
		}
		v.mu.Lock()
		select {
		case <-v.stopped:
		default:
			if v.origin != "" || v.run != nil && !v.run.over {
				v.frame++
				v.draw(nil)
			}
		}
		v.mu.Unlock()
	}
}

func (v *view) draw(done []string) {
	v.r.draw(done, v.lines(), -1, 0)
}

func (v *view) lines() []string {
	st := v.st
	if v.origin != "" {
		return []string{"  " + v.spinner() + " " + st.sgr(dim, "Connecting to "+v.origin)}
	}
	var lines []string
	if r := v.run; r != nil {
		lines = append(lines, v.runLine(r), "")
	}
	stopped := false
	select {
	case <-v.stopped:
		stopped = true
	default:
	}
	switch {
	case stopped:
		return append(lines, "  "+st.sgr(dim, st.sym("■", "#")+" Stopped sharing. The link no longer works."))
	case v.recipients == 0:
		return append(lines, "  "+st.sgr(dim, st.sym("○", "o")+" Waiting for recipients"))
	case v.recipients == 1:
		return append(lines, "  "+st.sgr(green, st.sym("●", "*"))+" 1 recipient connected")
	}
	return append(lines, fmt.Sprintf("  %s %d recipients connected", st.sgr(green, st.sym("●", "*")), v.recipients))
}

func (v *view) spinner() string {
	frames := []string{"|", "/", "-", "\\"}
	if v.st.unicode {
		frames = strings.Split("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏", "")
	}
	return v.st.sgr(v.st.foilCode(float64(v.frame)/40), frames[v.frame%len(frames)])
}

// runLine is a run in one line: when it started and how it went.
func (v *view) runLine(r *runView) string {
	st := v.st
	at := " " + st.sgr(dim, r.started.Format(time.TimeOnly)) + "  "
	switch {
	case !r.over:
		return "  " + v.spinner() + at + "Running " + st.sgr(dim, seconds(time.Since(r.started)))
	case r.failure == nil:
		return "  " + st.sgr(green, st.sym("✓", "+")) + at + "Finished in " + seconds(r.took)
	case r.failure.Code == "timeout":
		return "  " + st.sgr(red, st.sym("✗", "x")) + at + "Timed out after " + seconds(r.took)
	case r.failure.Code == "stopped":
		return "  " + st.sgr(red, st.sym("✗", "x")) + at + "Stopped after " + seconds(r.took)
	}
	return "  " + st.sgr(red, st.sym("✗", "x")) + at + "Failed after " + seconds(r.took) + st.sgr(dim, ": "+escape(r.failure.Error()))
}

func seconds(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// connecting shows that sharing is starting.
func (v *view) connecting(origin string) {
	if v.r == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.origin = origin
	v.draw(nil)
}

// begin shows the task's header and link, which stay above the status.
func (v *view) begin(head []string) {
	if v.r == nil {
		fmt.Fprint(v.out, strings.Join(head, "\n")+"\n")
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.origin = ""
	v.draw(head)
}

func (v *view) connected(change int) {
	if v.r == nil {
		if change > 0 {
			v.log.Print("A recipient connected.")
		} else {
			v.log.Print("A recipient disconnected.")
		}
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.recipients += change
	v.draw(nil)
}

// notice reports something the owner may need to act on.
func (v *view) notice(text string) {
	if v.r == nil {
		v.log.Print(text)
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.draw([]string{"  " + v.st.sgr(yellow, "!") + " " + v.st.sgr(dim, time.Now().Format(time.TimeOnly)) + "  " + escape(text)})
}

// started begins a run; the one before it shrinks to a line above.
func (v *view) started() {
	if v.r == nil {
		v.log.Print("Run started.")
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	var done []string
	if v.run != nil {
		done = []string{v.runLine(v.run)}
	}
	v.run = &runView{started: time.Now()}
	v.draw(done)
}

// stderr shows a line the script wrote to stderr. It goes to the scrollback
// in full, so the terminal keeps all of it.
func (v *view) stderr(line string) {
	if v.r == nil {
		v.log.Print("  ", line)
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	st, done := v.st, []string{}
	if !v.run.wrote {
		v.run.wrote = true
		done = append(done, "  "+st.sgr(dim, st.sym("›", ">")+" "+v.run.started.Format(time.TimeOnly))+"  Started")
	}
	v.draw(append(done, st.sgr(dim, "      "+st.sym("│", "|")+" "+escape(line))))
}

func (v *view) finished(took time.Duration, failure *runError) {
	if v.r == nil {
		if failure != nil {
			v.log.Printf("Run %s after %v. %v", failure.Code, took, failure)
		} else {
			v.log.Printf("Run finished in %v.", took)
		}
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.run.took, v.run.failure, v.run.over = took, failure, true
	v.draw(nil)
}

// close ends the status with sharing: the latest run stays, and below it that
// sharing stopped. Without a link shown, the region empties.
func (v *view) close() {
	if v.r == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	select {
	case <-v.stopped:
		return
	default:
		close(v.stopped)
	}
	if v.origin != "" {
		v.r.draw(nil, nil, -1, 0)
		return
	}
	v.draw(nil)
}
