package frontmatter

import "testing"

func TestParseAndFormat(t *testing.T) {
	raw := "---\nname: commit\nuser-invocable: false\nallowed-tools: [read_file, grep_search]\n---\nDo $ARGUMENTS"
	document, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if String(document.Meta, "name") != "commit" || Bool(document.Meta, "user-invocable", true) || len(Strings(document.Meta, "allowed-tools")) != 2 {
		t.Fatalf("document = %#v", document)
	}
	formatted, err := Format(document.Meta, document.Body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(formatted); err != nil {
		t.Fatal(err)
	}
}

func TestParseAllowsEmptyBody(t *testing.T) {
	document, err := Parse("---\nname: empty\n---")
	if err != nil || String(document.Meta, "name") != "empty" || document.Body != "" {
		t.Fatalf("document=%#v err=%v", document, err)
	}
}
