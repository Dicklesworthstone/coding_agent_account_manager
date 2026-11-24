package cmd

import (
	"regexp"
	"strings"
	"testing"
)

// =============================================================================
// Pane Discovery Fixtures - Real-world Claude Code output variants
// =============================================================================

// PaneFixture represents a test fixture for pane discovery.
type PaneFixture struct {
	Name           string
	Content        string
	Tool           string
	Override       *regexp.Regexp
	ShouldMatch    bool
	ExpectedReason string
}

// =============================================================================
// Claude Rate Limit Fixtures
// =============================================================================

var claudeRateLimitFixtures = []PaneFixture{
	{
		Name: "claude_rate_limit_basic",
		Content: `Claude Code session
═══════════════════════════════════════════
You've hit your limit · resets 3:00 PM

Rate limit reached. Please wait or upgrade to continue.`,
		Tool:           "claude",
		ShouldMatch:    true,
		ExpectedReason: "rate_limit",
	},
	{
		Name: "claude_rate_limit_with_ansi",
		Content: "\x1b[31mYou've hit your limit\x1b[0m · resets 3:00 PM\n" +
			"\x1b[90mRate limit reached\x1b[0m",
		Tool:           "claude",
		ShouldMatch:    true,
		ExpectedReason: "rate_limit",
	},
	{
		Name: "claude_usage_limit_variant",
		Content: `Claude Code
───────────────────────────────────────────
⚠️ Usage limit exceeded

You have reached your daily usage limit.
Please try again later or upgrade your plan.`,
		Tool:           "claude",
		ShouldMatch:    true,
		ExpectedReason: "rate_limit",
	},
	{
		Name: "claude_too_many_requests",
		Content: `error: too many requests
Please wait a moment before trying again.
Claude Code session closed.`,
		Tool:           "claude",
		ShouldMatch:    true,
		ExpectedReason: "rate_limit",
	},
	{
		Name: "claude_429_error",
		Content: `HTTP 429: Too Many Requests
Claude API rate limit exceeded. Retry after 60s.`,
		Tool:           "claude",
		ShouldMatch:    true,
		ExpectedReason: "rate_limit",
	},
}

// =============================================================================
// Claude Tool Marker Fixtures
// =============================================================================

var claudeToolMarkerFixtures = []PaneFixture{
	{
		Name: "claude_session_banner",
		Content: `Claude Code session
═══════════════════════════════════════════
> How can I help you today?

I'm ready to assist with your coding tasks.`,
		Tool:           "claude",
		ShouldMatch:    true,
		ExpectedReason: "tool_marker",
	},
	{
		Name: "claude_working_session",
		Content: `◐ Working...

Claude is analyzing your request. This may take a moment.

> What would you like me to help you with?`,
		Tool:           "claude",
		ShouldMatch:    true,
		ExpectedReason: "tool_marker",
	},
	{
		Name: "claude_anthropic_reference",
		Content: `Powered by Anthropic's Claude AI
Version 3.5 Sonnet

Enter /help for available commands.`,
		Tool:           "claude",
		ShouldMatch:    true,
		ExpectedReason: "tool_marker",
	},
	{
		Name: "claude_lowercase",
		Content: `Starting claude code...
Connecting to Anthropic API...
Session ready.`,
		Tool:           "claude",
		ShouldMatch:    true,
		ExpectedReason: "tool_marker",
	},
	{
		Name: "claude_with_box_drawing",
		Content: "╔════════════════════════════════════════╗\n" +
			"║          Claude Code Session           ║\n" +
			"╚════════════════════════════════════════╝\n" +
			"Ready for input.",
		Tool:           "claude",
		ShouldMatch:    true,
		ExpectedReason: "tool_marker",
	},
	{
		Name: "claude_oauth_prompt",
		Content: `Claude Code requires authentication.
Browser didn't open? Use the url below:
https://claude.ai/oauth/authorize?code=true&client_id=abc

Paste code here if prompted >`,
		Tool:           "claude",
		ShouldMatch:    true,
		ExpectedReason: "tool_marker",
	},
}

// =============================================================================
// Codex Tool Marker Fixtures
// =============================================================================

