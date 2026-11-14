package engine

import (
	"fmt"
	"html"
	"sort"
	"strings"
)

// LogLine is one line of a run log, for the page.
type LogLine struct {
	Offset string
	Step   string
	Kind   string
	Text   string
}

// PageRun is a run to overlay on the picture.
type PageRun struct {
	Title string
	State map[string]StepState
	Log   []LogLine
}

// RenderHTML draws the workflow as one column of boxes in running order, top to bottom, which is
// the order the steps execute. A step's inputs are drawn as arrows down the left side, from the
// step that produces them, so nothing crosses a box. The page is self-contained: no scripts and no
// network. All text from the workflow and the run is escaped.
func RenderHTML(g Graph, run *PageRun) string {
	const boxW, boxH, gapY, pad, rail, laneW = 560, 66, 26, 24, 96, 14
	n := len(g.Nodes)
	index := map[string]int{}
	for i, nd := range g.Nodes {
		index[nd.ID] = i
	}
	boxX := pad + rail
	width := boxX + boxW + pad
	height := pad + n*(boxH+gapY)

	// assign each edge a lane in the rail, so arrows that overlap in height do not overlap in x
	type lane struct{ from, to int }
	var lanes []lane
	edgeLane := map[int]int{}
	edges := append([]Edge(nil), g.Edges...)
	sort.SliceStable(edges, func(i, j int) bool {
		li, lj := index[edges[i].To]-index[edges[i].From], index[edges[j].To]-index[edges[j].From]
		return li < lj
	})
	maxLane := 0
	for ei, e := range edges {
		f, t := index[e.From], index[e.To]
		l := 0
		for ; ; l++ {
			clash := false
			for k, other := range lanes {
				if edgeLane[k] == l && !(other.to < f || other.from > t) {
					clash = true
					break
				}
			}
			if !clash {
				break
			}
		}
		edgeLane[ei] = l
		lanes = append(lanes, lane{f, t})
		if l > maxLane {
			maxLane = l
		}
	}
	if need := (maxLane + 1) * laneW; need > rail-10 {
		boxX = pad + need + 10
		width = boxX + boxW + pad
	}

	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString("<title>" + html.EscapeString(g.Name) + " workflow</title><style>")
	b.WriteString(`:root{--bg:#fff;--fg:#1b1f23;--mut:#5b6670;--line:#8a949e;--rd:#e6f4f7;--rds:#2b8ca3;--st:#fff4d6;--sts:#b8860b;--ou:#fde2e2;--ous:#c0392b;--bad:#c0392b;--card:#f6f8fa}
@media(prefers-color-scheme:dark){:root{--bg:#0f1316;--fg:#e6e9ec;--mut:#9aa5ae;--line:#6b7680;--rd:#12323a;--rds:#4cb6cc;--st:#3a3010;--sts:#e0b84a;--ou:#3d1a1a;--ous:#ef7b6e;--bad:#ff7b72;--card:#171c21}}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,sans-serif}main{max-width:1100px;margin:0 auto;padding:16px}
h1{font-size:20px;margin:0 0 4px}h2{font-size:16px;margin:22px 0 8px}.mut{color:var(--mut)}
.legend span{display:inline-block;margin:2px 14px 2px 0}.sw{display:inline-block;width:14px;height:14px;border-radius:3px;margin-right:6px;vertical-align:-2px;border:2px solid}
i.reads{background:var(--rd);border-color:var(--rds)}i.state{background:var(--st);border-color:var(--sts)}i.outside{background:var(--ou);border-color:var(--ous)}
.canvas{overflow-x:auto;border:1px solid var(--line);border-radius:8px;background:var(--card)}svg{display:block}
text{fill:var(--fg);font:13px system-ui,sans-serif}text.s{font-size:12px;fill:var(--mut)}
rect.reads{fill:var(--rd);stroke:var(--rds)}rect.state{fill:var(--st);stroke:var(--sts)}rect.outside{fill:var(--ou);stroke:var(--ous)}
.box{stroke-width:2}.dim{opacity:.5}.fail{stroke:var(--bad);stroke-width:4}
table{border-collapse:collapse;width:100%;font:13px ui-monospace,monospace}td{padding:3px 8px;border-bottom:1px solid var(--line);vertical-align:top}
td.t{color:var(--mut);white-space:nowrap}td.e{color:var(--bad)}@media(max-width:600px){main{padding:12px}}</style>`)
	b.WriteString("<main><h1>" + html.EscapeString(g.Name) + "</h1>")
	sub := "The steps in the order they run, top to bottom. An arrow on the left means the lower step uses what the upper step found."
	if g.Every != "" {
		sub += " Scheduled every " + html.EscapeString(g.Every) + "."
	}
	b.WriteString(`<div class="mut">` + sub + `</div>`)
	if run != nil {
		b.WriteString(`<div style="margin-top:6px"><strong>` + html.EscapeString(run.Title) + `</strong></div>`)
	}
	b.WriteString(`<p class="legend"><span><i class="sw reads"></i>only reads</span><span><i class="sw state"></i>saves state on this machine</span><span><i class="sw outside"></i>changes something outside</span><span>dashed arrow: used only in a condition</span>`)
	if run != nil {
		b.WriteString(`<span>✓ ran · – skipped · ✗ failed · ○ would run · faded = not run</span>`)
	}
	b.WriteString(`</p><div class="canvas"><svg role="img" aria-label="Workflow diagram" width="` + fmt.Sprint(width) + `" height="` + fmt.Sprint(height) + `" viewBox="0 0 ` + fmt.Sprint(width) + ` ` + fmt.Sprint(height) + `">`)
	b.WriteString(`<defs><marker id="a" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0L10 5L0 10z" fill="var(--line)"/></marker></defs>`)
	top := func(i int) int { return pad + i*(boxH+gapY) }
	// arrows in the rail: leave the source box at its left edge, run down a lane, enter the target
	for ei, e := range edges {
		f, t := index[e.From], index[e.To]
		x := boxX - 14 - edgeLane[ei]*laneW
		y1, y2 := top(f)+boxH/2, top(t)+boxH/2
		if f == t-1 { // neighbours: a short straight arrow between the boxes
			b.WriteString(fmt.Sprintf(`<path d="M%d %d L%d %d" stroke="var(--line)" stroke-width="1.6" fill="none"%s marker-end="url(#a)"/>`, boxX+boxW/2, top(f)+boxH, boxX+boxW/2, top(t), dashFor(e)))
			continue
		}
		b.WriteString(fmt.Sprintf(`<path d="M%d %d L%d %d L%d %d L%d %d" stroke="var(--line)" stroke-width="1.6" fill="none"%s marker-end="url(#a)"/>`,
			boxX, y1, x, y1, x, y2, boxX-1, y2, dashFor(e)))
	}
	for i, nd := range g.Nodes {
		y := top(i)
		cls := map[Effect]string{Read: "reads", WriteInternal: "state", WriteExternal: "outside"}[nd.Effect]
		extra, glyph, note := "", "", ""
		if run != nil {
			st, ok := run.State[nd.ID]
			switch {
			case !ok || st.Status == "":
				extra, glyph = " dim", "·"
			case st.Status == "failed":
				extra, glyph = " fail", "✗"
			case st.Status == "skipped":
				extra, glyph = " dim", "–"
			case st.Status == "planned":
				glyph = "○"
			default:
				glyph = "✓"
			}
			note = st.Note
			if st.Millis > 0 && st.Status != "skipped" {
				note = strings.TrimSpace(ms(st.Millis) + "  " + note)
			}
		}
		b.WriteString(fmt.Sprintf(`<g class="n%s"><rect class="box %s" x="%d" y="%d" width="%d" height="%d" rx="9"/>`, extra, cls, boxX, y, boxW, boxH))
		title := fmt.Sprintf("%d. %s", nd.Order, nd.ID)
		if glyph != "" {
			title += "  " + glyph
		}
		b.WriteString(fmt.Sprintf(`<text x="%d" y="%d" style="font-weight:600">%s</text>`, boxX+12, y+21, html.EscapeString(clipText(title, 60))))
		b.WriteString(fmt.Sprintf(`<text class="s" text-anchor="end" x="%d" y="%d">%s</text>`, boxX+boxW-12, y+21, html.EscapeString(nd.Block)))
		cond := ""
		if nd.When != "" {
			cond = "only if " + nd.When
		}
		if note != "" {
			b.WriteString(fmt.Sprintf(`<text class="s" x="%d" y="%d">%s</text>`, boxX+12, y+40, html.EscapeString(clipText(note, 78))))
			if cond != "" {
				b.WriteString(fmt.Sprintf(`<text class="s" x="%d" y="%d">%s</text>`, boxX+12, y+57, html.EscapeString(clipText(cond, 78))))
			}
		} else if cond != "" {
			b.WriteString(fmt.Sprintf(`<text class="s" x="%d" y="%d">%s</text>`, boxX+12, y+40, html.EscapeString(clipText(cond, 78))))
		}
		b.WriteString(`<title>` + html.EscapeString(nd.ID+" ("+nd.Block+")"+condTitle(nd)+noteTitle(run, nd.ID)) + `</title></g>`)
	}
	b.WriteString(`</svg></div>`)
	if run != nil && len(run.Log) > 0 {
		b.WriteString(`<h2>Run log</h2><div class="canvas" style="padding:6px"><table>`)
		for _, l := range run.Log {
			cls := ""
			if l.Kind == "error" {
				cls = ` class="e"`
			}
			who := l.Step
			if who == "" {
				who = "-"
			}
			b.WriteString(fmt.Sprintf(`<tr><td class="t">+%s</td><td>%s</td><td%s>%s</td></tr>`, html.EscapeString(l.Offset), html.EscapeString(who), cls, html.EscapeString(l.Text)))
		}
		b.WriteString(`</table></div>`)
	}
	b.WriteString(`</main></html>`)
	return b.String()
}

func dashFor(e Edge) string {
	if e.Via == "when" {
		return ` stroke-dasharray="6 5"`
	}
	return ""
}

func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func condTitle(n Node) string {
	if n.When == "" {
		return ""
	}
	return "\nonly if: " + n.When
}

func noteTitle(run *PageRun, id string) string {
	if run == nil {
		return ""
	}
	if st, ok := run.State[id]; ok && st.Note != "" {
		return "\n" + st.Note
	}
	return ""
}
