package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"testing"
)

func TestSearchStacks(t *testing.T) {
	m := &model{stacks: []stack{{Name: "Alpha"}, {Name: "Beta"}, {Name: "Alpine"}}}
	key := func(k tea.KeyMsg) { m.Update(k) }
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("AL")})
	matches := m.searchMatches()
	if len(matches) != 2 || matches[0] != 0 || matches[1] != 2 {
		t.Fatalf("matches: %v", matches)
	}
	key(tea.KeyMsg{Type: tea.KeyDown})
	if m.searchIndex != 1 {
		t.Fatalf("selection: %d", m.searchIndex)
	}
	key(tea.KeyMsg{Type: tea.KeyEnter})
	if m.searching || m.stackIndex != 2 {
		t.Fatalf("searching=%v stack=%d", m.searching, m.stackIndex)
	}
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
	key(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.searching || m.stackIndex != 2 {
		t.Fatal("empty results changed selection")
	}
	key(tea.KeyMsg{Type: tea.KeyEsc})
	if m.searching {
		t.Fatal("escape did not close search")
	}
}
