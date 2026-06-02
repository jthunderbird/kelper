package kubeconfig

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	wizardTitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("33"))
	wizardSelectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("36"))
	wizardDimStyle      = lipgloss.NewStyle().Faint(true)
	wizardErrorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

type wizardStep int

const (
	stepAccountType wizardStep = iota
	stepUsername
	stepNamespace
	stepAPIGroups  // scoped only
	stepResources  // scoped only
	stepVerbs      // scoped only
	stepOutput
	stepConfirm
	stepDone
)

var accountTypes = []AccountType{
	AccountTypeReadonly,
	AccountTypeAdmin,
	AccountTypeCluster,
	AccountTypeScoped,
}

var accountTypeLabels = map[AccountType]string{
	AccountTypeReadonly: "Readonly (namespace-scoped)",
	AccountTypeAdmin:    "Admin (namespace-scoped)",
	AccountTypeCluster:  "Cluster-wide readonly",
	AccountTypeScoped:   "Resource-scoped (custom)",
}

type wizardModel struct {
	step      wizardStep
	opts      Options
	cursor    int
	textInput string
	errMsg    string
	confirmed bool
}

// NewWizardModel returns an initial wizard model (for testing).
func NewWizardModel() wizardModel {
	return wizardModel{}
}

// Step returns the current wizard step index (for testing).
func (m wizardModel) Step() int {
	return int(m.step)
}

// Init implements tea.Model.
func (m wizardModel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m wizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch m.step {
	case stepAccountType:
		switch keyMsg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(accountTypes)-1 {
				m.cursor++
			}
		case "enter":
			m.opts.AccountType = accountTypes[m.cursor]
			m.cursor = 0
			m.step = stepUsername
		case "q", "ctrl+c":
			return m, tea.Quit
		}

	case stepUsername, stepNamespace, stepAPIGroups, stepResources, stepVerbs, stepOutput:
		switch keyMsg.String() {
		case "enter":
			m = m.advanceText()
		case "ctrl+c":
			return m, tea.Quit
		case "backspace":
			if len(m.textInput) > 0 {
				m.textInput = m.textInput[:len(m.textInput)-1]
			}
		default:
			if len(keyMsg.String()) == 1 {
				m.textInput += keyMsg.String()
			}
		}

	case stepConfirm:
		switch keyMsg.String() {
		case "y", "Y":
			m.confirmed = true
			m.step = stepDone
			return m, tea.Quit
		case "n", "N", "q", "ctrl+c":
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m wizardModel) advanceText() wizardModel {
	val := strings.TrimSpace(m.textInput)
	m.textInput = ""
	m.errMsg = ""

	switch m.step {
	case stepUsername:
		if val == "" {
			m.errMsg = "username cannot be empty"
			return m
		}
		m.opts.Username = val
		if m.opts.AccountType == AccountTypeCluster {
			m.step = stepOutput
		} else {
			m.step = stepNamespace
		}
	case stepNamespace:
		if val == "" {
			m.errMsg = "namespace cannot be empty"
			return m
		}
		m.opts.Namespace = val
		if m.opts.AccountType == AccountTypeScoped {
			m.step = stepAPIGroups
		} else {
			m.step = stepOutput
		}
	case stepAPIGroups:
		m.opts.APIGroups = splitCSV(val)
		if len(m.opts.APIGroups) == 0 {
			m.opts.APIGroups = []string{"*"}
		}
		m.step = stepResources
	case stepResources:
		if val == "" {
			m.errMsg = "resources cannot be empty"
			return m
		}
		m.opts.Resources = splitCSV(val)
		m.step = stepVerbs
	case stepVerbs:
		m.opts.Verbs = splitCSV(val)
		if len(m.opts.Verbs) == 0 {
			m.opts.Verbs = []string{"get", "list", "watch"}
		}
		m.step = stepOutput
	case stepOutput:
		m.opts.OutputFile = val // empty = stdout
		m.step = stepConfirm
	}
	return m
}

// View implements tea.Model.
func (m wizardModel) View() string {
	var b strings.Builder

	total := m.totalSteps()
	current := int(m.step) + 1
	if current > total {
		current = total
	}
	b.WriteString(wizardTitleStyle.Render(
		fmt.Sprintf("kelper kubeconfig wizard  (step %d/%d)", current, total)) + "\n\n")

	switch m.step {
	case stepAccountType:
		b.WriteString("Select account type:\n\n")
		for i, at := range accountTypes {
			label := accountTypeLabels[at]
			if i == m.cursor {
				b.WriteString(wizardSelectedStyle.Render("> "+label) + "\n")
			} else {
				b.WriteString(wizardDimStyle.Render("  "+label) + "\n")
			}
		}
	case stepUsername:
		b.WriteString("Username: " + m.textInput + "█\n")
	case stepNamespace:
		b.WriteString("Namespace: " + m.textInput + "█\n")
	case stepAPIGroups:
		b.WriteString("API groups (comma-separated, * for all): " + m.textInput + "█\n")
	case stepResources:
		b.WriteString("Resources (comma-separated, e.g. pods,secrets): " + m.textInput + "█\n")
	case stepVerbs:
		b.WriteString("Verbs (comma-separated, e.g. get,list,watch): " + m.textInput + "█\n")
	case stepOutput:
		b.WriteString("Output file (leave blank for stdout): " + m.textInput + "█\n")
	case stepConfirm:
		b.WriteString("Confirm:\n\n")
		b.WriteString(fmt.Sprintf("  Type:      %s\n", accountTypeLabels[m.opts.AccountType]))
		b.WriteString(fmt.Sprintf("  Username:  %s\n", m.opts.Username))
		if m.opts.Namespace != "" {
			b.WriteString(fmt.Sprintf("  Namespace: %s\n", m.opts.Namespace))
		}
		if len(m.opts.Verbs) > 0 {
			b.WriteString(fmt.Sprintf("  Verbs:     %s\n", strings.Join(m.opts.Verbs, ", ")))
		}
		if len(m.opts.Resources) > 0 {
			b.WriteString(fmt.Sprintf("  Resources: %s\n", strings.Join(m.opts.Resources, ", ")))
		}
		out := "stdout"
		if m.opts.OutputFile != "" {
			out = m.opts.OutputFile
		}
		b.WriteString(fmt.Sprintf("  Output:    %s\n", out))
		b.WriteString("\n  [ y ] Create User     [ n ] Cancel\n")
	}

	if m.errMsg != "" {
		b.WriteString("\n" + wizardErrorStyle.Render("error: "+m.errMsg) + "\n")
	}
	b.WriteString("\n" + wizardDimStyle.Render("ctrl+c to quit") + "\n")
	return b.String()
}

func (m wizardModel) totalSteps() int {
	switch m.opts.AccountType {
	case AccountTypeScoped:
		return 8 // type, user, ns, apigroups, resources, verbs, output, confirm
	case AccountTypeCluster:
		return 4 // type, user, output, confirm
	default:
		return 5 // type, user, ns, output, confirm
	}
}

// runTUI launches the kubeconfig wizard and runs the result if confirmed.
func runTUI() error {
	m := NewWizardModel()
	p := tea.NewProgram(m, tea.WithAltScreen())
	result, err := p.Run()
	if err != nil {
		return err
	}
	final, ok := result.(wizardModel)
	if !ok || !final.confirmed {
		fmt.Println("Cancelled.")
		return nil
	}
	return Create(final.opts)
}
