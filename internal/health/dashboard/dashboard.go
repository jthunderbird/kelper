// Package dashboard renders kelper's live health view: a continuously updating
// terminal dashboard driven by informer events, not a repeating snapshot.
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/jthunderbird/kelper/internal/health"
	"github.com/jthunderbird/kelper/internal/health/engine"
)

// Options configures the dashboard.
type Options struct {
	// Interval is the fallback repaint cadence; live updates arrive via the
	// engine's update channel.
	Interval time.Duration
}

// Dashboard holds the widgets and view state.
type Dashboard struct {
	app  *tview.Application
	live *engine.Live
	opts Options

	header  *tview.TextView
	gauges  *tview.TextView
	table   *tview.Table
	detail  *tview.TextView
	events  *tview.TextView
	footer  *tview.TextView
	pages   *tview.Pages
	flex    *tview.Flex
	filterI *tview.InputField

	// view state
	findings  []health.Finding
	filter    string
	minSev    health.Severity
	paused    bool
	filtering bool
	lastTick  time.Time
}

// Run starts informers, builds the UI and blocks until the user exits or the
// context is cancelled.
func Run(ctx context.Context, eng *engine.Engine, opts Options) error {
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}

	live, err := engine.NewLive(ctx, eng)
	if err != nil {
		return fmt.Errorf("starting cluster watch: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	live.Start(ctx)

	d := &Dashboard{
		app:      tview.NewApplication(),
		live:     live,
		opts:     opts,
		lastTick: time.Now(),
	}
	d.build()
	d.refresh()

	// Feed UI updates from the live engine. Every widget mutation must go
	// through QueueUpdateDraw because tview is not goroutine-safe.
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				d.app.Stop()
				return
			case <-live.Updates():
				if d.paused {
					continue
				}
				d.app.QueueUpdateDraw(d.refresh)
			case <-ticker.C:
				// Keeps ages and the clock moving even when nothing changes.
				d.app.QueueUpdateDraw(d.refreshHeaderOnly)
			}
		}
	}()

	return d.app.SetRoot(d.pages, true).EnableMouse(true).Run()
}

// build assembles the widget tree.
func (d *Dashboard) build() {
	d.header = tview.NewTextView().SetDynamicColors(true)
	d.header.SetBorder(true).SetTitle(" kelper health ")

	// Gauge lines are sized to fit; wrapping them would split a meter in half.
	d.gauges = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	d.gauges.SetBorder(true).SetTitle(" nodes ")

	d.table = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	d.table.SetBorder(true).SetTitle(" findings ")
	d.table.SetSelectionChangedFunc(func(row, _ int) { d.showDetail(row) })

	d.detail = tview.NewTextView().SetDynamicColors(true).SetWordWrap(true)
	d.detail.SetBorder(true).SetTitle(" detail ")

	d.events = tview.NewTextView().SetDynamicColors(true)
	d.events.SetBorder(true).SetTitle(" recent warnings ")

	d.footer = tview.NewTextView().SetDynamicColors(true)

	d.filterI = tview.NewInputField().SetLabel("/")
	d.filterI.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			d.filter = strings.TrimSpace(d.filterI.GetText())
		}
		d.filtering = false
		d.flex.RemoveItem(d.filterI)
		d.app.SetFocus(d.table)
		d.refresh()
	})

	top := tview.NewFlex().
		AddItem(d.header, 0, 2, false).
		AddItem(d.gauges, 0, 3, false)

	middle := tview.NewFlex().
		AddItem(d.table, 0, 3, true).
		AddItem(d.detail, 0, 2, false)

	d.flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(top, 8, 0, false).
		AddItem(middle, 0, 3, true).
		AddItem(d.events, 7, 0, false).
		AddItem(d.footer, 1, 0, false)

	d.pages = tview.NewPages().AddPage("main", d.flex, true, true)
	d.app.SetInputCapture(d.onKey)
}

// onKey handles global key bindings.
func (d *Dashboard) onKey(ev *tcell.EventKey) *tcell.EventKey {
	// While typing a filter, keys belong to the input field.
	if d.filtering {
		return ev
	}

	switch ev.Key() {
	case tcell.KeyCtrlC:
		d.app.Stop()
		return nil
	case tcell.KeyEsc:
		if d.filter != "" || d.minSev != health.SeverityOk {
			d.filter, d.minSev = "", health.SeverityOk
			d.refresh()
		}
		return nil
	}

	switch ev.Rune() {
	case 'q':
		d.app.Stop()
		return nil
	case 'p':
		d.paused = !d.paused
		d.refresh()
		return nil
	case '/':
		d.filtering = true
		d.filterI.SetText("")
		d.flex.AddItem(d.filterI, 1, 0, true)
		d.app.SetFocus(d.filterI)
		return nil
	case 's':
		// Cycle the minimum severity filter.
		d.minSev = (d.minSev + 1) % (health.SeverityCritical + 1)
		d.refresh()
		return nil
	case '?':
		d.toggleHelp()
		return nil
	}
	return ev
}