var codexToolMarkerFixtures = []PaneFixture{
	{
		Name: "codex_session_banner",
		Content: `Codex CLI v2.0.0
Powered by OpenAI

Type your request or /help for commands.`,
		Tool:           "codex",
		ShouldMatch:    true,
		ExpectedReason: "tool_marker",
	},
	{
		Name: "codex_openai_reference",
		Content: `OpenAI Codex
Connected to GPT-4 model.
Ready for code generation.`,
		Tool:           "codex",
		ShouldMatch:    true,
		ExpectedReason: "tool_marker",
	},
	{
		Name: "codex_rate_limited",
		Content: `OpenAI API rate limit reached.
Too many requests in the last hour.
Please wait before trying again.`,
		Tool:           "codex",
		ShouldMatch:    true,
		ExpectedReason: "rate_limit",
	},
}

// =============================================================================
// Gemini Tool Marker Fixtures
// =============================================================================

var geminiToolMarkerFixtures = []PaneFixture{
	{
		Name: "gemini_session_banner",
		Content: `Gemini CLI v1.0.0
Google AI Platform

Ready for conversation.`,
		Tool:           "gemini",
		ShouldMatch:    true,
		ExpectedReason: "tool_marker",
	},
	{
		Name: "gemini_google_ai_reference",
		Content: `Powered by Google AI
Model: Gemini Pro 1.5
Enter your query:`,
		Tool:           "gemini",
		ShouldMatch:    true,
		ExpectedReason: "tool_marker",
	},
	{
		Name: "gemini_rate_limited",
		Content: `RESOURCE_EXHAUSTED: Quota exceeded
You have exceeded your API quota for the day.
Please try again tomorrow or upgrade your plan.`,
		Tool:           "gemini",
		ShouldMatch:    true,
		ExpectedReason: "rate_limit",
	},
}

// =============================================================================
// Non-Matching Fixtures (Should NOT Match)
// =============================================================================

var nonMatchingFixtures = []PaneFixture{
	{
		Name: "bash_prompt_basic",
		Content: `Last login: Mon Jan 20 10:00:00 on ttys000
user@hostname ~ % ls -la
total 48
drwxr-xr-x  12 user  staff   384 Jan 20 10:00 .
drwxr-xr-x   6 user  staff   192 Jan 15 14:30 ..`,
		Tool:           "claude",
		ShouldMatch:    false,
		ExpectedReason: "no_match",
	},
	{
		Name: "zsh_prompt",
		Content: `➜  ~/projects git:(main) ✗ git status
On branch main
Your branch is up to date with 'origin/main'.

nothing to commit, working tree clean`,
		Tool:           "claude",
		ShouldMatch:    false,
		ExpectedReason: "no_match",
	},
	{
		Name: "vim_editor",
		Content: `  1 package main
  2
  3 import "fmt"
  4
  5 func main() {
  6     fmt.Println("Hello, World!")
  7 }
~
~
-- INSERT -- 1,1 All`,
		Tool:           "claude",
		ShouldMatch:    false,
		ExpectedReason: "no_match",
	},
	{
		Name: "htop_output",
		Content: `  PID USER      PRI  NI  VIRT   RES   SHR S CPU% MEM%   TIME+  Command
 1234 user       20   0  512M  128M  64M S  5.0  1.5   0:01.23 /usr/bin/code
 5678 user       20   0  256M   64M  32M S  2.0  0.8   0:00.45 node server.js`,
		Tool:           "claude",
		ShouldMatch:    false,
		ExpectedReason: "no_match",
	},
	{
		Name: "npm_output",
		Content: `npm WARN deprecated request@2.88.2: request has been deprecated
npm WARN deprecated har-validator@5.1.5: this library is no longer supported

added 245 packages, and audited 246 packages in 3s`,
		Tool:           "claude",
		ShouldMatch:    false,
		ExpectedReason: "no_match",
	},
	{
		Name: "docker_output",
		Content: `CONTAINER ID   IMAGE          COMMAND       CREATED        STATUS        PORTS     NAMES
a1b2c3d4e5f6   postgres:15    "docker..."   2 hours ago    Up 2 hours    5432/tcp  mydb
f6e5d4c3b2a1   redis:latest   "docker..."   3 hours ago    Up 3 hours    6379/tcp  cache`,
		Tool:           "claude",
		ShouldMatch:    false,
		ExpectedReason: "no_match",
	},
	{
		Name: "python_repl",
		Content: `Python 3.11.5 (main, Aug 24 2023, 15:09:45) [Clang 14.0.3]
Type "help", "copyright", "credits" or "license" for more information.
>>> import numpy as np
>>> np.array([1, 2, 3])
array([1, 2, 3])
>>>`,
		Tool:           "claude",
		ShouldMatch:    false,
		ExpectedReason: "no_match",
	},
	{
		Name: "go_test_output",
		Content: `=== RUN   TestHelloWorld
--- PASS: TestHelloWorld (0.00s)
=== RUN   TestCalculator
--- PASS: TestCalculator (0.00s)
PASS
ok      example.com/myapp    0.003s`,
		Tool:           "claude",
		ShouldMatch:    false,
		ExpectedReason: "no_match",
	},
	{
		Name: "man_page",
		Content: `LS(1)                     User Commands                    LS(1)

NAME
       ls - list directory contents

SYNOPSIS
       ls [OPTION]... [FILE]...

DESCRIPTION
       List information about the FILEs.`,
		Tool:           "claude",
		ShouldMatch:    false,
		ExpectedReason: "no_match",
	},
	{
		Name: "ssh_session",
		Content: `Welcome to Ubuntu 22.04.3 LTS (GNU/Linux 5.15.0-91-generic x86_64)

 * Documentation:  https://help.ubuntu.com
 * Management:     https://landscape.canonical.com

Last login: Mon Jan 20 09:00:00 2026 from 192.168.1.100
ubuntu@server:~$`,
		Tool:           "claude",
		ShouldMatch:    false,
		ExpectedReason: "no_match",
	},
	{
		Name: "tmux_status",
		Content: `[0] 0:bash* 1:vim- 2:htop
                                        "hostname" 10:30 20-Jan-26`,
		Tool:           "claude",
		ShouldMatch:    false,
		ExpectedReason: "no_match",
	},
}

