package cmd

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/version"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// robotDocsSchemaVersion versions the shape of 'caam robot docs' output.
const robotDocsSchemaVersion = 1

// RobotDocs is machine-readable CLI documentation for agents. Every field is
// generated from the live command tree and Go output types, so it cannot
// drift from the binary that prints it.
type RobotDocs struct {
	SchemaVersion int                       `json:"schema_version"`
	Version       string                    `json:"version"`
	Topic         string                    `json:"topic"`
	Commands      []RobotDocCommand         `json:"commands,omitempty"`
	ExitCodes     []RobotDocCode            `json:"exit_codes,omitempty"`
	ErrorCodes    []RobotDocCode            `json:"error_codes,omitempty"`
	Schemas       map[string]map[string]any `json:"schemas,omitempty"`
}

// RobotDocCommand describes one command.
type RobotDocCommand struct {
	Path        string            `json:"path"`
	Usage       string            `json:"usage"`
	Aliases     []string          `json:"aliases,omitempty"`
	Summary     string            `json:"summary"`
	Description string            `json:"description,omitempty"`
	Examples    []string          `json:"examples,omitempty"`
	Flags       []RobotDocFlag    `json:"flags,omitempty"`
	JSONOutput  bool              `json:"json_output"`
	Subcommands []string          `json:"subcommands,omitempty"`
	Runnable    bool              `json:"runnable"`
	Inherited   []RobotDocFlag    `json:"inherited_flags,omitempty"`
	Deprecated  string            `json:"deprecated,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// RobotDocFlag describes one flag.
type RobotDocFlag struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	Type      string `json:"type"`
	Default   string `json:"default,omitempty"`
	Usage     string `json:"usage"`
}

// RobotDocCode documents an exit code or a robot error code.
type RobotDocCode struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// robotExitCodes lists caam's process exit statuses.
var robotExitCodes = []RobotDocCode{
	{"0", "success"},
	{"1", "error; robot commands also print a JSON error envelope on stdout"},
	{"2", "partial success (robot commands that act on several items)"},
	{"N", "caam exec / caam run propagate the wrapped tool's own non-zero exit code"},
}

// robotErrorCodes documents every error.code a robot command can return.
// TestRobotErrorCodesAreDocumented keeps it in sync with robot.go.
var robotErrorCodes = []RobotDocCode{
	{"ACTIVATE_FAILED", "the profile could not be activated; live auth is unchanged or restored"},
	{"ACTIVE_VALIDATION_UNSUPPORTED", "provider probing is unavailable for saved profiles"},
	{"ALL_BLOCKED", "every profile is in cooldown or unhealthy"},
	{"BACKUP_FAILED", "the current login could not be saved as a profile"},
	{"COOLDOWN_FAILED", "the cooldown could not be recorded"},
	{"DB_ERROR", "the activity/cooldown database could not be opened or queried"},
	{"INVALID_ACTION", "unknown 'robot act' action"},
	{"INVALID_PROVIDER", "unknown provider name"},
	{"INVALID_TOPIC", "unknown 'robot docs' topic or schema name"},
	{"MISSING_PROFILE", "a profile name is required"},
	{"NO_AUTH", "no live login was found to back up"},
	{"NO_PROFILES", "no profiles exist for the provider"},
	{"PROFILE_NOT_FOUND", "the requested saved profile does not exist"},
	{"REFRESH_FAILED", "the provider refused or failed the token refresh"},
	{"REFRESH_SKIPPED", "the saved credential cannot be safely refreshed"},
	{"REFRESH_UNSUPPORTED", "renewal belongs to the native CLI or requires a new login"},
	{"SAVE_ERROR", "the configuration could not be saved"},
	{"UNCOOLDOWN_FAILED", "the cooldown could not be cleared"},
	{"UNKNOWN_KEY", "unknown configuration key"},
	{"VAULT_ERROR", "the profile vault could not be read"},
}

// robotDocSchemas maps schema names to the Go types commands encode.
var robotDocSchemas = map[string]reflect.Type{
	"robot_output":   reflect.TypeOf(RobotOutput{}),
	"robot_status":   reflect.TypeOf(RobotStatusData{}),
	"robot_next":     reflect.TypeOf(RobotNextData{}),
	"robot_act":      reflect.TypeOf(RobotActResult{}),
	"robot_limits":   reflect.TypeOf(RobotLimitsData{}),
	"robot_precheck": reflect.TypeOf(RobotPrecheckData{}),
	"robot_validate": reflect.TypeOf(RobotValidateData{}),
	"robot_paths":    reflect.TypeOf(RobotPathsData{}),
	"robot_history":  reflect.TypeOf(RobotHistoryData{}),
	"status":         reflect.TypeOf(statusOutput{}),
	"ls":             reflect.TypeOf(lsOutput{}),
	"activate":       reflect.TypeOf(activateOutput{}),
}

var robotDocsTopics = []string{"all", "commands", "exit-codes", "errors", "schemas"}

var robotDocsCmd = &cobra.Command{
	Use:   "docs [topic]",
	Short: "Machine-readable CLI documentation (commands, flags, codes, schemas)",
	Long: `Emits JSON documentation generated from this binary: every command with its
flags and examples, exit codes, robot error codes, and JSON Schemas for the
outputs of robot commands, 'caam status --json', 'caam ls --json', and
'caam activate --json'.

Topics: all (default), commands, exit-codes, errors, schemas.
'caam robot docs schemas <name>' prints one schema.

Examples:
  caam robot docs
  caam robot docs commands
  caam robot docs schemas ls`,
	Args:      cobra.RangeArgs(0, 2),
	ValidArgs: robotDocsTopics,
	RunE:      runRobotDocs,
}

func init() {
	robotCmd.AddCommand(robotDocsCmd)
}

func runRobotDocs(cmd *cobra.Command, args []string) error {
	topic := "all"
	if len(args) > 0 {
		topic = strings.ToLower(args[0])
	}
	valid := false
	for _, t := range robotDocsTopics {
		valid = valid || t == topic
	}
	if !valid {
		return robotError(cmd, "docs", "INVALID_TOPIC",
			fmt.Sprintf("unknown docs topic %q", topic), "",
			[]string{"caam robot docs " + strings.Join(robotDocsTopics[1:], "|")})
	}
	if len(args) == 2 && topic != "schemas" {
		return robotError(cmd, "docs", "INVALID_TOPIC", "only the schemas topic takes a name", "", nil)
	}

	docs := RobotDocs{SchemaVersion: robotDocsSchemaVersion, Version: version.Version, Topic: topic}
	if topic == "all" || topic == "commands" {
		docs.Commands = robotDocCommands(cmd.Root())
	}
	if topic == "all" || topic == "exit-codes" {
		docs.ExitCodes = robotExitCodes
	}
	if topic == "all" || topic == "errors" {
		docs.ErrorCodes = robotErrorCodes
	}
	if topic == "all" || topic == "schemas" {
		docs.Schemas = make(map[string]map[string]any)
		for name, t := range robotDocSchemas {
			if len(args) == 2 && name != args[1] {
				continue
			}
			docs.Schemas[name] = jsonSchemaDocument(name, t)
		}
		if len(args) == 2 && len(docs.Schemas) == 0 {
			return robotError(cmd, "docs", "INVALID_TOPIC",
				fmt.Sprintf("unknown schema %q", args[1]), "",
				[]string{"caam robot docs schemas"})
		}
	}

	return robotOutput(cmd, RobotOutput{
		Success:   true,
		Command:   "docs",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Data:      docs,
	})
}

// robotDocCommands walks the command tree in path order.
func robotDocCommands(root *cobra.Command) []RobotDocCommand {
	var out []RobotDocCommand
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			return
		}
		out = append(out, robotDocCommand(c))
		children := c.Commands()
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			walk(child)
		}
	}
	walk(root)
	return out
}

func robotDocCommand(c *cobra.Command) RobotDocCommand {
	doc := RobotDocCommand{
		Path:       c.CommandPath(),
		Usage:      c.UseLine(),
		Aliases:    c.Aliases,
		Summary:    c.Short,
		Runnable:   c.Runnable(),
		Deprecated: c.Deprecated,
	}
	doc.Description, doc.Examples = splitExamples(c.Long)
	if c.Example != "" {
		doc.Examples = append(doc.Examples, exampleLines(c.Example)...)
	}
	c.LocalFlags().VisitAll(func(f *pflag.Flag) {
		if !f.Hidden {
			doc.Flags = append(doc.Flags, robotDocFlag(f))
		}
	})
	if c.HasParent() {
		c.InheritedFlags().VisitAll(func(f *pflag.Flag) {
			if !f.Hidden {
				doc.Inherited = append(doc.Inherited, robotDocFlag(f))
			}
		})
	}
	for _, child := range c.Commands() {
		if !child.Hidden && child.Name() != "help" {
			doc.Subcommands = append(doc.Subcommands, child.Name())
		}
	}
	sort.Strings(doc.Subcommands)
	// Robot commands always print JSON; others do with --json or --format json.
	doc.JSONOutput = strings.HasPrefix(c.CommandPath(), c.Root().Name()+" robot") ||
		c.Flags().Lookup("json") != nil ||
		(c.Flags().Lookup("format") != nil && strings.Contains(c.Flags().Lookup("format").Usage, "json"))
	if len(c.Annotations) > 0 {
		doc.Annotations = c.Annotations
	}
	return doc
}

func robotDocFlag(f *pflag.Flag) RobotDocFlag {
	return RobotDocFlag{
		Name:      f.Name,
		Shorthand: f.Shorthand,
		Type:      f.Value.Type(),
		Default:   f.DefValue,
		Usage:     f.Usage,
	}
}

// splitExamples separates a Long help text from its "Examples:" section and
// returns the example command lines.
func splitExamples(long string) (string, []string) {
	idx := strings.Index(long, "Examples:")
	if idx < 0 {
		return strings.TrimSpace(long), nil
	}
	return strings.TrimSpace(long[:idx]), exampleLines(long[idx+len("Examples:"):])
}

// exampleLines returns the command lines of an example block, without
// trailing comments.
func exampleLines(block string) []string {
	var out []string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "caam ") && !strings.HasPrefix(line, "eval ") {
			continue
		}
		if i := strings.Index(line, "  #"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		out = append(out, line)
	}
	return out
}

// jsonSchemaDocument renders a JSON Schema (draft 2020-12) for t.
func jsonSchemaDocument(name string, t reflect.Type) map[string]any {
	defs := map[string]any{}
	root := jsonSchemaFor(t, defs, true)
	root["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	root["title"] = name
	if len(defs) > 0 {
		root["$defs"] = defs
	}
	return root
}

var timeType = reflect.TypeOf(time.Time{})

// jsonSchemaFor describes t as encoding/json would encode it. Named structs
// other than the root go to defs and are referenced, which also terminates
// recursive types.
func jsonSchemaFor(t reflect.Type, defs map[string]any, root bool) map[string]any {
	switch {
	case t == timeType:
		return map[string]any{"type": "string", "format": "date-time"}
	case t.Kind() == reflect.Pointer:
		return jsonSchemaFor(t.Elem(), defs, root)
	}

	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "contentEncoding": "base64"}
		}
		return map[string]any{"type": "array", "items": jsonSchemaFor(t.Elem(), defs, false)}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": jsonSchemaFor(t.Elem(), defs, false)}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Struct:
		if !root && t.Name() != "" {
			name := t.Name()
			if _, done := defs[name]; !done {
				defs[name] = map[string]any{} // reserve before recursing
				defs[name] = jsonStructSchema(t, defs)
			}
			return map[string]any{"$ref": "#/$defs/" + name}
		}
		return jsonStructSchema(t, defs)
	default:
		return map[string]any{}
	}
}

func jsonStructSchema(t reflect.Type, defs map[string]any) map[string]any {
	props := map[string]any{}
	var required []string
	var addFields func(t reflect.Type)
	addFields = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, opts, _ := strings.Cut(tag, ",")
			if f.Anonymous && name == "" {
				ft := f.Type
				if ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				if ft.Kind() == reflect.Struct {
					addFields(ft)
					continue
				}
			}
			if !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			props[name] = jsonSchemaFor(f.Type, defs, false)
			if !strings.Contains(opts, "omitempty") && !strings.Contains(opts, "omitzero") {
				required = append(required, name)
			}
		}
	}
	addFields(t)
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = required
	}
	return schema
}
