package telegram

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/template"
)

type ChangeEntry struct {
	Key   string
	Value string
}

var (
	msgTemplates map[string]*template.Template
	tmplMu       sync.RWMutex
)

var protectedRegion = regexp.MustCompile(
	`(\{\{-?\s*.*?\s*-?\}\}` +
		`|\*[^*\n]+\*` +
		"|`[^`\n]+`" +
		`)`,
)

var templateSchema = map[string]map[string]interface{}{
	"new_request_create": {
		"UserName": "test",
		"Slug":     "test",
		"SiteName": "test",
		"URL":      "test",
		"Date":     "test",
	},
	"new_request_update": {
		"UserName": "test",
		"SiteName": "test",
		"SiteSlug": "test",
		"Changes":  []ChangeEntry{{Key: "k", Value: "v"}},
		"Date":     "test",
	},
	"approved_create": {
		"SiteName": "test",
	},
	"approved_update": {
		"Changes": []ChangeEntry{{Key: "k", Value: "v"}},
	},
	"declined_create": {
		"SiteName": "test",
	},
	"declined_update": {
		"SiteName": "test",
	},
	"admin_approved_create": {
		"AdminName": "test",
		"UserName":  "test",
		"SiteName":  "test",
	},
	"admin_approved_update": {
		"AdminName": "test",
		"UserName":  "test",
		"SiteName":  "test",
		"Changes":   []ChangeEntry{{Key: "k", Value: "v"}},
	},
	"admin_declined_create": {
		"AdminName": "test",
		"UserName":  "test",
		"SiteName":  "test",
	},
	"admin_declined_update": {
		"AdminName": "test",
		"UserName":  "test",
		"SiteName":  "test",
	},
	"site_online": {
		"SiteName": "test",
	},
	"site_offline": {
		"SiteName":      "test",
		"DownThreshold": 3,
	},
}

var staticEscaper = regexp.MustCompile(`([_\[\]()~>#+\-=|{}.!\\])`)

func prepareTemplate(raw string) string {
	locs := protectedRegion.FindAllStringIndex(raw, -1)
	if len(locs) == 0 {
		return staticEscaper.ReplaceAllString(raw, `\$1`)
	}

	var b strings.Builder
	last := 0
	for _, loc := range locs {
		if loc[0] > last {
			b.WriteString(staticEscaper.ReplaceAllString(
				raw[last:loc[0]], `\$1`,
			))
		}
		b.WriteString(raw[loc[0]:loc[1]])
		last = loc[1]
	}
	if last < len(raw) {
		b.WriteString(staticEscaper.ReplaceAllString(
			raw[last:], `\$1`,
		))
	}
	return b.String()
}

//nolint:lll // template strings are naturally long
var defaults = map[string]string{
	"new_request_create":    "*New Site Submission Request*\n\n*User:* {{.UserName}}\n*Slug:* `{{.Slug}}`\n*Site Name:* {{.SiteName}}\n*URL:* {{.URL}}\n\n*Submitted:* {{.Date}}",
	"new_request_update":    "*Site Update Request*\n\n*User:* {{.UserName}}\n*Site:* {{.SiteName}} (`{{.SiteSlug}}`)\n\n*Changes:*\n{{- range .Changes}}\n  • *{{.Key}}:* {{.Value}}\n{{- end}}\n\n*Submitted:* {{.Date}}",
	"approved_create":       "*Request Approved*\n\nYour site submission has been approved!\n\n*Site:* {{.SiteName}}\n\nYour site is now part of the webring.",
	"approved_update":       "*Update Approved*\n\nYour site update request has been approved and the changes have been applied.\n{{- if .Changes}}\n\n*Applied changes:*\n{{- range .Changes}}\n  • *{{.Key}}:* {{.Value}}\n{{- end}}\n{{- end}}",
	"declined_create":       "*Request Declined*\n\nYour site submission request for *{{.SiteName}}* has been declined by an administrator.\n\nIf you have questions, please contact the webring administrator.",
	"declined_update":       "*Update Request Declined*\n\nYour update request for *{{.SiteName}}* has been declined by an administrator.\n\nIf you have questions, please contact the webring administrator.",
	"admin_approved_create": "*Request Approved*\n\n*Admin:* {{.AdminName}}\n*Action:* Approved site creation\n*User:* {{.UserName}}\n*Site:* {{.SiteName}}",
	"admin_approved_update": "*Update Approved*\n\n*Admin:* {{.AdminName}}\n*Action:* Approved site update\n*User:* {{.UserName}}\n*Site:* {{.SiteName}}\n{{- if .Changes}}\n\n*Changes:*\n{{- range .Changes}}\n  • *{{.Key}}:* {{.Value}}\n{{- end}}\n{{- end}}",
	"admin_declined_create": "*Request Declined*\n\n*Admin:* {{.AdminName}}\n*Action:* Declined site creation\n*User:* {{.UserName}}\n*Site:* {{.SiteName}}",
	"admin_declined_update": "*Update Declined*\n\n*Admin:* {{.AdminName}}\n*Action:* Declined site update\n*User:* {{.UserName}}\n*Site:* {{.SiteName}}",
	"site_online":           "*Site Status: Online*\n\nYour site *{{.SiteName}}* is now responding and back online.",
	"site_offline":          "*Site Status: Offline*\n\nYour site *{{.SiteName}}* is currently not responding after {{.DownThreshold}} consecutive checks. Please check your server.",
}