// =============================================================================
// ANSI and Box Drawing Fixtures
// =============================================================================

var ansiAndBoxDrawingFixtures = []PaneFixture{
	{
		Name: "heavy_ansi_escapes",
		Content: "\x1b[38;2;255;100;100mClaude\x1b[0m \x1b[38;5;208mCode\x1b[0m \x1b[1;4;31msession\x1b[0m\n" +
			"\x1b[?25l\x1b[2J\x1b[H\x1b[?25h" + // cursor hide/show, clear screen, home
			"Ready for input.",
		Tool:           "claude",
		ShouldMatch:    true,
		ExpectedReason: "tool_marker",
	},
	{
		Name: "osc_escape_sequences",
		Content: "\x1b]0;Claude Code - Terminal\x07" + // OSC window title
			"\x1b]52;c;dGVzdA==\x07" + // OSC clipboard
			"Claude Code session ready.",
		Tool:           "claude",
		ShouldMatch:    true,
		ExpectedReason: "tool_marker",
	},
	{
		Name: "mixed_box_drawing",
		Content: "┌─────────────────────────────┐\n" +
			"│   Claude Code Session       │\n" +
			"├─────────────────────────────┤\n" +
			"│ ░░░░░░░░░░░░░░░░░░░░░░░░░░  │\n" +
			"│ ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓  │\n" +
			"└─────────────────────────────┘\n" +
			"Type /help for commands.",
		Tool:           "claude",
		ShouldMatch:    true,
		ExpectedReason: "tool_marker",
	},
	{
		Name: "unicode_box_drawing",
		Content: "╭──────────────────────────────╮\n" +
			"│  ✨ Claude Code ✨           │\n" +
			"╰──────────────────────────────╯\n" +
			"Welcome! How can I help?",
		Tool:           "claude",
		ShouldMatch:    true,
		ExpectedReason: "tool_marker",
	},
	{
		Name: "rate_limit_with_emoji_and_ansi",
		Content: "\x1b[31m⚠️  You've hit your limit\x1b[0m\n" +
			"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n" +
			"Please wait or upgrade your plan.",
		Tool:           "claude",
		ShouldMatch:    true,
		ExpectedReason: "rate_limit",
	},
}

// =============================================================================
// Override (--match) Fixtures
// =============================================================================

