package theme

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestMeterBarProportions(t *testing.T) {
	if got := MeterBar(10, 10, 5); lipgloss.Width(got) != 5 {
		t.Errorf("полный столбик: got %q (%d ячеек), want 5", got, lipgloss.Width(got))
	}
	if got := MeterBar(0, 10, 5); strings.Contains(got, "█") {
		t.Errorf("нулевое значение не должно закрашивать ни одной ячейки: got %q", got)
	}
	// Значение больше пика не должно вылезать за отведённые ячейки.
	if got := MeterBar(99, 10, 5); lipgloss.Width(got) > 5 {
		t.Errorf("переполнение: got %q (%d ячеек)", got, lipgloss.Width(got))
	}
	// Нулевой пик — тот же случай, что "ничего не пробовали": по-прежнему
	// полноширинный тихий столбик, а не паника на делении на ноль.
	if got := MeterBar(5, 0, 5); lipgloss.Width(got) != 5 {
		t.Errorf("нулевой пик: got %q (%d ячеек), want 5", got, lipgloss.Width(got))
	}
}
