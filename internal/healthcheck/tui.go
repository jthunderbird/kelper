package healthcheck

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const refreshInterval = 10 * time.Second

var (
	tuiSelectedStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("33"))
	tuiUnselectedStyle = lipgloss.NewStyle().Faint(true)
	tuiFooterStyle     = lipgloss.NewStyle().Faint(true).Italic(true)
)

// TUIModel is the bubbletea model for the healthcheck TUI.
type TUIModel struct {
	cs         *kubernetes.Clientset
	namespaces []string
	cursor     int
	selected   string
	tableOut   string
	loading    bool
	err        error
}

type refreshMsg struct {
	tableOut string
	err      error
}

type tickMsg time.Time

// NewTUIModel creates a model with the given namespace list (for testing).
func NewTUIModel(namespaces []string) TUIModel {
	return TUIModel{namespaces: namespaces}
}

// Namespaces returns the model's namespace list (for testing).
func (m TUIModel) Namespaces() []string {
	return m.namespaces
}

func newTUIModelWithClient(cs *kubernetes.Clientset, namespaces []string) TUIModel {
	return TUIModel{cs: cs, namespaces: namespaces}
}

// Init implements tea.Model.
func (m TUIModel) Init() tea.Cmd {
	return tick()
}

// Update implements tea.Model.
func (m TUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.namespaces)-1 {
				m.cursor++
			}
		case "enter":
			m.selected = m.namespaces[m.cursor]
			m.loading = true
			return m, m.fetchHealth()
		}
	case refreshMsg:
		m.loading = false
		m.tableOut = msg.tableOut
		m.err = msg.err
		return m, tick()
	case tickMsg:
		if m.selected != "" {
			m.loading = true
			return m, m.fetchHealth()
		}
		return m, tick()
	}
	return m, nil
}

// View implements tea.Model.
func (m TUIModel) View() string {
	var b strings.Builder
	if m.selected == "" {
		b.WriteString("Select a namespace:\n\n")
		for i, ns := range m.namespaces {
			if i == m.cursor {
				b.WriteString(tuiSelectedStyle.Render("> "+ns) + "\n")
			} else {
				b.WriteString(tuiUnselectedStyle.Render("  "+ns) + "\n")
			}
		}
	} else {
		b.WriteString(fmt.Sprintf("Namespace: %s", tuiSelectedStyle.Render(m.selected)))
		if m.loading {
			b.WriteString("  (refreshing...)")
		}
		b.WriteString("\n\n")
		if m.err != nil {
			b.WriteString("error: " + m.err.Error() + "\n")
		} else {
			b.WriteString(m.tableOut)
		}
	}
	b.WriteString("\n" + tuiFooterStyle.Render(
		"To run non-interactively: kelper healthcheck -n <namespace>  •  q to quit",
	))
	return b.String()
}

func (m TUIModel) fetchHealth() tea.Cmd {
	return func() tea.Msg {
		ns := m.selected
		if ns == "All Namespaces" {
			ns = ""
		}
		pods, err := m.cs.CoreV1().Pods(ns).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			return refreshMsg{err: err}
		}
		var sb strings.Builder
		var unhealthyPods []string
		for _, p := range pods.Items {
			if IsUnhealthyPod(p) {
				ready := podReadyString(p)
				status := podStatusString(p)
				unhealthyPods = append(unhealthyPods,
					fmt.Sprintf("  %-40s %-8s %s", p.Name, ready, status))
			}
		}
		if len(unhealthyPods) == 0 {
			sb.WriteString("Unhealthy Pods: none\n")
		} else {
			sb.WriteString("Unhealthy Pods:\n")
			for _, line := range unhealthyPods {
				sb.WriteString(line + "\n")
			}
		}
		return refreshMsg{tableOut: sb.String()}
	}
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// runTUI fetches namespaces from the cluster and launches the bubbletea TUI.
func runTUI(cs *kubernetes.Clientset) error {
	nsList, err := cs.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list namespaces: %w", err)
	}
	namespaces := []string{"All Namespaces"}
	for _, ns := range nsList.Items {
		namespaces = append(namespaces, ns.Name)
	}
	m := newTUIModelWithClient(cs, namespaces)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}