var overrideFixtures = []PaneFixture{
	{
		Name:           "override_matches_custom_pattern",
		Content:        "my-custom-session-marker-12345",
		Tool:           "claude",
		Override:       regexp.MustCompile(`custom-session-marker-\d+`),
		ShouldMatch:    true,
		ExpectedReason: "override",
	},
	{
		Name:           "override_no_match",
		Content:        "some generic bash prompt content",
		Tool:           "claude",
		Override:       regexp.MustCompile(`custom-session-marker-\d+`),
		ShouldMatch:    false,
		ExpectedReason: "override_no_match",
	},
	{
		Name:           "override_case_insensitive",
		Content:        "MY-PROJECT-SESSION active",
		Tool:           "claude",
		Override:       regexp.MustCompile(`(?i)my-project-session`),
		ShouldMatch:    true,
		ExpectedReason: "override",
	},
	{
		Name:           "override_multiword_whitespace",
		Content:        "session   marker   active",
		Tool:           "claude",
		Override:       regexp.MustCompile(`session\s+marker\s+active`),
		ShouldMatch:    true,
		ExpectedReason: "override",
	},
	{
		Name:           "override_ignores_tool_markers",
		Content:        "Claude Code session", // Would normally match tool_marker
		Tool:           "claude",
		Override:       regexp.MustCompile(`^never-matches$`),
		ShouldMatch:    false,
		ExpectedReason: "override_no_match", // Override takes precedence
	},
}

// =============================================================================
// Cross-Tool No-Match Fixtures (Ensure correct tool matching)
// =============================================================================

var crossToolFixtures = []PaneFixture{
	{
		Name:           "claude_content_codex_tool",
		Content:        "Claude Code session ready",
		Tool:           "codex", // Looking for Codex, not Claude
		ShouldMatch:    false,
		ExpectedReason: "no_match",
	},
	{
		Name:           "codex_content_claude_tool",
		Content:        "Codex CLI v2.0.0 - OpenAI",
		Tool:           "claude", // Looking for Claude, not Codex
		ShouldMatch:    false,
		ExpectedReason: "no_match",
	},
	{
		Name:           "gemini_content_claude_tool",
		Content:        "Gemini Pro - Google AI Platform",
		Tool:           "claude", // Looking for Claude, not Gemini
		ShouldMatch:    false,
		ExpectedReason: "no_match",
	},
	{
		Name:           "rate_limit_matches_any_tool",
		Content:        "You've hit your limit - please wait",
		Tool:           "codex", // Rate limit matches regardless of tool
		ShouldMatch:    true,
		ExpectedReason: "rate_limit",
	},
}

// =============================================================================
// Test Functions
// =============================================================================

func TestPaneDiscoveryFixtures(t *testing.T) {
	allFixtures := []struct {
		category string
		fixtures []PaneFixture
	}{
		{"claude_rate_limit", claudeRateLimitFixtures},
		{"claude_tool_marker", claudeToolMarkerFixtures},
		{"codex_tool_marker", codexToolMarkerFixtures},
		{"gemini_tool_marker", geminiToolMarkerFixtures},
		{"non_matching", nonMatchingFixtures},
		{"ansi_box_drawing", ansiAndBoxDrawingFixtures},
		{"override", overrideFixtures},
		{"cross_tool", crossToolFixtures},
	}

	for _, category := range allFixtures {
		t.Run(category.category, func(t *testing.T) {
			for _, fixture := range category.fixtures {
				t.Run(fixture.Name, func(t *testing.T) {
					result := matchWeztermPane(fixture.Tool, fixture.Content, fixture.Override)

					// Log structured output for debugging
					t.Logf("fixture=%s tool=%s matched=%v reason=%s expected_match=%v expected_reason=%s",
						fixture.Name, fixture.Tool, result.Matched, result.Reason,
						fixture.ShouldMatch, fixture.ExpectedReason)

					if result.Matched != fixture.ShouldMatch {
						t.Errorf("match mismatch: got %v, want %v",
							result.Matched, fixture.ShouldMatch)
					}

					if result.Reason != fixture.ExpectedReason {
						t.Errorf("reason mismatch: got %q, want %q",
							result.Reason, fixture.ExpectedReason)
					}
				})
			}
		})
	}
}