func mustParseFallback(name, fallback string) *template.Template {
	tmpl, err := template.New(name).
		Option("missingkey=error").
		Parse(prepareTemplate(fallback))
	if err != nil {
		log.Fatalf(
			"FATAL: built-in template %s has invalid syntax: %v",
			name, err,
		)
	}
	return tmpl
}

func InitTemplates(dir string) {
	tmplMu.Lock()

	msgTemplates = make(map[string]*template.Template, len(defaults))

	for name, fallback := range defaults {
		raw := fallback
		fromFile := false

		path := filepath.Join(dir, name+".txt")
		cleanPath := filepath.Clean(path)
		if data, err := os.ReadFile(cleanPath); err == nil {
			raw = string(data)
			fromFile = true
			log.Printf("Loaded message template: %s", cleanPath)
		}

		prepared := prepareTemplate(raw)

		tmpl, err := template.New(name).
			Option("missingkey=error").
			Parse(prepared)
		if err != nil {
			if fromFile {
				log.Printf(
					"ERROR: template %s has invalid syntax: %v — falling back to default",
					name, err,
				)
			} else {
				tmplMu.Unlock()
				log.Fatalf(
					"FATAL: built-in template %s has invalid syntax: %v",
					name, err,
				)
			}
			tmpl = mustParseFallback(name, fallback)
		}

		if schema, ok := templateSchema[name]; ok {
			var buf bytes.Buffer
			if execErr := tmpl.Execute(&buf, schema); execErr != nil {
				if fromFile {
					log.Printf(
						"ERROR: template %s references invalid variables: %v — falling back to default",
						name, execErr,
					)
					tmpl = mustParseFallback(name, fallback)
				} else {
					tmplMu.Unlock()
					log.Fatalf(
						"FATAL: built-in template %s references invalid variables: %v",
						name, execErr,
					)
				}
			}
		}

		msgTemplates[name] = tmpl
	}

	tmplMu.Unlock()

	log.Printf(
		"Initialized and validated %d message templates",
		len(msgTemplates),
	)
}

func autoEscapeData(
	data map[string]interface{},
) map[string]interface{} {
	result := make(map[string]interface{}, len(data))
	for k, v := range data {
		switch val := v.(type) {
		case string:
			result[k] = EscapeMarkdownV2(val)
		case []ChangeEntry:
			escaped := make([]ChangeEntry, len(val))
			for i, e := range val {
				escaped[i] = ChangeEntry{
					Key:   EscapeMarkdownV2(e.Key),
					Value: EscapeMarkdownV2(e.Value),
				}
			}
			result[k] = escaped
		default:
			result[k] = v
		}
	}
	return result
}

func RenderMessage(
	name string,
	data map[string]interface{},
) string {
	tmplMu.RLock()
	tmpl, ok := msgTemplates[name]
	tmplMu.RUnlock()

	if !ok {
		log.Printf("Template %s not found", name)
		return ""
	}

	escaped := autoEscapeData(data)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, escaped); err != nil {
		log.Printf("Error rendering template %s: %v", name, err)
		return ""
	}

	return buf.String()
}

func BuildChanges(
	fields map[string]interface{},
) []ChangeEntry {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	entries := make([]ChangeEntry, 0, len(fields))
	for _, k := range keys {
		entries = append(entries, ChangeEntry{
			Key:   k,
			Value: fmt.Sprintf("%v", fields[k]),
		})
	}
	return entries
}
