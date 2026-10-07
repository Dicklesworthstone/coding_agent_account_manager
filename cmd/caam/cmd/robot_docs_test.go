package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func runRobotDocsJSON(t *testing.T, args ...string) (RobotOutput, map[string]any) {
	t.Helper()
	var out bytes.Buffer
	c := &cobra.Command{}
	c.SetOut(&out)
	// Walk the real tree from the real root.
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := runRobotDocs(c, args)
	var envelope RobotOutput
	if jerr := json.Unmarshal(out.Bytes(), &envelope); jerr != nil {
		t.Fatalf("output is not JSON (%v): %s", jerr, out.String())
	}
	if err != nil && envelope.Success {
		t.Fatalf("error %v with success envelope", err)
	}
	var raw map[string]any
	json.Unmarshal(out.Bytes(), &raw)
	data, _ := raw["data"].(map[string]any)
	return envelope, data
}

func TestRobotDocsCommandsCoverTree(t *testing.T) {
	envelope, data := runRobotDocsJSON(t, "commands")
	if !envelope.Success {
		t.Fatalf("docs failed: %+v", envelope.Error)
	}
	if data["schema_version"].(float64) != robotDocsSchemaVersion || data["version"] == "" {
		t.Fatalf("metadata = %v", data)
	}
	commands, _ := data["commands"].([]any)
	byPath := map[string]map[string]any{}
	for _, c := range commands {
		m := c.(map[string]any)
		byPath[m["path"].(string)] = m
	}
	for _, path := range []string{"caam backup", "caam activate", "caam status", "caam ls", "caam robot status", "caam robot docs", "caam auth-coordinator status", "caam notify test"} {
		if byPath[path] == nil {
			t.Errorf("command %q missing from docs", path)
		}
	}
	ls := byPath["caam ls"]
	if ls["json_output"] != true {
		t.Errorf("ls should be marked json_output: %v", ls)
	}
	var flagNames []string
	for _, f := range ls["flags"].([]any) {
		flagNames = append(flagNames, f.(map[string]any)["name"].(string))
	}
	if !strings.Contains(strings.Join(flagNames, ","), "tag") {
		t.Errorf("ls flags = %v", flagNames)
	}
	if ex, _ := ls["examples"].([]any); len(ex) == 0 || !strings.HasPrefix(ex[0].(string), "caam ls") {
		t.Errorf("ls examples = %v", ls["examples"])
	}
	if byPath["caam robot status"]["json_output"] != true {
		t.Error("robot commands are JSON output")
	}
}

func TestRobotDocsSchemas(t *testing.T) {
	envelope, data := runRobotDocsJSON(t, "schemas", "ls")
	if !envelope.Success {
		t.Fatalf("docs failed: %+v", envelope.Error)
	}
	schemas := data["schemas"].(map[string]any)
	if len(schemas) != 1 {
		t.Fatalf("expected only the ls schema, got %d", len(schemas))
	}
	ls := schemas["ls"].(map[string]any)
	if ls["$schema"] == nil || ls["type"] != "object" {
		t.Fatalf("ls schema = %v", ls)
	}
	props := ls["properties"].(map[string]any)
	profiles := props["profiles"].(map[string]any)
	if profiles["type"] != "array" {
		t.Fatalf("profiles = %v", profiles)
	}
	ref := profiles["items"].(map[string]any)["$ref"].(string)
	def := ls["$defs"].(map[string]any)[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
	defProps := def["properties"].(map[string]any)
	for _, field := range []string{"name", "tool", "active", "health", "tags", "description"} {
		if defProps[field] == nil {
			t.Errorf("profile schema missing %q", field)
		}
	}
	required := strings.Join(toStrings(def["required"]), ",")
	if !strings.Contains(required, "name") || strings.Contains(required, "tags") {
		t.Errorf("required = %s (omitempty fields must be optional)", required)
	}

	_, all := runRobotDocsJSON(t, "schemas")
	for _, name := range []string{"robot_output", "robot_status", "status", "ls", "activate"} {
		if all["schemas"].(map[string]any)[name] == nil {
			t.Errorf("schema %q missing", name)
		}
	}
}

func TestRobotDocsRejectsUnknownTopic(t *testing.T) {
	envelope, _ := runRobotDocsJSON(t, "nonsense")
	if envelope.Success || envelope.Error == nil || envelope.Error.Code != "INVALID_TOPIC" {
		t.Fatalf("envelope = %+v", envelope)
	}
	envelope, _ = runRobotDocsJSON(t, "schemas", "nope")
	if envelope.Success || envelope.Error.Code != "INVALID_TOPIC" {
		t.Fatalf("unknown schema envelope = %+v", envelope)
	}
}

// TestRobotErrorCodesAreDocumented fails when a robot command starts
// returning an error code the docs catalog does not describe.
func TestRobotErrorCodesAreDocumented(t *testing.T) {
	documented := map[string]bool{}
	for _, c := range robotErrorCodes {
		documented[c.Code] = true
	}
	literal := regexp.MustCompile(`robotError\(cmd,\s*[^,]+,\s*"([A-Z][A-Z_]+)"`)
	assigned := regexp.MustCompile(`code\s*:?=\s*"([A-Z][A-Z_]+)"`)
	for _, file := range []string{"robot.go", "robot_docs.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, re := range []*regexp.Regexp{literal, assigned} {
			for _, m := range re.FindAllStringSubmatch(string(src), -1) {
				if !documented[m[1]] {
					t.Errorf("%s emits undocumented robot error code %s; add it to robotErrorCodes", file, m[1])
				}
			}
		}
	}
}

func toStrings(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}