func TestNormalizeWeztermTextFixtures(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		contains []string
		excludes []string
	}{
		{
			name:     "removes_basic_ansi",
			input:    "\x1b[31mRed text\x1b[0m normal",
			contains: []string{"Red text", "normal"},
			excludes: []string{"\x1b[", "[31m"},
		},
		{
			name:     "removes_256_color",
			input:    "\x1b[38;5;196mBright red\x1b[0m",
			contains: []string{"Bright red"},
			excludes: []string{"\x1b[", "38;5;196"},
		},
		{
			name:     "removes_24bit_color",
			input:    "\x1b[38;2;255;0;0mTrue red\x1b[0m",
			contains: []string{"True red"},
			excludes: []string{"\x1b[", "38;2;255;0;0"},
		},
		{
			name:     "removes_osc_title",
			input:    "\x1b]0;Window Title\x07Some content",
			contains: []string{"Some content"},
			excludes: []string{"\x1b]", "Window Title"},
		},
		{
			name:     "removes_osc_bel",
			input:    "\x1b]52;c;dGVzdA==\x07Text",
			contains: []string{"Text"},
			excludes: []string{"\x1b]", "dGVzdA=="},
		},
		{
			name:     "removes_box_drawing_light",
			input:    "┌──────┐\n│ Text │\n└──────┘",
			contains: []string{"Text"},
			excludes: []string{"┌", "─", "┐", "│", "└", "┘"},
		},
		{
			name:     "removes_box_drawing_heavy",
			input:    "┏━━━━━━┓\n┃ Text ┃\n┗━━━━━━┛",
			contains: []string{"Text"},
			excludes: []string{"┏", "━", "┓", "┃", "┗", "┛"},
		},
		{
			name:     "removes_box_drawing_double",
			input:    "╔══════╗\n║ Text ║\n╚══════╝",
			contains: []string{"Text"},
			excludes: []string{"╔", "═", "╗", "║", "╚", "╝"},
		},
		{
			name:     "removes_block_elements",
			input:    "▀▁▂▃▄▅▆▇█ Text ░▒▓",
			contains: []string{"Text"},
			excludes: []string{"▀", "█", "░", "▓"},
		},
		{
			name:     "preserves_meaningful_text",
			input:    "\x1b[1mYou've\x1b[0m hit your \x1b[31mlimit\x1b[0m",
			contains: []string{"You've", "hit your", "limit"},
			excludes: []string{"\x1b["},
		},
		{
			name:     "normalizes_whitespace",
			input:    "Multiple   spaces\t\ttabs\n\nnewlines",
			contains: []string{"Multiple spaces tabs newlines"},
			excludes: []string{"   ", "\t\t", "\n\n"},
		},
		{
			name:     "handles_nbsp",
			input:    "Non\u00a0breaking\u00a0space",
			contains: []string{"Non breaking space"},
			excludes: []string{"\u00a0"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := normalizeWeztermText(tc.input)

			for _, want := range tc.contains {
				if !strings.Contains(result, want) {
					t.Errorf("expected result to contain %q, got: %q", want, result)
				}
			}

			for _, exclude := range tc.excludes {
				if strings.Contains(result, exclude) {
					t.Errorf("expected result to NOT contain %q, got: %q", exclude, result)
				}
			}
		})
	}
}

func TestMatchWeztermPaneRateLimitVariants(t *testing.T) {
	rateLimitPatterns := []string{
		"You've hit your limit",
		"you've hit your limit",
		"YOU'VE HIT YOUR LIMIT",
		"Youve hit your limit", // no apostrophe
		"usage limit",
		"Usage Limit exceeded",
		"rate limit",
		"Rate Limit Reached",
		"too many requests",
		"Too Many Requests",
		"TOO MANY REQUESTS",
		"resource_exhausted",
		"RESOURCE_EXHAUSTED",
		"429",
		"HTTP 429",
		"Error 429",
	}

	for _, pattern := range rateLimitPatterns {
		t.Run(pattern, func(t *testing.T) {
			content := "Some preamble " + pattern + " some postamble"
			result := matchWeztermPane("claude", content, nil)

			if !result.Matched {
				t.Errorf("expected %q to match as rate_limit, got: %+v", pattern, result)
			}
			if result.Reason != "rate_limit" {
				t.Errorf("expected reason rate_limit, got: %s", result.Reason)
			}
		})
	}
}

func TestMatchWeztermPaneToolMarkerVariants(t *testing.T) {
	claudeMarkers := []string{
		"Claude",
		"claude",
		"CLAUDE",
		"Claude Code",
		"claude code",
		"Anthropic",
		"anthropic",
	}

	for _, marker := range claudeMarkers {
		t.Run("claude_"+marker, func(t *testing.T) {
			content := "Session: " + marker + " ready"
			result := matchWeztermPane("claude", content, nil)

			if !result.Matched {
				t.Errorf("expected %q to match as tool_marker for claude, got: %+v", marker, result)
			}
		})
	}

	codexMarkers := []string{
		"Codex",
		"codex",
		"CODEX",
		"OpenAI",
		"openai",
	}

	for _, marker := range codexMarkers {
		t.Run("codex_"+marker, func(t *testing.T) {
			content := "Session: " + marker + " ready"
			result := matchWeztermPane("codex", content, nil)

			if !result.Matched {
				t.Errorf("expected %q to match as tool_marker for codex, got: %+v", marker, result)
			}
		})
	}

	geminiMarkers := []string{
		"Gemini",
		"gemini",
		"GEMINI",
		"Google AI",
		"google ai",
	}

	for _, marker := range geminiMarkers {
		t.Run("gemini_"+marker, func(t *testing.T) {
			content := "Session: " + marker + " ready"
			result := matchWeztermPane("gemini", content, nil)

			if !result.Matched {
				t.Errorf("expected %q to match as tool_marker for gemini, got: %+v", marker, result)
			}
		})
	}
}

