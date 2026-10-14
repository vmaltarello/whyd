package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/vmaltarello/whyd/internal/analyze"
	"github.com/vmaltarello/whyd/internal/report"
	"github.com/vmaltarello/whyd/internal/rules"
)

const (
	sessionHeight = 5 // the session panel, top right: how it ended, in three lines
	detailHeight  = 6 // under the problems list: what the selected problem means
	minW, minH    = 100, 28
)

func (m *model) tooSmall() string {
	msg := []string{
		fmt.Sprintf("The terminal is %d×%d.", m.w, m.h),
		fmt.Sprintf("whyd needs at least %d×%d.", minW, minH),
		"",
		"Make the window larger, or run whyd | less.",
		"Q quits.",
	}
	var out []string
	for range max(0, (m.h-len(msg))/2) {
		out = append(out, "")
	}
	for _, l := range msg {
		out = append(out, strings.Repeat(" ", max(0, (m.w-len([]rune(l)))/2))+l)
	}
	return strings.Join(out, "\n")
}

func (m *model) mainView() string {
	if m.w < minW || m.h < minH {
		return m.tooSmall()
	}
	bodyH := m.h - 2
	showIdx := m.w >= 110
	lw := 34
	if showIdx {
		lw = 39
	}
	rw := m.w - lw - 1

	left := m.sessionsPanel(lw, bodyH, showIdx)
	res, err := m.cur()
	right := m.sessionPanel(res, err, rw)
	right = append(right, m.problemsPanel(res, rw, bodyH-len(right))...)

	lines := []string{m.header()}
	for i := range bodyH {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		lines = append(lines, pad(l, lw)+" "+r)
	}
	only := "only bad ends"
	if m.onlyBad {
		only = "all sessions"
	}
	keys := [][2]string{{"↑↓", "move"}, {"→", "problems"}, {"L", "last minutes"}, {"R", "report"}, {"U", only}, {"?", "help"}}
	if m.focus == focusProblems {
		order := "by time"
		if m.byTime {
			order = "by importance"
		}
		keys = [][2]string{{"↑↓", "move"}, {"←", "sessions"}, {"enter", "lines"}, {"T", order}, {"L", "last minutes"}, {"R", "report"}, {"?", "help"}}
	}
	return strings.Join(append(lines, m.keyBar(keys)), "\n")
}

func (m *model) header() string {
	total, done, nbad := len(m.order), 0, 0
	for _, idx := range m.order {
		if st, ok := m.summaries[idx]; ok {
			done++
			if st.err == nil && st.sum.Bad() {
				nbad++
			}
		}
	}
	var info string
	switch {
	case len(m.src.Boots) == 0:
		info = "recorded journal"
	case done < total:
		info = fmt.Sprintf("reading sessions %d/%d", done, total)
	case m.older > 0:
		days := int(report.Now().Sub(m.since).Hours()/24 + 0.5)
		info = fmt.Sprintf("%s in the last %d days, %d ended badly", report.Plural(total, "session"), days, nbad)
	default:
		info = fmt.Sprintf("%s, %d ended badly", report.Plural(total, "session"), nbad)
	}
	return fit(" "+stBold.Render("whyd")+" "+stGray.Render(report.Version)+"   "+stDim.Render(info), m.w)
}

// keyBar is the line of keys at the bottom, quiet: the key in white, what it does in grey.
func (m *model) keyBar(keys [][2]string) string {
	var parts []string
	for _, k := range keys {
		parts = append(parts, stDim.Render(k[0])+" "+stGray.Render(k[1]))
	}
	bar := " " + strings.Join(parts, "  ")
	if m.status != "" {
		return spread(bar, stYellow.Render(m.status)+" ", m.w)
	}
	return fit(bar, m.w)
}

// ---- sessions, left ----

