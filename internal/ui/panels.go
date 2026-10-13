package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/vmaltarello/whyd/internal/analyze"
	"github.com/vmaltarello/whyd/internal/report"
	"github.com/vmaltarello/whyd/internal/rules"
)

const (
	sessionHeight = 10 // the session panel, top right
	minW, minH    = 100, 28
)

func (m *model) tooSmall() string {
	msg := []string{
		fmt.Sprintf("The terminal is %d×%d.", m.w, m.h),
		fmt.Sprintf("whyd needs at least %d×%d.", minW, minH),
		"",
		"Make the window larger, or use whyd --text.",
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
	keys := [][2]string{{"↑↓", "Move"}, {"→", "Problems"}, {"L", "Last minutes"}, {"R", "Report"}, {"?", "Help"}, {"Q", "Quit"}}
	if m.focus == focusProblems {
		keys = [][2]string{{"↑↓", "Move"}, {"←", "Sessions"}, {"Enter", "Lines"}, {"R", "Report"}, {"?", "Help"}, {"Q", "Quit"}}
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
		info = fmt.Sprintf("%d sessions in the last %d days, %d without a clean shutdown", total, days, nbad)
	default:
		info = fmt.Sprintf("%d sessions in the journal, %d without a clean shutdown", total, nbad)
	}
	left := stBold.Render("whyd") + " " + stDim.Render(report.Version) + "  " + stDim.Render(info)
	return spread(left, stDim.Render(report.Now().Format("Mon 2 Jan 15:04")), m.w)
}

func (m *model) keyBar(keys [][2]string) string {
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(stKey.Render(" "+k[0]+" ") + " " + k[1] + "  ")
	}
	bar := b.String()
	if m.status != "" {
		return spread(bar, stYellow.Render(m.status), m.w)
	}
	return fit(bar, m.w)
}

// ---- sessions, left ----

func (m *model) sessionsPanel(w, h int, showIdx bool) []string {
	vis := m.visible()
	inner := w - 4
	var rows []string
	for i, idx := range vis {
		rows = append(rows, m.sessionRow(idx, i == m.selS, inner, showIdx))
	}
	if len(vis) == 0 && m.older == 0 {
		rows = append(rows, stDim.Render("none yet"))
	}
	if m.older > 0 {
		t := fit(fmt.Sprintf("… %d older, Enter: +%d days", m.older, int(Recent.Hours()/24)), inner)
		if m.selS >= len(vis) {
			st := stSel
			if m.focus != focusSessions {
				st = stSelBlur
			}
			rows = append(rows, st.Render(pad(t, inner)))
		} else {
			rows = append(rows, stGray.Render(t))
		}
	}
	listH := h - 4
	m.offS = scrollTo(m.offS, m.selS, 1, listH)
	lines := window(rows, m.offS, listH)
	for len(lines) < listH {
		lines = append(lines, "")
	}
	hint := "U: only the unclean ones"
	if m.onlyBad {
		hint = "U: show all"
	}
	lines = append(lines, stGray.Render(hint))
	title := "Sessions"
	if m.onlyBad {
		title = "Sessions without shutdown"
	}
	return frame(lines, w, h, title, m.focus == focusSessions)
}

func (m *model) sessionRow(idx int, sel bool, w int, showIdx bool) string {
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
	errs := "" // a clean ending can still hide a serious problem: say how many errors it had
	if done && st.err == nil && st.sum.Errors > 0 {
		errs = fmt.Sprintf("%d err", st.sum.Errors)
	}
	plain := label + pad(whenText, 13) + pad(end, 12) + errs
	if sel {
		if m.focus == focusSessions {
			return stSel.Render(pad(plain, w))
		}
		return stSelBlur.Render(pad(plain, w))
	}
	return stGray.Render(label) + pad(whenText, 13) + endSt.Render(pad(end, 12)) + stRed.Render(errs)
}

// ---- the selected session, top right ----

func (m *model) sessionPanel(res *analyze.Result, err error, w int) []string {
	inner := w - 4
	var lines []string
	title := "Session"
	switch {
	case err != nil:
		lines = append(lines, stRed.Render(fit(err.Error(), inner)))
	case res == nil:
		lines = append(lines, stDim.Render("reading the journal…"))
	default:
		title = report.SessionName(res)
		title = strings.ToUpper(title[:1]) + title[1:]
		lines = append(lines, stBold.Render(report.Span(res))+"  "+stDim.Render(report.Length(res)))
		kind := res.Verdict.Kind.String()
		rest := strings.TrimPrefix(res.Verdict.Summary, kind)
		lines = append(lines, stDim.Render("end     ")+endStyle(res.Verdict.Kind).Render(kind)+rest)
		if q := report.QuietLine(res); q != "" {
			lines = append(lines, "        "+q)
		}
		if b := report.Before(res); b != "" {
			label := "before  "
			if res.Verdict.Kind == analyze.EndRunning {
				label = "so far  "
			}
			lines = append(lines, stDim.Render(label)+b)
		}
		for len(lines) < sessionHeight-5 {
			lines = append(lines, "")
		}
		lines = append(lines, m.axis(res, inner)...)
	}
	for i := range lines {
		lines[i] = fit(lines[i], inner)
	}
	return frame(lines, w, sessionHeight, title, false)
}