func TestNoFalsePositivesOnCommonCLIs(t *testing.T) {
	// Common CLI outputs that should NEVER match
	commonCLIOutputs := map[string]string{
		"git_log": `commit a1b2c3d4e5f6g7h8i9j0
Author: John Doe <john@example.com>
Date:   Mon Jan 20 10:00:00 2026 -0500

    Add new feature`,

		"cargo_build": `   Compiling myproject v0.1.0
   Compiling serde v1.0.193
    Finished dev [unoptimized + debuginfo] target(s) in 2.54s`,

		"npm_start": `> myapp@1.0.0 start
> node server.js

Server listening on port 3000`,

		"kubectl_pods": `NAME                     READY   STATUS    RESTARTS   AGE
nginx-deployment-abc123  1/1     Running   0          2d
redis-master-def456      1/1     Running   0          3d`,

		"terraform_plan": `Terraform will perform the following actions:

  # aws_instance.example will be created
  + resource "aws_instance" "example" {
      + ami           = "ami-12345678"`,

		"make_output": `gcc -c -o main.o main.c
gcc -c -o utils.o utils.c
gcc -o myapp main.o utils.o`,

		"rustc_error": `error[E0382]: borrow of moved value: 'x'
 --> src/main.rs:5:13
  |
3 |     let x = String::from("hello");
  |         - move occurs because 'x'`,

		"pytest_output": `================================ test session starts ================================
platform linux -- Python 3.11.5, pytest-7.4.3
collected 42 items

tests/test_api.py ....................                                    [ 47%]
tests/test_models.py ......................                               [100%]`,

		"mysql_client": `mysql> SELECT * FROM users LIMIT 5;
+----+----------+-------------------+
| id | username | email             |
+----+----------+-------------------+
|  1 | alice    | alice@example.com |`,

		"redis_cli": `127.0.0.1:6379> KEYS *
1) "session:abc123"
2) "user:456"
127.0.0.1:6379> GET session:abc123
"active"`,
	}

	for name, content := range commonCLIOutputs {
		t.Run(name, func(t *testing.T) {
			for _, tool := range []string{"claude", "codex", "gemini"} {
				result := matchWeztermPane(tool, content, nil)

				if result.Matched {
					t.Errorf("false positive: %s matched as %s (%s) for tool %s",
						name, result.Reason, tool, tool)
				}
			}
		})
	}
}

func TestStripBoxDrawingComprehensive(t *testing.T) {
	testCases := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name:   "light_box_chars",
			input:  "─│┌┐└┘├┤┬┴┼",
			expect: "           ", // All replaced with spaces
		},
		{
			name:   "heavy_box_chars",
			input:  "━┃┏┓┗┛┣┫┳┻╋",
			expect: "           ",
		},
		{
			name:   "double_box_chars",
			input:  "═║╔╗╚╝╠╣╦╩╬",
			expect: "           ",
		},
		{
			name:   "rounded_corners",
			input:  "╭╮╯╰",
			expect: "    ",
		},
		{
			name:   "block_elements",
			input:  "▀▁▂▃▄▅▆▇█▉▊▋▌▍▎▏░▒▓",
			expect: "                   ",
		},
		{
			name:   "mixed_with_text",
			input:  "┌───┐ Text └───┘",
			expect: "      Text      ",
		},
		{
			name:   "preserves_regular_text",
			input:  "Hello, World!",
			expect: "Hello, World!",
		},
		{
			name:   "preserves_numbers",
			input:  "12345",
			expect: "12345",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := stripBoxDrawing(tc.input)
			if result != tc.expect {
				t.Errorf("stripBoxDrawing(%q) = %q, want %q", tc.input, result, tc.expect)
			}
		})
	}
}