// refreshHeaderOnly updates the clock and ages without rebuilding the table, so
// selection is never disturbed by a routine tick.
func (d *Dashboard) refreshHeaderOnly() {
	d.renderHeader()
	d.renderFooter()
}

// refresh rebuilds every panel from the live engine's latest result.
func (d *Dashboard) refresh() {
	res := d.live.Result()
	d.findings = filter(res.Findings, d.filter, d.minSev)

	d.renderHeader()
	d.renderGauges()
	d.renderTable()
	d.renderEvents()
	d.renderFooter()
}

func (d *Dashboard) renderHeader() {
	res := d.live.Result()
	counts := health.Tally(res.Findings)

	var b strings.Builder
	fmt.Fprintf(&b, "[white::]%s\n", time.Now().Format("15:04:05"))
	fmt.Fprintf(&b, "%s  %s  %s  %s\n",
		sevCount("critical", counts[health.SeverityCritical], "red"),
		sevCount("error", counts[health.SeverityError], "red"),
		sevCount("warn", counts[health.SeverityWarn], "yellow"),
		sevCount("info", counts[health.SeverityInfo], "gray"))

	if snap := d.live.Snapshot(); snap != nil {
		fmt.Fprintf(&b, "[gray::]%d pods  %d nodes\n", len(snap.Pods), len(snap.Nodes))
	}
	if deg := res.Degraded; len(deg) > 0 {
		fmt.Fprintf(&b, "[yellow::]degraded: %s", truncate(strings.Join(deg, "; "), 60))
	}
	d.header.SetText(b.String())
}

// renderGauges draws a bar per node for CPU and memory, plus severity trend.
func (d *Dashboard) renderGauges() {
	snap := d.live.Snapshot()
	if snap == nil {
		return
	}

	var b strings.Builder
	usage := map[string]struct{ cpu, mem float64 }{}
	for _, nm := range snap.NodeMetrics {
		node := snap.NodeByName(nm.Name)
		if node == nil {
			continue
		}
		alloc := node.Status.Allocatable
		var cpuPct, memPct float64
		if c := alloc.Cpu(); c != nil && c.MilliValue() > 0 {
			cpuPct = float64(nm.Usage.Cpu().MilliValue()) / float64(c.MilliValue()) * 100
		}
		if m := alloc.Memory(); m != nil && m.Value() > 0 {
			memPct = float64(nm.Usage.Memory().Value()) / float64(m.Value()) * 100
		}
		usage[nm.Name] = struct{ cpu, mem float64 }{cpuPct, memPct}
	}

	for i := range snap.Nodes {
		n := &snap.Nodes[i]
		u := usage[n.Name]
		name := n.Name
		if len(name) > 20 {
			name = name[:20]
		}
		fmt.Fprintf(&b, "[white::]%-20s[-:-:-] cpu %s  mem %s\n", name, bar(u.cpu, 10), bar(u.mem, 10))
	}
	if len(snap.Nodes) == 0 {
		b.WriteString("[gray::]no node data")
	}

	// Severity trend beneath the gauges, from the history ring buffer.
	if series := d.live.History().Series(health.SeverityError); len(series) > 1 {
		fmt.Fprintf(&b, "\n[gray::]errors  %s", sparkline(series, 40))
	}
	d.gauges.SetText(b.String())
}

func (d *Dashboard) renderTable() {
	// Preserve the selected finding across a rebuild so a live update does not
	// move the cursor out from under the user.
	var selectedKey string
	if row, _ := d.table.GetSelection(); row > 0 && row-1 < len(d.findings) {
		selectedKey = d.findings[row-1].Key()
	}

	d.table.Clear()
	headers := []string{"SEV", "NAMESPACE", "KIND", "NAME", "REASON", "MESSAGE", "AGE"}
	for i, h := range headers {
		d.table.SetCell(0, i, tview.NewTableCell(h).
			SetTextColor(tcell.ColorDarkCyan).
			SetSelectable(false).
			SetAttributes(tcell.AttrBold))
	}

	restore := 0
	for i, f := range d.findings {
		row := i + 1
		if f.Key() == selectedKey {
			restore = row
		}
		color := severityColor(f.Severity)
		ns := f.Ref.Namespace
		if ns == "" {
			ns = "-"
		}
		cells := []string{
			f.Severity.String(), ns, f.Ref.Kind, f.Ref.Name,
			f.Reason, truncate(f.Message, 60), age(f.Since),
		}
		for c, v := range cells {
			cell := tview.NewTableCell(v).SetTextColor(color)
			if c == 0 {
				cell.SetAttributes(tcell.AttrBold)
			}
			d.table.SetCell(row, c, cell)
		}
	}

	if len(d.findings) == 0 {
		d.table.SetCell(1, 0, tview.NewTableCell("no findings").
			SetTextColor(tcell.ColorGreen))
	}
	if restore > 0 {
		d.table.Select(restore, 0)
	}
	d.showDetailFor(restore)
}

