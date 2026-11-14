package engine

import (
	"fmt"
	"sort"
	"strings"
)

// Edge says that step To uses an output of step From (a reference such as ${from.output}) or
// follows it in a condition.
type Edge struct {
	From, To string
	Via      string // "data" (an input) or "when" (a condition)
}

// Node is a step as the picture needs it.
type Node struct {
	Order     int // position in the file: the order steps run in
	ID, Block string
	Effect    Effect
	Level     int    // column: 0 for steps that read nothing from earlier steps
	When      string // condition, if any
	Reads     []string
}

type Graph struct {
	Name  string
	Every string
	Nodes []Node
	Edges []Edge
}

// BuildGraph derives the structure of a workflow from its definition alone.
func BuildGraph(w Workflow) Graph {
	g := Graph{Name: w.Name, Every: w.Every}
	level := map[string]int{}
	known := map[string]bool{}
	for _, st := range w.Steps {
		known[st.ID] = true
	}
	for _, st := range w.Steps {
		n := Node{Order: len(g.Nodes) + 1, ID: st.ID, Block: st.Block, When: st.When}
		if b, ok := registry[st.Block]; ok {
			n.Effect = b.Spec().Effect
		}
		deps := map[string]string{}
		collect(st.In, deps, "data", known, st.ID)
		collect(st.Key, deps, "data", known, st.ID)
		for _, ref := range whenRefs(st.When, known, st.ID) {
			if _, has := deps[ref]; !has {
				deps[ref] = "when"
			}
		}
		for from, via := range deps {
			g.Edges = append(g.Edges, Edge{From: from, To: st.ID, Via: via})
			n.Reads = append(n.Reads, from)
			if level[from]+1 > n.Level {
				n.Level = level[from] + 1
			}
		}
		sort.Strings(n.Reads)
		level[st.ID] = n.Level
		g.Nodes = append(g.Nodes, n)
	}
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].To != g.Edges[j].To {
			return indexOf(g.Nodes, g.Edges[i].To) < indexOf(g.Nodes, g.Edges[j].To)
		}
		return g.Edges[i].From < g.Edges[j].From
	})
	return g
}

func indexOf(ns []Node, id string) int {
	for i, n := range ns {
		if n.ID == id {
			return i
		}
	}
	return -1
}

func collect(v any, deps map[string]string, via string, known map[string]bool, self string) {
	switch t := v.(type) {
	case string:
		for _, m := range interpRe.FindAllStringSubmatch(t, -1) {
			if known[m[1]] && m[1] != self {
				deps[m[1]] = via
			}
		}
	case map[string]any:
		for _, e := range t {
			collect(e, deps, via, known, self)
		}
	case []any:
		for _, e := range t {
			collect(e, deps, via, known, self)
		}
	}
}

// whenRefs lists the steps a condition mentions: any known step id used as a word.
func whenRefs(when string, known map[string]bool, self string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range splitWords(when) {
		if known[w] && w != self && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

func splitWords(s string) []string {
	var words []string
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		if r == '_' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			cur.WriteRune(r)
		} else if r == '.' {
			flush() // a.b: the first word is the step, the field after the dot is not
			cur.Reset()
		} else {
			flush()
		}
	}
	flush()
	return words
}

// StepState is what a past run did with a step, for the overlay.
type StepState struct {
	Status string // ran | skipped | failed | planned | (empty: did not get that far)
	Millis int64
	Note   string
}

