package permission

import "testing"

func TestPlanModeOnlyAllowsPlanFile(t *testing.T) {
	e := New(ModePlan, t.TempDir(), t.TempDir())
	e.PlanFilePath = "plan.md"
	if got := e.Check("write_file", map[string]any{"file_path": "other.md"}); got.Action != ActionDeny {
		t.Fatalf("other file = %s", got.Action)
	}
	if got := e.Check("write_file", map[string]any{"file_path": "plan.md"}); got.Action != ActionAllow {
		t.Fatalf("plan file = %s", got.Action)
	}
}

func TestDangerousCommandNeedsConfirmation(t *testing.T) {
	e := New(ModeDefault, t.TempDir(), t.TempDir())
	if got := e.Check("run_shell", map[string]any{"command": "git reset --hard"}); got.Action != ActionConfirm {
		t.Fatalf("action = %s", got.Action)
	}
}
