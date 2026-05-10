// Package dag renders a Unicode box-drawing diagram of a zenflow
// workflow DAG. It is a CLI-only concern (used by `zenflow plan` and the
// stdout sink); the library does not depend on it.
package dag

import (
	"fmt"
	"math"
	"strings"

	"github.com/zendev-sh/zenflow"
)

// Render produces a Unicode box-drawing diagram of the workflow DAG.
func Render(wf *zenflow.Workflow) string {
	if wf == nil || len(wf.Steps) == 0 {
		return ""
	}

	stepMap := make(map[string]zenflow.Step, len(wf.Steps))
	for _, s := range wf.Steps {
		stepMap[s.ID] = s
	}

	layers := assignLayers(wf.Steps, stepMap)
	maxLayer := 0
	for _, l := range layers {
		maxLayer = max(maxLayer, l)
	}

	layerGroups := make([][]string, maxLayer+1)
	order, _ := zenflow.TopoSort(wf.Steps)
	for _, id := range order {
		layerGroups[layers[id]] = append(layerGroups[layers[id]], id)
	}

	boxContent := make(map[string]dagBoxContent)
	for _, s := range wf.Steps {
		boxContent[s.ID] = buildContent(s)
	}

	// --- Pass 1: compute positions ---

	// Layer widths for centering.
	layerWidths := make([]int, maxLayer+1)
	layerBoxWidths := make([][]int, maxLayer+1)
	maxTotalWidth := 0
	for layer := range maxLayer + 1 {
		group := layerGroups[layer]
		widths := make([]int, 0, len(group))
		total := 0
		for i, id := range group {
			w := boxContent[id].innerWidth + 2
			widths = append(widths, w)
			if i > 0 {
				total += 2
			}
			total += w
		}
		layerWidths[layer] = total
		layerBoxWidths[layer] = widths
		maxTotalWidth = max(maxTotalWidth, total)
	}

	// Compute Y positions and box centers for each layer.
	type layerPos struct {
		boxY    int // y where boxes start
		bottomY int // y after boxes end (first connector row)
	}
	positions := make([]layerPos, maxLayer+1)
	boxCenters := make(map[string]int)

	y := 2 // after title
	for layer := range maxLayer + 1 {
		group := layerGroups[layer]

		positions[layer] = layerPos{boxY: y}

		// Compute box centers.
		startX := (maxTotalWidth - layerWidths[layer]) / 2
		x := startX
		maxHeight := 0
		for i, id := range group {
			if i > 0 {
				x += 2
			}
			w := layerBoxWidths[layer][i]
			boxCenters[id] = x + w/2
			h := len(boxContent[id].lines) + 2
			maxHeight = max(maxHeight, h)
			x += w
		}

		positions[layer] = layerPos{boxY: y, bottomY: y + maxHeight}
		y += maxHeight

		// Reserve space for connectors (estimate).
		if layer < maxLayer {
			y += 3 // │ + merge bar + │ + ▼ (worst case)
		}
	}

	// Identify all edges, including cross-layer.
	type edgeInfo struct {
		fromID    string
		toID      string
		fromLayer int
		toLayer   int
	}
	totalEdges := 0
	for _, s := range wf.Steps {
		totalEdges += len(s.DependsOn)
	}
	allEdges := make([]edgeInfo, 0, totalEdges)
	for _, s := range wf.Steps {
		for _, dep := range s.DependsOn {
			allEdges = append(allEdges, edgeInfo{
				fromID:    dep,
				toID:      s.ID,
				fromLayer: layers[dep],
				toLayer:   layers[s.ID],
			})
		}
	}

	// Find pass-through edges (span > 1 layer).
	// For each intermediate layer, we route the pass-through line to the left
	// of all boxes (startX - 2) to avoid overlapping with box content.
	type ptInfo struct {
		x     int // x position of the pass-through line at this layer
		origX int // original source center x
	}
	passThrough := make(map[int][]ptInfo) // layer → pass-through lines
	for _, e := range allEdges {
		if e.toLayer-e.fromLayer > 1 {
			cx := boxCenters[e.fromID]
			for l := e.fromLayer + 1; l < e.toLayer; l++ {
				// Route to the left of boxes in this layer.
				layerStart := (maxTotalWidth - layerWidths[l]) / 2
				ptX := layerStart - 2
				if ptX < 0 {
					ptX = 0
				}
				passThrough[l] = append(passThrough[l], ptInfo{x: ptX, origX: cx})
			}
		}
	}
	// Also update boxCenters for pass-through sources: at destination layer's
	// connector, the source center should reflect the pass-through x.
	// We store these overrides to use in connector drawing.
	ptCenterOverride := make(map[string]int) // fromID → override x at destination
	for _, e := range allEdges {
		if e.toLayer-e.fromLayer > 1 {
			// The pass-through line arrives at the layer just before destination
			// at the shifted x position.
			destPrevLayer := e.toLayer - 1
			layerStart := (maxTotalWidth - layerWidths[destPrevLayer]) / 2
			ptX := layerStart - 2
			if ptX < 0 {
				ptX = 0
			}
			ptCenterOverride[e.fromID] = ptX
		}
	}

	// --- Pass 2: render onto canvas ---
	c := &canvas{}
	c.init(maxTotalWidth+20, y+10)

	title := fmt.Sprintf("%s (%d steps)", wf.Name, len(wf.Steps))
	c.writeStr(0, 0, title)

	// Re-compute Y positions while rendering (pass 1 was approximate).
	y = 2
	for layer := range maxLayer + 1 {
		group := layerGroups[layer]

		positions[layer] = layerPos{boxY: y}

		// Draw boxes.
		startX := (maxTotalWidth - layerWidths[layer]) / 2
		x := startX
		maxHeight := 0
		for i, id := range group {
			if i > 0 {
				x += 2
			}
			bc := boxContent[id]
			w := layerBoxWidths[layer][i]
			h := len(bc.lines) + 2
			drawBox(c, x, y, w, bc.lines)
			boxCenters[id] = x + w/2
			maxHeight = max(maxHeight, h)
			x += w
		}

		// Draw pass-through lines alongside boxes in this layer.
		if pts, ok := passThrough[layer]; ok {
			for _, pt := range pts {
				for row := y; row < y+maxHeight; row++ {
					c.writeStr(pt.x, row, "│")
				}
			}
		}

		positions[layer] = layerPos{boxY: y, bottomY: y + maxHeight}
		y += maxHeight

		// Draw connectors to next layer.
		if layer < maxLayer {
			connStartY := y
			allPrevious := make([]string, 0, len(order))
			for l := range layer + 1 {
				allPrevious = append(allPrevious, layerGroups[l]...)
			}
			nextGroup := layerGroups[layer+1]
			// Build effective centers: use pass-through x for cross-layer sources.
			effectiveCenters := make(map[string]int)
			for k, v := range boxCenters {
				effectiveCenters[k] = v
			}
			for k, v := range ptCenterOverride {
				effectiveCenters[k] = v
			}
			y = drawConnectors(c, y, allPrevious, nextGroup, effectiveCenters, stepMap)

			// Draw pass-through lines through the connector area for edges
			// that pass through the next layer.
			if pts, ok := passThrough[layer+1]; ok {
				for _, pt := range pts {
					// Draw │ from connector start to end.
					for row := connStartY; row < y; row++ {
						if c.getCell(pt.x, row) == ' ' {
							c.writeStr(pt.x, row, "│")
						}
					}
					// Draw horizontal bend from source center to pass-through x
					// if the source is in this layer (first transition).
					if pt.origX != pt.x {
						bendY := connStartY // use first row of connector area
						for bx := pt.x; bx <= pt.origX; bx++ {
							ch := c.getCell(bx, bendY)
							if ch == ' ' {
								c.writeStr(bx, bendY, "─")
							}
						}
						// Corner: pass-through is left of source → ┌──┘
						c.writeStr(pt.x, bendY, "┌")
						c.writeStr(pt.origX, bendY, "┘")
					}
				}
			}
		}
	}

	return c.render()
}