func (d *Dashboard) showDetail(row int) { d.showDetailFor(row) }

func (d *Dashboard) showDetailFor(row int) {
	idx := row - 1
	if idx < 0 || idx >= len(d.findings) {
		d.detail.SetText("[gray::]select a finding")
		return
	}
	f := d.findings[idx]

	var b strings.Builder
	fmt.Fprintf(&b, "[%s::b]%s[-:-:-]  %s\n\n", colorName(f.Severity), f.Severity, f.Reason)
	fmt.Fprintf(&b, "[white::]%s\n\n", f.Ref.String())
	fmt.Fprintf(&b, "%s\n", f.Message)
	if f.Detail != "" && f.Detail != f.Message {
		fmt.Fprintf(&b, "\n[gray::]%s\n", f.Detail)
	}
	if len(f.Evidence) > 0 {
		b.WriteString("\n[darkcyan::]evidence[-:-:-]\n")
		for _, e := range f.Evidence {
			if e.Value == "" {
				continue
			}
			fmt.Fprintf(&b, "  [gray::]%s:[-:-:-] %s\n", e.Label, truncate(e.Value, 200))
		}
	}
	if f.Remediation != "" {
		fmt.Fprintf(&b, "\n[darkcyan::]fix[-:-:-]\n  %s\n", f.Remediation)
	}
	if !f.Since.IsZero() {
		fmt.Fprintf(&b, "\n[gray::]first seen %s ago[-:-:-]\n", age(f.Since))
	}
	fmt.Fprintf(&b, "[gray::]check: %s[-:-:-]\n", f.Check)

	d.detail.SetText(b.String())
	d.detail.ScrollToBeginning()
}

// renderEvents tails recent cluster warnings, giving context the findings
// themselves may not carry.
func (d *Dashboard) renderEvents() {
	snap := d.live.Snapshot()
	if snap == nil {
		return
	}
	type ev struct {
		t   time.Time
		msg string
	}
	var evs []ev
	for i := range snap.Events {
		e := &snap.Events[i]
		if e.Type != "Warning" {
			continue
		}
		t := e.LastTimestamp.Time
		if t.IsZero() {
			t = e.EventTime.Time
		}
		evs = append(evs, ev{t, fmt.Sprintf("[gray::]%s[-:-:-] [yellow::]%s[-:-:-] %s/%s: %s",
			t.Format("15:04:05"), e.Reason, e.InvolvedObject.Namespace,
			e.InvolvedObject.Name, truncate(e.Message, 80))})
	}
	// Newest first, capped to what fits the pane.
	for i := 0; i < len(evs); i++ {
		for j := i + 1; j < len(evs); j++ {
			if evs[j].t.After(evs[i].t) {
				evs[i], evs[j] = evs[j], evs[i]
			}
		}
	}
	var b strings.Builder
	for i, e := range evs {
		if i >= 5 {
			break
		}
		b.WriteString(e.msg + "\n")
	}
	if len(evs) == 0 {
		b.WriteString("[gray::]no recent warnings")
	}
	d.events.SetText(b.String())
}

func (d *Dashboard) renderFooter() {
	state := "[green::]live"
	if d.paused {
		state = "[yellow::]paused"
	}
	filters := ""
	if d.filter != "" {
		filters += fmt.Sprintf("  [darkcyan::]filter:[-:-:-]%s", d.filter)
	}
	if d.minSev != health.SeverityOk {
		filters += fmt.Sprintf("  [darkcyan::]min:[-:-:-]%s", d.minSev)
	}
	d.footer.SetText(fmt.Sprintf(
		"%s[-:-:-]  [gray::]/ filter  s severity  p pause  ? help  q quit%s", state, filters))
}

func (d *Dashboard) toggleHelp() {
	if d.pages.HasPage("help") {
		d.pages.RemovePage("help")
		return
	}
	help := tview.NewTextView().SetDynamicColors(true).SetText(strings.Join([]string{
		"",
		"  [darkcyan::]kelper health dashboard[-:-:-]",
		"",
		"  [white::]/[-:-:-]      filter findings by text",
		"  [white::]s[-:-:-]      cycle minimum severity",
		"  [white::]p[-:-:-]      pause or resume live updates",
		"  [white::]esc[-:-:-]    clear filters",
		"  [white::]?[-:-:-]      toggle this help",
		"  [white::]q[-:-:-]      quit (ctrl-c also works)",
		"",
		"  [gray::]Updates arrive from cluster watch events.",
		"  Metrics, volume stats and certs poll separately.[-:-:-]",
		"",
	}, "\n"))
	help.SetBorder(true).SetTitle(" help ")
	d.pages.AddPage("help", modal(help, 60, 16), true, true)
}

// modal centers a primitive at a fixed size.
func modal(p tview.Primitive, width, height int) tview.Primitive {
	return tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(p, height, 1, true).
			AddItem(nil, 0, 1, false), width, 1, true).
		AddItem(nil, 0, 1, false)
}
