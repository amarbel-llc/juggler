package jugglerprovider

import (
	"strings"
	"testing"
)

const sampleScript = `
name = "file-one-issue"

[[step]]
text = "listing {{.Repo}}"
tool = "list_repos"
args = '{"owner":"{{.Owner}}"}'

[[step]]
text = "done"
`

func TestParseAndRenderScriptTemplate(t *testing.T) {
	tpl, err := ParseScriptTemplate([]byte(sampleScript))
	if err != nil {
		t.Fatal(err)
	}
	if tpl.Name != "file-one-issue" || len(tpl.Steps) != 2 {
		t.Fatalf("template = %+v", tpl)
	}
	got, err := tpl.Render(map[string]string{"Repo": "juggler", "Owner": "me"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Steps[0].Text != "listing juggler" || got.Steps[0].ToolArgs != `{"owner":"me"}` {
		t.Errorf("step 0 = %+v", got.Steps[0])
	}
	// The source template is untouched.
	if !strings.Contains(tpl.Steps[0].Text, "{{.Repo}}") {
		t.Error("Render must not mutate the template")
	}
}

func TestRenderRefusesAMissingKeyAndBadArgs(t *testing.T) {
	tpl, err := ParseScriptTemplate([]byte(sampleScript))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tpl.Render(map[string]string{"Repo": "x"}); err == nil {
		t.Error("a template key missing from params must be an error")
	}
	bad := ScriptTemplate{Name: "bad", Steps: []ScriptStep{{ToolName: "t", ToolArgs: `[1]`}}}
	if _, err := bad.Render(nil); err == nil {
		t.Error("non-object tool args must be an error")
	}
	if _, err := ParseScriptTemplate([]byte(`name = "empty"`)); err == nil {
		t.Error("a template without steps must be an error")
	}
}