// dagBoxContent holds the content lines and computed inner width.
type dagBoxContent struct {
	lines      []string
	innerWidth int
}

func buildContent(s zenflow.Step) dagBoxContent {
	line1 := s.ID
	parts := make([]string, 0, 5)
	if s.Agent != "" {
		parts = append(parts, fmt.Sprintf("(%s)", s.Agent))
	}
	if s.Loop != nil {
		parts = append(parts, "[loop]")
	}
	if s.Condition != nil {
		parts = append(parts, "[if]")
	}
	if s.Include != "" {
		parts = append(parts, "[ref]")
	}
	if n := len(s.ContextFiles); n == 1 {
		parts = append(parts, "[1 file]")
	} else if n > 1 {
		parts = append(parts, fmt.Sprintf("[%d files]", n))
	}
	line2 := strings.Join(parts, " ")

	var lines []string
	lines = append(lines, line1)
	if line2 != "" {
		lines = append(lines, line2)
	}

	maxLen := 0
	for _, l := range lines {
		maxLen = max(maxLen, strWidth(l))
	}
	inner := max(maxLen+2, 6)
	return dagBoxContent{lines: lines, innerWidth: inner}
}

func drawBox(c *canvas, x, y, width int, lines []string) {
	inner := width - 2
	c.writeStr(x, y, "┌"+strings.Repeat("─", inner)+"┐")
	for i, line := range lines {
		pad := inner - strWidth(line) - 1
		c.writeStr(x, y+1+i, "│ "+line+spaces(pad)+"│")
	}
	c.writeStr(x, y+1+len(lines), "└"+strings.Repeat("─", inner)+"┘")
}