// RenderText draws the graph as text. With a run overlay each step shows what happened to it.
// width is the terminal width, used to decide how much of a step's detail fits.
func RenderText(g Graph, overlay map[string]StepState, color bool) string {
	var b strings.Builder
	title := g.Name
	if g.Every != "" {
		title += "   runs every " + strings.TrimPrefix(strings.TrimPrefix(g.Every, "every "), "")
	}
	b.WriteString(title + "\n")
	b.WriteString(strings.Repeat("=", min(len([]rune(title)), 72)) + "\n")
	b.WriteString("Legend:  " + marker(Read, color) + " reads   " + marker(WriteInternal, color) + " saves state here   " +
		marker(WriteExternal, color) + " changes something outside   (?) runs only if its condition holds\n\n")

	b.WriteString("Steps run in the numbered order. Layers group steps by what they depend on.\n\n")
	byLevel := map[int][]Node{}
	maxLevel := 0
	for _, n := range g.Nodes {
		byLevel[n.Level] = append(byLevel[n.Level], n)
		if n.Level > maxLevel {
			maxLevel = n.Level
		}
	}
	for lv := 0; lv <= maxLevel; lv++ {
		nodes := byLevel[lv]
		if len(nodes) == 0 {
			continue
		}
		head := "Start"
		if lv > 0 {
			head = fmt.Sprintf("Then (after %d earlier layer%s)", lv, map[bool]string{true: "", false: "s"}[lv == 1])
		}
		b.WriteString(head + "\n")
		for _, n := range nodes {
			b.WriteString(nodeLine(g, n, overlay, color))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func nodeLine(g Graph, n Node, overlay map[string]StepState, color bool) string {
	var b strings.Builder
	cond := "   "
	if n.When != "" {
		cond = "(?)"
	}
	state := ""
	if overlay != nil {
		st, ok := overlay[n.ID]
		switch {
		case !ok || st.Status == "":
			state = "  · not reached"
		case st.Status == "failed":
			state = "  ✗ FAILED " + ms(st.Millis)
		case st.Status == "skipped":
			state = "  – skipped"
		case st.Status == "planned":
			state = "  ○ would run"
		default:
			state = "  ✓ ran " + ms(st.Millis)
		}
		if st.Note != "" {
			state += "  " + st.Note
		}
	}
	b.WriteString(fmt.Sprintf("  %2d. %s %s %-17s %-19s%s\n", n.Order, marker(n.Effect, color), cond, n.ID, n.Block, state))
	if len(n.Reads) > 0 {
		b.WriteString("            uses: " + strings.Join(n.Reads, ", ") + "\n")
	}
	if n.When != "" {
		b.WriteString("            only if: " + n.When + "\n")
	}
	return b.String()
}

func ms(v int64) string {
	if v <= 0 {
		return ""
	}
	if v >= 1000 {
		return fmt.Sprintf("%.1fs", float64(v)/1000)
	}
	return fmt.Sprintf("%dms", v)
}

func marker(e Effect, color bool) string {
	sym := map[Effect]string{Read: "◇", WriteInternal: "◆", WriteExternal: "■"}[e]
	if sym == "" {
		sym = "?"
	}
	if !color {
		return sym
	}
	code := map[Effect]string{Read: "36", WriteInternal: "33", WriteExternal: "31"}[e]
	return "\x1b[" + code + "m" + sym + "\x1b[0m"
}

// RenderMermaid draws the same graph as a Mermaid flowchart, for docs or a pull request.
func RenderMermaid(g Graph) string {
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	for _, n := range g.Nodes {
		label := n.ID + "<br/>" + n.Block
		switch n.Effect {
		case WriteExternal:
			b.WriteString(fmt.Sprintf("  %s[\"%s\"]:::outside\n", mid(n.ID), label))
		case WriteInternal:
			b.WriteString(fmt.Sprintf("  %s([\"%s\"]):::state\n", mid(n.ID), label))
		default:
			b.WriteString(fmt.Sprintf("  %s(\"%s\"):::reads\n", mid(n.ID), label))
		}
	}
	for _, e := range g.Edges {
		if e.Via == "when" {
			b.WriteString(fmt.Sprintf("  %s -. only if .-> %s\n", mid(e.From), mid(e.To)))
		} else {
			b.WriteString(fmt.Sprintf("  %s --> %s\n", mid(e.From), mid(e.To)))
		}
	}
	b.WriteString("  classDef reads fill:#e6f4f7,stroke:#2b8ca3\n  classDef state fill:#fff4d6,stroke:#b8860b\n  classDef outside fill:#fde2e2,stroke:#c0392b\n")
	return b.String()
}

func mid(id string) string { return "s_" + strings.ReplaceAll(id, "-", "_") }