// axis draws the session on a time line: awake (─), suspended (┄), the serious problems (┼)
// with their times above, and how it ended (┤).
func (m *model) axis(r *analyze.Result, w int) []string {
	from, to := r.Boot.First, r.Boot.Last
	if w < 20 || !to.After(from) {
		return nil
	}
	span := to.Sub(from)
	col := func(t time.Time) int {
		return max(0, min(w-1, int(float64(t.Sub(from))/float64(span)*float64(w-1)+0.5)))
	}
	cells := make([]string, w)
	asleep := make([]bool, w)
	for _, sl := range r.Sleeps {
		for c := col(sl.From); c <= col(sl.To); c++ {
			asleep[c] = true
		}
	}
	for c := range cells {
		if asleep[c] {
			cells[c] = stGray.Render("┄")
		} else {
			cells[c] = stDim.Render("─")
		}
	}
	endSt := endStyle(r.Verdict.Kind)
	cells[0] = stDim.Render("├")
	cells[w-1] = endSt.Render("┤")

	above := make([]string, w)
	for c := range above {
		above[c] = " "
	}
	free := func(x, n int) bool {
		for i := max(0, x-1); i < min(w, x+n+1); i++ {
			if above[i] != " " {
				return false
			}
		}
		return x >= 0 && x+n <= w
	}
	marked := make([]bool, w)
	for _, ev := range r.Findings {
		if ev.Rule.Severity < rules.Warning {
			continue
		}
		c := col(ev.First)
		st := levelStyle(ev.Rule.Severity)
		if c > 0 && c < w-1 && !marked[c] { // findings come most serious first: keep its colour
			cells[c] = st.Render("┼")
			marked[c] = true
		}
		label := ev.First.Format("15:04")
		x := max(0, min(w-len(label), c-2))
		said := label == from.Format("15:04") || label == to.Format("15:04") // already under the axis
		if !said && free(x, len(label)) {
			for i, ch := range label {
				above[x+i] = st.Render(string(ch))
			}
		}
	}

	below := make([]string, w)
	for c := range below {
		below[c] = " "
	}
	put := func(x int, s string, st lipgloss.Style) { // where it fits and the cells are free
		n := len([]rune(s))
		if x < 0 || x+n > w {
			return
		}
		for i := x; i < x+n; i++ {
			if below[i] != " " {
				return
			}
		}
		for i, ch := range []rune(s) {
			below[x+i] = st.Render(string(ch))
		}
	}
	put(0, from.Format("15:04"), stDim)
	endLabel := to.Format("15:04")
	if r.Current {
		endLabel = "now"
	}
	put(w-len(endLabel), endLabel, endSt)
	for _, sl := range r.Sleeps { // label each sleep long enough to hold it
		a, b := col(sl.From), col(sl.To)
		lbl := "suspended " + analyze.Duration(sl.To.Sub(sl.From))
		if sl.Hibernate {
			lbl = "hibernated " + analyze.Duration(sl.To.Sub(sl.From))
		}
		if b-a > len(lbl)+2 {
			put(a+(b-a-len(lbl))/2, lbl, stGray)
		}
	}
	return []string{strings.Join(above, ""), strings.Join(cells, ""), strings.Join(below, "")}
}

// ---- problems, bottom right ----

