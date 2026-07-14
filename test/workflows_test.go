package acceptance

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"
)

// TestGitHubWorkflowsAreValidYAML 确保 CI 和自动发布工作流至少保持为可解析的 YAML。
func TestGitHubWorkflowsAreValidYAML(t *testing.T) {
	for _, name := range []string{"ci.yml", "release.yml"} {
		t.Run(name, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join("..", ".github", "workflows", name))
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := yaml.Unmarshal(content, &document); err != nil {
				t.Fatalf("invalid workflow YAML: %v", err)
			}
			if document["name"] == nil || document["on"] == nil || document["jobs"] == nil {
				t.Fatalf("workflow is missing name, on, or jobs: %#v", document)
			}
			if name == "release.yml" {
				for _, required := range [][]byte{[]byte(".env.example"), []byte("windows linux darwin"), []byte("amd64 arm64"), []byte("SHA256SUMS"), []byte("gh release create")} {
					if !bytes.Contains(content, required) {
						t.Fatalf("release workflow is missing %q", required)
					}
				}
			}
		})
	}
}
