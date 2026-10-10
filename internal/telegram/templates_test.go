package telegram

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// No such directory, so every template falls back to its built-in default.
	InitTemplates("testdata-does-not-exist")
	os.Exit(m.Run())
}

func TestPollApprovedListsVotersInOrder(t *testing.T) {
	text := RenderMessage("poll_approved", map[string]interface{}{
		"SiteName": "Example Site",
		"UserName": "Jane Doe",
		"Voters":   []string{"Alice", "Bob", "Carol", "Dave"},
	})

	if text == "" {
		t.Fatalf("poll_approved rendered empty")
	}
	for _, name := range []string{"Alice", "Bob", "Carol", "Dave"} {
		if !strings.Contains(text, name) {
			t.Errorf("voter %q missing from:\n%s", name, text)
		}
	}
	if !strings.Contains(text, "Example Site") || !strings.Contains(text, "Jane Doe") {
		t.Errorf("site or user missing from:\n%s", text)
	}
	if strings.Index(text, "Alice") > strings.Index(text, "Dave") {
		t.Errorf("voters are not listed in the order they were given:\n%s", text)
	}
}

func TestPollDeclinedListsVoters(t *testing.T) {
	text := RenderMessage("poll_declined", map[string]interface{}{
		"SiteName": "Example Site",
		"UserName": "Jane Doe",
		"Voters":   []string{"Alice"},
	})

	if !strings.Contains(text, "Declined") {
		t.Errorf("poll_declined does not read as a decline:\n%s", text)
	}
	if !strings.Contains(text, "Alice") {
		t.Errorf("voter missing from:\n%s", text)
	}
}

// The voter list is omitted entirely rather than rendered as an empty bullet list.
func TestPollTemplatesHandleNoVoters(t *testing.T) {
	for _, name := range []string{"poll_approved", "poll_declined"} {
		t.Run(name, func(t *testing.T) {
			text := RenderMessage(name, map[string]interface{}{
				"SiteName": "Example Site",
				"UserName": "Jane Doe",
				"Voters":   []string{},
			})

			if text == "" {
				t.Fatalf("%s rendered empty with no voters", name)
			}
			if strings.Contains(text, "•") {
				t.Errorf("%s emitted an empty bullet list:\n%s", name, text)
			}
		})
	}
}

func TestPollTemplatesHandleNilVoters(t *testing.T) {
	text := RenderMessage("poll_approved", map[string]interface{}{
		"SiteName": "Example Site",
		"UserName": "Jane Doe",
		"Voters":   []string(nil),
	})
	if text == "" {
		t.Errorf("poll_approved rendered empty for a nil voter list")
	}
}

// Telegram usernames are full of MarkdownV2 metacharacters. Without escaping, a name like
// @a_b.c makes the whole message fail to send.
func TestVoterNamesAreMarkdownEscaped(t *testing.T) {
	text := RenderMessage("poll_approved", map[string]interface{}{
		"SiteName": "Example",
		"UserName": "Jane",
		"Voters":   []string{"@a_b.c", "Dash-Name"},
	})

	if !strings.Contains(text, `@a\_b\.c`) {
		t.Errorf("underscore and dot were not escaped:\n%s", text)
	}
	if !strings.Contains(text, `Dash\-Name`) {
		t.Errorf("hyphen was not escaped:\n%s", text)
	}
}

func TestAutoEscapeDataEscapesStringSlices(t *testing.T) {
	escaped := autoEscapeData(map[string]interface{}{
		"Voters": []string{"a_b", "c.d"},
		"Plain":  "e-f",
		"Count":  3,
	})

	voters, ok := escaped["Voters"].([]string)
	if !ok {
		t.Fatalf("Voters became %T, want []string", escaped["Voters"])
	}
	if voters[0] != `a\_b` || voters[1] != `c\.d` {
		t.Errorf("string slice not escaped: %q", voters)
	}
	if escaped["Plain"] != `e\-f` {
		t.Errorf("plain string not escaped: %q", escaped["Plain"])
	}
	if escaped["Count"] != 3 {
		t.Errorf("non-string value was altered: %v", escaped["Count"])
	}
	// Escaping must not write through to the caller's slice.
	if voters[0] == "a_b" {
		t.Errorf("original slice was mutated")
	}
}

// Every template named in the schema must render, or a release could ship a message that
// silently produces nothing.
func TestAllTemplatesRenderFromTheirSchema(t *testing.T) {
	for name, schema := range templateSchema {
		t.Run(name, func(t *testing.T) {
			if got := RenderMessage(name, schema); got == "" {
				t.Errorf("%s rendered empty", name)
			}
		})
	}
}

func TestPollTemplatesAreLoadedFromDisk(t *testing.T) {
	dir := t.TempDir()
	custom := "*Custom approval*\n{{.SiteName}} by {{.UserName}}\n{{range .Voters}}{{.}} {{end}}"
	if err := os.WriteFile(filepath.Join(dir, "poll_approved.txt"), []byte(custom), 0o600); err != nil {
		t.Fatalf("writing template: %v", err)
	}

	InitTemplates(dir)
	t.Cleanup(func() { InitTemplates("testdata-does-not-exist") })

	text := RenderMessage("poll_approved", map[string]interface{}{
		"SiteName": "Example",
		"UserName": "Jane",
		"Voters":   []string{"Alice"},
	})
	if !strings.Contains(text, "Custom approval") {
		t.Errorf("the on-disk template was not used:\n%s", text)
	}
	if !strings.Contains(text, "Alice") {
		t.Errorf("custom template dropped the voter list:\n%s", text)
	}
}

// A customized template that references a field that does not exist must not take the
// message down with it.
func TestBadCustomTemplateFallsBackToTheDefault(t *testing.T) {
	dir := t.TempDir()
	broken := "Approved by {{.NoSuchField}}"
	if err := os.WriteFile(filepath.Join(dir, "poll_approved.txt"), []byte(broken), 0o600); err != nil {
		t.Fatalf("writing template: %v", err)
	}

	InitTemplates(dir)
	t.Cleanup(func() { InitTemplates("testdata-does-not-exist") })

	text := RenderMessage("poll_approved", map[string]interface{}{
		"SiteName": "Example",
		"UserName": "Jane",
		"Voters":   []string{"Alice"},
	})
	if text == "" {
		t.Fatalf("poll_approved rendered empty instead of falling back")
	}
	if strings.Contains(text, "NoSuchField") {
		t.Errorf("the broken template was used:\n%s", text)
	}
	if !strings.Contains(text, "Alice") {
		t.Errorf("fallback dropped the voter list:\n%s", text)
	}
}

func TestUnknownTemplateRendersEmpty(t *testing.T) {
	if got := RenderMessage("no_such_template", nil); got != "" {
		t.Errorf("unknown template rendered %q", got)
	}
}