func (m *model) problemsPanel(res *analyze.Result, w, h int) []string {
	inner := w - 4
	if h < 5 {
		return nil
	}
	if res == nil {
		return frame(nil, w, h, "Problems", m.focus == focusProblems)
	}
	var lines []string
	if len(res.Findings) == 0 {
		lines = append(lines, "No problems recognised in this session.")
	} else {
		lines = append(lines, stGray.Bold(true).Render(spread(pad("TIME", 11)+pad("LEVEL", 7)+"WHAT", "TIMES", inner)))
		var rows []string
		selStart, selLen := 0, 1
		for i, ev := range res.Findings {
			r := m.problemRows(res, ev, i == m.selP, inner)
			if i == m.selP {
				selStart, selLen = len(rows), len(r)
			}
			rows = append(rows, r...)
		}
		listH := min(len(rows), max(4, (h-4)/2))
		m.offP = scrollTo(m.offP, selStart, selLen, listH)
		lines = append(lines, window(rows, m.offP, listH)...)
		lines = append(lines, "")
		lines = append(lines, m.problemDetail(res, res.Findings[min(m.selP, len(res.Findings)-1)], inner, h-2-len(lines)-1)...)
	}
	for len(lines) < h-3 {
		lines = append(lines, "")
	}
	lines = lines[:h-3]
	lines = append(lines, fit(bottomLine(res), inner))
	return frame(lines, w, h, problemsTitle(res), m.focus == focusProblems)
}

// problemsTitle counts by level, so ten harmless notes don't read as ten problems.
func problemsTitle(r *analyze.Result) string {
	if len(r.Findings) == 0 {
		return "Problems"
	}
	var parts []string
	for _, l := range []struct {
		s    rules.Severity
		name string
	}{{rules.Critical, "err"}, {rules.Warning, "warn"}, {rules.Info, "info"}} {
		if n := r.Count(l.s); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, l.name))
		}
	}
	return "Problems: " + strings.Join(parts, ", ")
}

// problemRows is a problem's line in the table; the selected one continues on more lines when
// its title doesn't fit, so every title can be read in full.
func (m *model) problemRows(r *analyze.Result, ev *rules.Event, sel bool, w int) []string {
	when := pad(report.Within(ev.First, r.Boot.Last), 11)
	level := pad(report.Level(ev.Rule.Severity), 7)
	count := fmt.Sprint(ev.Count)
	const lead = 18 // when + level
	room := w - lead - 6
	title := []string{fit(ev.Title, room)}
	if sel && ansi.StringWidth(ev.Title) > room {
		title = wrap(ev.Title, room)
	}
	var out []string
	for i, t := range title {
		left := when + level + t
		right := count
		if i > 0 {
			left, right = strings.Repeat(" ", lead)+t, ""
		}
		switch {
		case sel && m.focus == focusProblems:
			out = append(out, stSel.Render(spread(left, right, w)))
		case sel:
			out = append(out, stSelBlur.Render(spread(left, right, w)))
		case ev.Rule.Severity <= rules.Info: // notes: present, but quieter than problems
			out = append(out, stDim.Render(spread(left, right, w)))
		default:
			out = append(out, spread(when+levelStyle(ev.Rule.Severity).Render(level)+t, stDim.Render(right), w))
		}
	}
	return out
}

// problemDetail is what is known about the selected problem: the facts, then what it means.
// The title is already on the selected row, so it isn't repeated.
func (m *model) problemDetail(r *analyze.Result, ev *rules.Event, w, h int) []string {
	var out []string
	fact := func(label, value string) {
		out = append(out, stDim.Render(pad(label, 9))+fit(value, w-9))
	}
	switch {
	case (ev.Rule.ID == "memory.oom-report" || ev.Rule.ID == "memory.oom-kill") && r.OOM != nil:
		fact("killed", killedList(r.OOM))
		fact("memory", report.MemoryLine(r.OOM))
		var top []string
		for _, p := range r.OOM.Top[:min(4, len(r.OOM.Top))] {
			top = append(top, p.Name+" "+report.Size(p.RSSKB))
		}
		fact("largest", strings.Join(top, ", "))
	default:
		if d := report.Detail(r, ev); d != "" {
			out = append(out, fit(d, w))
		}
	}
	if ev.Rule.Explain != "" && h-len(out) > 1 {
		if len(out) > 0 {
			out = append(out, "")
		}
		for _, l := range sentencesThatFit(joinParagraphs(ev.Rule.Explain), w, h-len(out)) {
			out = append(out, stDim.Render(l))
		}
	}
	return out[:min(len(out), max(0, h))]
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

// bottomLine says what the problems list doesn't show: lines no rule recognised, routine lines.
func bottomLine(r *analyze.Result) string {
	var parts []string
	if n := r.UnknownErr + r.UnknownW; n > 0 {
		s := fmt.Sprintf("%d not recognised", n)
		if r.UnknownErr > 0 {
			s += fmt.Sprintf(" (%d err)", r.UnknownErr)
		}
		parts = append(parts, stDim.Render(s)+stGray.Render(", press W"))
	}
	if r.NoiseLines > 0 {
		parts = append(parts, stGray.Render(fmt.Sprintf("%d routine hidden", r.NoiseLines)))
	}
	return strings.Join(parts, stGray.Render("   "))
}