func drawConnectors(c *canvas, y int, fromGroup, toGroup []string, boxCenters map[string]int, stepMap map[string]zenflow.Step) int {
	fromSet := make(map[string]bool, len(fromGroup))
	for _, id := range fromGroup {
		fromSet[id] = true
	}

	// Collect active source and target positions from actual edges.
	srcPos := make(map[int]bool)
	tgtPos := make(map[int]bool)
	for _, toID := range toGroup {
		s := stepMap[toID]
		for _, dep := range s.DependsOn {
			if fromSet[dep] {
				srcPos[boxCenters[dep]] = true
				tgtPos[boxCenters[toID]] = true
			}
		}
	}

	if len(srcPos) == 0 {
		return y
	}

	// Simple case: single source, single target, same column - stem + arrow.
	if len(srcPos) == 1 && len(tgtPos) == 1 {
		for sx := range srcPos {
			for tx := range tgtPos {
				if sx == tx {
					c.writeStr(sx, y, "│")
					y++
					c.writeStr(sx, y, "▼")
					y++
					return y
				}
			}
		}
	}

	// Vertical pipes from sources.
	for cx := range srcPos {
		c.writeStr(cx, y, "│")
	}
	y++

	// Find bar extents (union of source and target positions).
	barMin, barMax := math.MaxInt, 0
	for x := range srcPos {
		barMin = min(barMin, x)
		barMax = max(barMax, x)
	}
	for x := range tgtPos {
		barMin = min(barMin, x)
		barMax = max(barMax, x)
	}

	// Draw horizontal bar using box-drawing characters.
	// Each position connects: LEFT (not leftmost), RIGHT (not rightmost),
	// UP (if source), DOWN (if target).
	for x := barMin; x <= barMax; x++ {
		up := srcPos[x]
		down := tgtPos[x]
		atLeft := x == barMin
		atRight := x == barMax

		var ch string
		switch {
		// Endpoints (leftmost).
		case atLeft && up && down:
			ch = "├"
		case atLeft && up:
			ch = "└"
		case atLeft && down:
			ch = "┌"
			// Endpoints (rightmost).
		case atRight && up && down:
			ch = "┤"
		case atRight && up:
			ch = "┘"
		case atRight && down:
			ch = "┐"
			// Interior.
		case up && down:
			ch = "┼"
		case up:
			ch = "┴"
		case down:
			ch = "┬"
		default:
			ch = "─"
		}
		c.writeStr(x, y, ch)
	}
	y++

	// Stems at target positions.
	for cx := range tgtPos {
		c.writeStr(cx, y, "│")
	}
	y++

	// Arrow heads at target positions.
	for cx := range tgtPos {
		c.writeStr(cx, y, "▼")
	}
	y++

	return y
}

func strWidth(s string) int { return len([]rune(s)) }

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(" ", n)
}

func assignLayers(steps []zenflow.Step, stepMap map[string]zenflow.Step) map[string]int {
	layers := make(map[string]int, len(steps))
	var resolve func(id string) int
	resolve = func(id string) int {
		if l, ok := layers[id]; ok {
			return l
		}
		s := stepMap[id]
		maxDep := -1
		for _, dep := range s.DependsOn {
			maxDep = max(maxDep, resolve(dep))
		}
		layers[id] = maxDep + 1
		return maxDep + 1
	}
	for _, s := range steps {
		resolve(s.ID)
	}
	return layers
}

// --- canvas ---

type canvas struct {
	cells [][]rune
	w, h  int
}

func (c *canvas) init(w, h int) {
	c.w = w
	c.h = h
	c.cells = make([][]rune, h)
	for i := range c.cells {
		c.cells[i] = make([]rune, w)
		for j := range c.cells[i] {
			c.cells[i][j] = ' '
		}
	}
}

func (c *canvas) ensure(x, y int) {
	for y >= c.h {
		newRow := make([]rune, c.w)
		for j := range newRow {
			newRow[j] = ' '
		}
		c.cells = append(c.cells, newRow)
		c.h++
	}
	if x >= c.w {
		newW := x + 50
		for i := range c.cells {
			ext := make([]rune, newW-c.w)
			for j := range ext {
				ext[j] = ' '
			}
			c.cells[i] = append(c.cells[i], ext...)
		}
		c.w = newW
	}
}

func (c *canvas) writeStr(x, y int, s string) {
	runes := []rune(s)
	c.ensure(x+len(runes), y)
	for i, r := range runes {
		c.cells[y][x+i] = r
	}
}

func (c *canvas) getCell(x, y int) rune {
	if y >= c.h || x >= c.w {
		return ' '
	}
	return c.cells[y][x]
}

func (c *canvas) render() string {
	lastRow := len(c.cells) - 1
	for lastRow >= 0 {
		empty := true
		for _, r := range c.cells[lastRow] {
			if r != ' ' {
				empty = false
				break
			}
		}
		if !empty {
			break
		}
		lastRow--
	}

	var sb strings.Builder
	for i := range lastRow + 1 {
		row := c.cells[i]
		last := len(row) - 1
		for last >= 0 && row[last] == ' ' {
			last--
		}
		if last >= 0 {
			sb.WriteString(string(row[:last+1]))
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}
