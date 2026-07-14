package acceptance

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestAcceptanceGuideKeepsNineteenScenarios 防止原项目的验收项目在重写中被无意删减。
// 每项的实际行为由 TEST-GUIDE.md 中映射到 internal/* 的自动化测试验证。
func TestAcceptanceGuideKeepsNineteenScenarios(t *testing.T) {
	guide, err := os.ReadFile("TEST-GUIDE.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(guide)
	for index := 1; index <= 19; index++ {
		t.Run(fmt.Sprintf("scenario_%02d", index), func(t *testing.T) {
			if !strings.Contains(text, fmt.Sprintf("## %d.", index)) {
				t.Fatalf("scenario %d is missing from TEST-GUIDE.md", index)
			}
		})
	}
	if strings.Contains(text, "## 20.") {
		t.Fatal("acceptance guide unexpectedly contains a twentieth numbered scenario")
	}
}