func (m *model) sessionsPanel(w, h int, showIdx bool) []string {
	vis := m.visible()
	inner := w - 4
	focused := m.focus == focusSessions
	var rows []string
	for i, idx := range vis {
		rows = append(rows, m.sessionRow(idx, i == m.selS, focused, inner, showIdx))
	}
	if len(vis) == 0 && m.older == 0 {
		rows = append(rows, "  "+stDim.Render("none yet"))
	}
	if m.older > 0 {
		sel := m.selS >= len(vis)
		t := fit(fmt.Sprintf("… %d older, enter: +%d days", m.older, int(Recent.Hours()/24)), inner-2)
		if sel {
			t = stPlain.Render(t)
		} else {
			t = stGray.Render(t)
		}
		rows = append(rows, mark(sel, focused)+t)
	}
	card := m.machineCard(inner)
	listH := h - 2
	if listH-len(card) >= 5 { // the list keeps room for a few sessions
		listH -= len(card)
	} else {
		card = nil
	}
	m.offS = scrollTo(m.offS, m.selS, 1, listH)
	lines := window(rows, m.offS, listH)
	for len(lines) < listH {
		lines = append(lines, "")
	}
	title := "Sessions"
	if m.onlyBad {
		title = "Sessions · bad ends only"
	}
	return frame(append(lines, card...), w, h, title, focused)
}

