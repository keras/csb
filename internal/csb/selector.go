package csb

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

type selectorEntry struct {
	label    string
	selected bool
	isHeader bool
	imageID  string
	volName  string
}

// runSelector shows an interactive terminal checkbox selector.
// Returns (selected entries, true) on confirm, or (nil, false) on abort.
// Returns (nil, false) immediately if stdin is not a terminal.
func runSelector(entries []selectorEntry) ([]selectorEntry, bool) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, false
	}

	cursor := firstSelectable(entries, 0, 1)

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, false
	}
	defer term.Restore(fd, oldState)  //nolint
	defer fmt.Print("\033[?25h")      // restore cursor visibility

	fmt.Print("\033[?25l") // hide cursor

	lineCount := 0
	render := func() {
		if lineCount > 0 {
			fmt.Printf("\033[%dA", lineCount)
		}
		lineCount = 0
		line := func(s string) {
			fmt.Printf("\r\033[K%s\r\n", s)
			lineCount++
		}

		line("  space=toggle  a=all  n=none  enter=confirm  q=quit")
		line("")
		for i, e := range entries {
			if e.isHeader {
				if e.label == "" {
					line("")
				} else {
					line("  " + e.label)
				}
			} else {
				ind := " "
				if i == cursor {
					ind = ">"
				}
				chk := " "
				if e.selected {
					chk = "x"
				}
				line(fmt.Sprintf("  %s [%s]  %s", ind, chk, e.label))
			}
		}
		line("")
		n := countSelected(entries)
		line(fmt.Sprintf("  %d item(s) selected", n))
	}

	render()

	buf := make([]byte, 4)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return nil, false
		}
		b := buf[:n]

		switch {
		case n == 1 && (b[0] == 'q' || b[0] == 'Q'):
			return nil, false
		case n == 1 && b[0] == 0x1b: // bare Escape
			return nil, false
		case n == 1 && (b[0] == '\r' || b[0] == '\n'):
			var sel []selectorEntry
			for _, e := range entries {
				if !e.isHeader && e.selected {
					sel = append(sel, e)
				}
			}
			return sel, true
		case n == 1 && b[0] == ' ':
			if !entries[cursor].isHeader {
				entries[cursor].selected = !entries[cursor].selected
			}
		case n == 1 && (b[0] == 'a' || b[0] == 'A'):
			for i := range entries {
				if !entries[i].isHeader {
					entries[i].selected = true
				}
			}
		case n == 1 && (b[0] == 'n' || b[0] == 'N'):
			for i := range entries {
				if !entries[i].isHeader {
					entries[i].selected = false
				}
			}
		case n >= 3 && b[0] == 0x1b && b[1] == '[' && b[2] == 'A': // up arrow
			cursor = firstSelectable(entries, cursor-1, -1)
		case n >= 3 && b[0] == 0x1b && b[1] == '[' && b[2] == 'B': // down arrow
			cursor = firstSelectable(entries, cursor+1, 1)
		}

		render()
	}
}

// firstSelectable returns the index of the nearest selectable entry starting
// at start, stepping by dir (+1 or -1), wrapping around.
func firstSelectable(entries []selectorEntry, start, dir int) int {
	total := len(entries)
	if total == 0 {
		return 0
	}
	i := ((start % total) + total) % total
	for range entries {
		if !entries[i].isHeader {
			return i
		}
		i = ((i + dir) % total + total) % total
	}
	return ((start % total) + total) % total
}

func countSelected(entries []selectorEntry) int {
	n := 0
	for _, e := range entries {
		if !e.isHeader && e.selected {
			n++
		}
	}
	return n
}