// machineCard describes the machine of the selected session, from its own journal, at the
// bottom of the sessions panel; it says when the kernel changed from the session before.
func (m *model) machineCard(w int) []string {
	idx, ok := m.selected()
	st, done := m.summaries[idx]
	if !ok || !done || st.err != nil {
		return nil
	}
	mc, kernel := st.sum.Machine, st.sum.Kernel
	var lines []string
	if mc.Model != "" {
		lines = append(lines, stDim.Render(fit(mc.Model, w)))
	}
	var hw []string
	for _, p := range []string{mc.CPU, mc.RAM()} {
		if p != "" {
			hw = append(hw, p)
		}
	}
	if len(hw) > 0 {
		lines = append(lines, stDim.Render(fit(strings.Join(hw, " · "), w)))
	}
	if len(mc.Graphics) > 0 {
		lines = append(lines, stDim.Render(fit("graphics "+strings.Join(mc.Graphics, " + "), w)))
	}
	if kernel != "" {
		lines = append(lines, stDim.Render(fit("kernel "+kernel, w)))
		if before := m.kernelBefore(idx); before != "" && before != kernel {
			lines = append(lines, stYellow.Render(fit("changed, was "+before, w)))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	head := stGray.Render("─ This session " + strings.Repeat("─", max(0, w-15)))
	return append([]string{head}, lines...)
}

// kernelBefore is the kernel of the session before this one, "" when unknown.
func (m *model) kernelBefore(idx int) string {
	if len(m.src.Boots) == 0 { // a recorded journal: one session, nothing before it
		return ""
	}
	if st, ok := m.summaries[idx-1]; ok && st.err == nil {
		return st.sum.Kernel
	}
	return ""
}

func (m *model) sessionRow(idx int, sel, focused bool, w int, showIdx bool) string {
	var when time.Time
	for _, b := range m.src.Boots {
		if b.Index == idx {
			when = b.First
		}
	}
	if r := m.results[idx].res; when.IsZero() && r != nil { // a recorded journal
		when = r.Boot.First
	}
	st, done := m.summaries[idx]
	end, endSt := "…", stGray
	switch {
	case !done:
	case st.err != nil:
		end, endSt = "error", stRed
	default:
		end, endSt = st.sum.Kind.String(), endStyle(st.sum.Kind)
	}
	label := ""
	if showIdx {
		label = fmt.Sprintf("%3d  ", idx)
		if idx == 0 && len(m.src.Boots) > 0 {
			label = "now  "
		}
	}
	whenText := report.WhenShort(when)
	if len(m.src.Boots) == 0 {
		whenText = when.Format("2 Jan 15:04")
	}
	whenSt := stDim
	if sel {
		whenSt = stPlain
	}
	if done && st.err == nil && !st.sum.Bad() { // colour only for what went wrong
		endSt = whenSt
	}
	return fit(mark(sel, focused)+stGray.Render(label)+whenSt.Render(pad(whenText, 13))+endSt.Render(end), w)
}

// ---- the selected session, top right ----

// sessionPanel says how the session ended in three lines: the verdict, what it means, and the
// facts around it.
func (m *model) sessionPanel(res *analyze.Result, err error, w int) []string {
	inner := w - 4
	var lines []string
	title := "Session"
	switch {
	case err != nil:
		lines = append(lines, stRed.Render(fit(err.Error(), inner)))
	case res == nil:
		idx, _ := m.selected()
		if st, ok := m.summaries[idx]; ok && st.err == nil { // the verdict is known: say it at once
			for _, b := range m.src.Boots {
				if b.Index == idx {
					title = report.When(b.First) + " → " + report.When(b.Last)
				}
			}
			head, why := endWords(st.sum.Kind, st.sum.Errors, st.sum.Warnings)
			lines = append(lines, endStyle(st.sum.Kind).Bold(true).Render("● "+head), "  "+why)
		}
		lines = append(lines, "  "+stGray.Render("reading the journal…"))
	default:
		title = report.When(res.Boot.First) + " → " + report.When(res.Boot.Last)
		if res.Current {
			title = report.When(res.Boot.First) + " → now"
		}
		head, why := verdictWords(res)
		st := endStyle(res.Verdict.Kind).Bold(true)
		lines = append(lines, st.Render("● "+head), "  "+why, "  "+m.facts(res, inner-2))
	}
	for i := range lines {
		lines[i] = fit(lines[i], inner)
	}
	return frame(lines, w, sessionHeight, title, false)
}

// verdictWords is the verdict as a heading and one sentence that says what it means.
func verdictWords(r *analyze.Result) (head, why string) {
	switch r.Verdict.Kind {
	case analyze.EndAsleep:
		what := "suspended"
		if len(r.Sleeps) > 0 && r.Sleeps[len(r.Sleeps)-1].Hibernate {
			what = "hibernated"
		}
		at := r.Boot.Last
		if len(r.Sleeps) > 0 {
			at = r.Sleeps[len(r.Sleeps)-1].From
		}
		return "Never woke up", fmt.Sprintf("It %s at %s and never resumed: a flat battery or a failed resume.", what, at.Format("15:04"))
	case analyze.EndPanic:
		return "Kernel panic", "The kernel stopped on an error: " + strings.TrimPrefix(r.Verdict.Summary, "kernel panic: ")
	default:
		return endWords(r.Verdict.Kind, r.Count(rules.Err), r.Count(rules.Warn))
	}
}

// endWords is the verdict from how the session ended alone, as the list of sessions knows it.
func endWords(k analyze.EndKind, errors, warnings int) (head, why string) {
	switch k {
	case analyze.EndRunning:
		return "Running", "The session in use now."
	case analyze.EndPowerOff, analyze.EndShutdown, analyze.EndReboot:
		head = "Shut down normally"
		if k == analyze.EndReboot {
			head = "Restarted normally"
		}
		switch {
		case errors > 0:
			return head, fmt.Sprintf("A normal end, but the session had %s: see below.", report.Plural(errors, "error"))
		case warnings > 0:
			return head, fmt.Sprintf("A normal end, with %s worth a look.", report.Plural(warnings, "warning"))
		}
		return head, "A normal end: nothing in this session points to a problem."
	case analyze.EndAsleep:
		return "Never woke up", "It went to sleep and never resumed: a flat battery or a failed resume."
	case analyze.EndPanic:
		return "Kernel panic", "The kernel stopped on an error it could not survive."
	case analyze.EndUnclean: // below
	}
	return "Ended abruptly", "No shutdown: a power cut, a forced power-off or a freeze."
}

// facts is the line under the verdict: how long, how long asleep, the silence before the end.
func (m *model) facts(r *analyze.Result, w int) string {
	parts := []string{report.Length(r)}
	switch k := r.Verdict.Kind; {
	case k.Clean():
		parts = append(parts, strings.SplitN(r.Verdict.Summary, " at ", 2)[0]+" at "+r.Boot.Last.Format("15:04"))
	case r.Quiet > 0:
		parts = append(parts, "nothing abnormal after "+r.Boot.Last.Add(-r.Quiet).Format("15:04"))
	}
	if g := r.Gap(); g > 0 && !r.Verdict.Kind.Clean() {
		parts = append(parts, "next boot "+analyze.Duration(g)+" later")
	}
	sep := stGray.Render(" · ")
	out := stDim.Render(parts[0])
	for _, p := range parts[1:] { // whole parts only
		if lipgloss.Width(out)+3+lipgloss.Width(p) > w {
			break
		}
		out += sep + stDim.Render(p)
	}
	return out
}

// ---- problems, bottom right ----

func (m *model) problemsPanel(res *analyze.Result, w, h int) []string {
	inner := w - 4
	focused := m.focus == focusProblems
	if h < 5 {
		return nil
	}
	if res == nil {
		return frame(nil, w, h, "Problems", focused)
	}
	var lines []string
	if len(res.Findings) == 0 {
		lines = append(lines, stDim.Render("No problems recognised in this session."))
	} else {
		lines = append(lines, "  "+stGray.Render(spread(pad("TIME", 13)+pad("LEVEL", 9)+"WHAT", "TIMES", inner-2)))
		var rows []string
		var owner []int // the problem each row belongs to; a moment belongs to the next one
		selStart, selLen := 0, 1
		list := m.problems(res)
		moments := m.moments(res)
		for i, ev := range list {
			for len(moments) > 0 && !moments[0].at.After(ev.First) {
				rows, owner = append(rows, momentRow(res, moments[0], m, inner)), append(owner, i)
				moments = moments[1:]
			}
			r := m.problemRows(res, ev, i == m.selP, focused, inner)
			if i == m.selP {
				selStart, selLen = len(rows), len(r)
			}
			rows = append(rows, r...)
			for range r {
				owner = append(owner, i)
			}
		}
		for _, mo := range moments {
			rows, owner = append(rows, momentRow(res, mo, m, inner)), append(owner, len(list))
		}
		if m.selP == len(list)-1 { // the last problem: show how the session ended after it
			selLen = len(rows) - selStart
		}
		area := h - 2 - 1 - 1 - detailHeight // frame, column names, separator, detail
		listH := area
		if len(rows) > area {
			listH = area - 1 // a line to say what is out of sight
		}
		m.offP = scrollTo(m.offP, selStart, selLen, listH)
		off := max(0, min(m.offP, len(rows)-listH))
		lines = append(lines, window(rows, off, listH)...)
		if len(rows) > area {
			var more []string
			if off > 0 {
				more = append(more, fmt.Sprintf("↑ %d above", owner[off-1]+1))
			}
			if end := off + listH; end < len(rows) && owner[end] < len(list) {
				more = append(more, fmt.Sprintf("↓ %d more", len(list)-owner[end]))
			}
			lines = append(lines, "  "+stGray.Render(strings.Join(more, "  ")))
		}
		for len(lines) < 1+area {
			lines = append(lines, "")
		}
		lines = append(lines, stGray.Render(strings.Repeat("┄", inner)))
		lines = append(lines, m.problemDetail(res, m.selectedProblem(res), inner, detailHeight)...)
	}
	order := "by importance"
	if m.byTime {
		order = "by time"
	}
	title := problemsTitle(res) + "  " + stGray.Render(order)
	return frameNote(lines, w, h, title, problemsNote(res), focused)
}

// moment is something that happened to the session itself: it started, slept, woke up, ended
// (the silence before an abrupt end is already a problem in the list). In time order they go between the problems and tell the story around them.
type moment struct {
	at   time.Time
	text string
	st   lipgloss.Style
}

func (m *model) moments(r *analyze.Result) []moment {
	if !m.byTime {
		return nil
	}
	start := "started"
	if r.Kernel != "" {
		start += " · kernel " + r.Kernel
	}
	out := []moment{{r.Boot.First, start, stGray}}
	for _, sl := range r.Sleeps {
		what := "went to sleep"
		if sl.Hibernate {
			what = "hibernated"
		}
		out = append(out, moment{sl.From, what, stGray})
		if sl.To.Before(r.Boot.Last) || r.Verdict.Kind != analyze.EndAsleep {
			out = append(out, moment{sl.To, "woke up after " + analyze.Duration(sl.To.Sub(sl.From)), stGray})
		}
	}
	end := map[analyze.EndKind]string{
		analyze.EndRunning: "now", analyze.EndPowerOff: "shut down", analyze.EndReboot: "restarted",
		analyze.EndShutdown: "shut down", analyze.EndAsleep: "never woke up", analyze.EndPanic: "kernel panic",
		analyze.EndUnclean: "the journal stops, with no shutdown",
	}[r.Verdict.Kind]
	out = append(out, moment{r.Boot.Last, end, endStyle(r.Verdict.Kind)})
	slices.SortStableFunc(out, func(a, b moment) int { return a.at.Compare(b.at) })
	return out
}

// momentRow is a moment as a separator in the problem list: "today 10:08  ── woke up after 10h38m ──".
func momentRow(r *analyze.Result, mo moment, m *model, w int) string {
	left := "  " + stGray.Render(pad(m.when(r, mo.at), 13)) + mo.st.Render("── "+mo.text+" ")
	return left + mo.st.Render(strings.Repeat("─", max(0, w-lipgloss.Width(left))))
}

// levelWord is a problem's level in its column: colour for errors and warnings only.
func levelWord(l rules.Level) string {
	w := pad(l.Word(), 9)
	switch l {
	case rules.Err:
		return stRed.Render(w)
	case rules.Warn:
		return stYellow.Render(w)
	case rules.Info, rules.Context, rules.Hidden:
	}
	return stGray.Render(w)
}

// problemsTitle counts by level, so ten harmless notes don't read as ten problems.
func problemsTitle(r *analyze.Result) string {
	var parts []string
	for _, l := range []rules.Level{rules.Err, rules.Warn, rules.Info} {
		if n := r.Count(l); n > 0 {
			word := l.Word()
			if l != rules.Info {
				word = report.Plural(n, word)[len(fmt.Sprint(n))+1:]
			}
			st := stDim
			switch l {
			case rules.Err:
				st = stRed
			case rules.Warn:
				st = stYellow
			case rules.Info, rules.Context, rules.Hidden:
			}
			parts = append(parts, st.Render(fmt.Sprintf("%d %s", n, word)))
		}
	}
	title := "Problems"
	if len(parts) > 0 {
		title += "  " + strings.Join(parts, stGray.Render(" · "))
	}
	return title
}

// problemsNote says what the list doesn't show: harmless lines, and lines no rule recognised.
func problemsNote(r *analyze.Result) string {
	var parts []string
	if r.HiddenLines > 0 {
		parts = append(parts, stGray.Render(report.Plural(r.HiddenLines, "harmless line")+" hidden"))
	}
	if n := r.UnknownErr + r.UnknownW; n > 0 {
		what := "other warnings"
		if r.UnknownErr > 0 {
			what = "other warnings and errors"
		}
		if n == 1 {
			what = strings.TrimSuffix(strings.Replace(what, "warnings and errors", "warning or error", 1), "s")
		}
		parts = append(parts, stGray.Render(fmt.Sprintf("%d %s: ", n, what))+stDim.Render("W"))
	}
	return strings.Join(parts, stGray.Render(" · "))
}

// when writes a moment of the session for the problem list: the time alone when the session
// fits in one day, else with the same day words as the sessions list on every row ("yest. 18:49",
// "today 10:53"), so rows of different days can't be mistaken.
func (m *model) when(r *analyze.Result, t time.Time) string {
	y1, m1, d1 := r.Boot.First.Date()
	y2, m2, d2 := r.Boot.Last.Date()
	if y1 == y2 && m1 == m2 && d1 == d2 {
		return t.Format("15:04")
	}
	if len(m.src.Boots) == 0 { // a recorded journal: "today" would be wrong
		return t.Format("Mon 15:04")
	}
	return report.WhenShort(t)
}

// problemRows is a problem's line in the table; the selected one continues on more lines when
// its title doesn't fit, so every title can be read in full.
func (m *model) problemRows(r *analyze.Result, ev *rules.Event, sel, focused bool, w int) []string {
	when := pad(m.when(r, ev.First), 13)
	level := levelWord(ev.Rule.Level)
	count := fmt.Sprint(ev.Count)
	const lead = 2 + 13 + 9 // mark, when, level
	room := w - lead - 5
	title := []string{fit(ev.Title, room)}
	if sel && ansi.StringWidth(ev.Title) > room {
		title = wrap(ev.Title, room)
	}
	whenSt, textSt, countSt := stGray, stDim, stGray
	if sel {
		whenSt, textSt, countSt = stPlain, stPlain, stPlain
	}
	var out []string
	for i, t := range title {
		if i > 0 {
			out = append(out, strings.Repeat(" ", lead)+textSt.Render(t))
			continue
		}
		left := mark(sel, focused) + whenSt.Render(when) + level + textSt.Render(t)
		out = append(out, spread(left, countSt.Render(count), w))
	}
	return out
}

// problemDetail is what is known about the selected problem: what it means, then the facts.
// The title is already on the selected row, so it isn't repeated.
func (m *model) problemDetail(r *analyze.Result, ev *rules.Event, w, h int) []string {
	var facts []string
	fact := func(label, value string) {
		facts = append(facts, stGray.Render(pad(label, 9))+stDim.Render(fit(value, w-9)))
	}
	if o := r.OOMOf(ev); o != nil {
		fact("killed", killedList(o))
		fact("memory", report.MemoryLine(o))
		var top []string
		for _, p := range o.Top[:min(4, len(o.Top))] {
			top = append(top, p.Name+" "+report.Size(p.RSSKB))
		}
		fact("largest", strings.Join(top, ", "))
	} else if d := report.Detail(r, ev); d != "" && (d != ev.Note() || ev.Rule.Explain == "") { // a note repeats the explanation
		facts = append(facts, stDim.Render(fit(d, w)))
	}
	var out []string
	if ev.Rule.Explain != "" {
		out = sentencesThatFit(joinParagraphs(ev.Rule.Explain), w, h-len(facts))
	}
	return append(out, facts...)[:min(len(out)+len(facts), max(0, h))]
}

func killedList(o *analyze.OOMReport) string {
	counts := map[string]int{}
	var names []string
	for _, k := range o.Killed {
		if counts[k.Name] == 0 {
			names = append(names, k.Name)
		}
		counts[k.Name]++
	}
	for i, n := range names {
		if counts[n] > 1 {
			names[i] = fmt.Sprintf("%s (%d)", n, counts[n])
		}
	}
	return strings.Join(names, ", ")
}

// sentencesThatFit wraps text to width w and keeps as many whole sentences as fit in h lines,
// so an explanation is never cut mid-sentence.
func sentencesThatFit(text string, w, h int) []string {
	if h <= 0 {
		return nil
	}
	all := wrap(text, w)
	if len(all) <= h {
		return all
	}
	words := strings.Fields(text)
	best := ""
	acc := ""
	for _, word := range words {
		if acc != "" {
			acc += " "
		}
		acc += word
		if strings.HasSuffix(word, ".") && len(wrap(acc, w)) <= h {
			best = acc
		}
	}
	if best == "" {
		return nil // not even one whole sentence fits: better nothing than half of one
	}
	return wrap(best, w)
}
