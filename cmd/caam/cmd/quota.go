package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/shallow"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// =============================================================================
// caam quota — remaining usage per Claude profile, read from local disk.
// =============================================================================
//
// `caam limits` answers the same question over the network, but to do so it
// presents every profile's bearer token from a single process: one machine
// speaking for several accounts at once, which is the shared-credential shape
// that gets subscriptions revoked. Claude Code already caches each account's
// utilization in that account's own .claude.json, refreshed by that account's
// own sessions. This command reads those files and nothing else — no token is
// used, no request is made.
//
// The cost of that trade is freshness: a profile's numbers only move when that
// profile actually runs. The table says so, per row, via its AS OF column and
// the "~" marker on figures taken from a frozen vault snapshot.

// Where a row's numbers came from.
const (
	// quotaSourceLive is the live ~/.claude.json of the active profile.
	quotaSourceLive = "live"
	// quotaSourceSnapshot is a vault copy, frozen when the profile was last
	// switched away from.
	quotaSourceSnapshot = "snapshot"
	// quotaSourceShallow is a shallow profile's own HOME.
	quotaSourceShallow = "shallow"
)

// quotaBarWidth is the number of cells in a usage bar.
const quotaBarWidth = 10

// quotaFooter explains what the numbers are and are not.
const quotaFooter = "usage as cached by Claude Code; refreshed by that account's own sessions. No network."

// quotaRow is one profile's cached usage, as rendered and as emitted by --json.
type quotaRow struct {
	Profile string `json:"profile"`
	Email   string `json:"email"`
	Plan    string `json:"plan"`
	Source  string `json:"source"`
	Active  bool   `json:"active"`
	// Lanes lists where this account is set up: "active" (the vault profile
	// currently switched into ~/.claude), "vault" (a vault profile that is
	// not active), and "shallow" or "shallow(<name>)" for a shallow profile
	// (named when its name differs from Profile). One account can be in
	// several lanes at once; its usage is one number regardless.
	Lanes       []string             `json:"lanes"`
	AccountUUID string               `json:"account_uuid"`
	FetchedAt   *time.Time           `json:"fetched_at"`
	Windows     []usage.CachedWindow `json:"windows"`
}

// quotaScan describes where to look for cached usage. Every path is injected
// so the walk can be tested against a temporary vault.
type quotaScan struct {
	vault          *authfile.Vault
	liveClaudeJSON string
	active         string
	shallowMgr     *shallow.Manager
	now            time.Time

	// identityFor resolves the account behind a profile: shallowHome is empty
	// for vault profiles. A nil func leaves every row's email and plan unknown.
	identityFor func(profile, shallowHome string) *identity.Identity
}

var quotaCmd = &cobra.Command{
	Use:   "quota [provider]",
	Short: "Show cached Claude usage per profile (no network)",
	Long: `Show how much of each Claude account's usage window is spent, read from
the usage snapshot Claude Code caches in each profile's own .claude.json.

Unlike 'caam limits', this command makes no request and presents no token: it
only reads files already on disk, so no single process ever speaks for several
accounts at once. The trade-off is freshness — a profile's numbers only change
when that profile itself runs a session. Each row reports how old its snapshot
is, and marks figures read from a frozen vault copy with "~".

Rows cover vault profiles (the active one read live, the rest from the vault)
and shallow profiles. Only Claude caches usage locally.

Examples:
  caam quota            # table of every Claude profile
  caam quota claude     # same; claude is the default provider
  caam quota --json     # machine-readable output`,
	RunE: runQuota,
}

func init() {
	rootCmd.AddCommand(quotaCmd)
	quotaCmd.Flags().Bool("json", false, "output as JSON")
}

func runQuota(cmd *cobra.Command, args []string) error {
	provider := "claude"
	if len(args) > 0 {
		provider = strings.ToLower(strings.TrimSpace(args[0]))
	}
	if provider != "claude" {
		cmd.SilenceUsage = true
		return fmt.Errorf("no local usage cache for %s (only claude caches usage on disk; try: caam limits %s)", provider, provider)
	}

	asJSON, _ := cmd.Flags().GetBool("json")
	now := time.Now()

	scan := quotaScan{
		vault:          vault,
		liveClaudeJSON: liveClaudeJSONPath(),
		now:            now,
		identityFor:    quotaIdentity,
	}
	if active, err := vault.ActiveProfile(authfile.ClaudeAuthFiles()); err == nil {
		scan.active = active
	}
	if mgr, err := shallow.NewManager("", ""); err == nil {
		scan.shallowMgr = mgr
	}

	rows, err := collectQuotaRows(scan)
	if err != nil {
		cmd.SilenceUsage = true
		return err
	}

	out := cmd.OutOrStdout()
	if asJSON {
		return renderQuotaJSON(out, rows)
	}
	return renderQuotaTable(out, rows, now, isTerminal())
}

// collectQuotaRows reads the cached usage of every Claude profile caam knows
// about. A profile with no cache yet is still a row, with no windows: the
// table says so rather than hiding the account.
func collectQuotaRows(scan quotaScan) ([]quotaRow, error) {
	var rows []quotaRow

	names, err := scan.vault.List("claude")
	if err != nil {
		return nil, fmt.Errorf("read vault: %w", err)
	}
	sort.Strings(names)

	for _, name := range names {
		path := scan.vault.BackupPath("claude", name, ".claude.json")
		source := quotaSourceSnapshot
		if name == scan.active && scan.active != "" {
			// The vault copy froze when this profile was switched to; the live
			// file is the one Claude Code has been updating since.
			path = scan.liveClaudeJSON
			source = quotaSourceLive
		}
		rows = append(rows, buildQuotaRow(scan, name, "", path, source, name == scan.active && scan.active != ""))
	}

	rows = append(rows, collectShallowQuotaRows(scan)...)
	return mergeQuotaRows(rows), nil
}

// mergeQuotaRows folds the rows that belong to one account into one. Usage is
// a property of the account, not of the place its cache was read from: a
// vault profile and a shallow profile logged into the same account draw down
// the same windows, so listing them twice with two different ages reads as
// two accounts. The merged row keeps the freshest numbers (latest fetch;
// ties go live > shallow > snapshot), the vault profile's name when there is
// one, and every lane the account is set up in. Rows without an account uuid
// cannot be matched and stay as they are.
func mergeQuotaRows(rows []quotaRow) []quotaRow {
	var out []quotaRow
	index := map[string]int{}
	for _, row := range rows {
		row.Lanes = quotaLanes(row, row.Profile)
		if row.AccountUUID == "" {
			out = append(out, row)
			continue
		}
		i, seen := index[row.AccountUUID]
		if !seen {
			index[row.AccountUUID] = len(out)
			out = append(out, row)
			continue
		}
		out[i] = mergeQuotaPair(out[i], row)
	}
	// Lane names are relative to the row's final name, which is only known
	// once every row of the account has been folded in.
	for i := range out {
		out[i].Lanes = quotaNormalizeLanes(out[i].Lanes, out[i].Profile)
	}
	return out
}

// mergeQuotaPair combines two rows known to be the same account.
func mergeQuotaPair(a, b quotaRow) quotaRow {
	base, other := a, b
	if quotaFresher(b, a) {
		base, other = b, a
	}
	merged := base
	merged.Active = a.Active || b.Active
	// The vault profile's name is the one `caam activate`/`next` use, so it
	// names the row whenever the account has one.
	if base.Source == quotaSourceShallow && other.Source != quotaSourceShallow {
		merged.Profile = other.Profile
	}
	if merged.Email == "" || merged.Email == "n/a" || merged.Email == "unknown" {
		if other.Email != "" {
			merged.Email = other.Email
		}
	}
	merged.Lanes = append(append([]string{}, a.Lanes...), b.Lanes...)
	return merged
}

// quotaFresher reports whether a's numbers should be preferred over b's.
func quotaFresher(a, b quotaRow) bool {
	switch {
	case a.FetchedAt == nil:
		return false
	case b.FetchedAt == nil:
		return true
	case !a.FetchedAt.Equal(*b.FetchedAt):
		return a.FetchedAt.After(*b.FetchedAt)
	}
	return quotaSourceRank(a.Source) > quotaSourceRank(b.Source)
}

func quotaSourceRank(source string) int {
	switch source {
	case quotaSourceLive:
		return 3
	case quotaSourceShallow:
		return 2
	default:
		return 1
	}
}

// quotaLanes describes one unmerged row's lane.
func quotaLanes(row quotaRow, _ string) []string {
	switch {
	case row.Active:
		return []string{"active"}
	case row.Source == quotaSourceShallow:
		// Always carry the shallow profile's name; quotaNormalizeLanes
		// collapses it to plain "shallow" once the merged row's name is known.
		return []string{"shallow(" + row.Profile + ")"}
	default:
		return []string{"vault"}
	}
}

// quotaNormalizeLanes dedupes lanes, orders them active, vault, shallow, and
// renames shallow lanes relative to the merged row's profile name.
func quotaNormalizeLanes(lanes []string, profile string) []string {
	rank := func(l string) int {
		switch {
		case l == "active":
			return 0
		case l == "vault":
			return 1
		default:
			return 2
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, l := range lanes {
		if l == "shallow("+profile+")" {
			l = "shallow"
		}
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

// collectShallowQuotaRows adds a row per Claude shallow profile. Shallow
// profiles are a convenience layer, so any failure to enumerate them is
// silently skipped rather than failing the whole table.
func collectShallowQuotaRows(scan quotaScan) []quotaRow {
	if scan.shallowMgr == nil {
		return nil
	}
	profiles, err := scan.shallowMgr.List()
	if err != nil {
		return nil
	}

	var rows []quotaRow
	for _, p := range profiles {
		provider, err := scan.shallowMgr.ResolveProvider(p.Name)
		if err != nil || provider != "claude" {
			continue
		}
		path := filepath.Join(p.Path, ".claude.json")
		rows = append(rows, buildQuotaRow(scan, p.Name, p.Path, path, quotaSourceShallow, false))
	}
	return rows
}

// buildQuotaRow reads one .claude.json into a row. A missing snapshot is not
// an error: the row simply carries no windows.
func buildQuotaRow(scan quotaScan, name, shallowHome, path, source string, active bool) quotaRow {
	row := quotaRow{
		Profile: name,
		Source:  source,
		Active:  active,
		Windows: []usage.CachedWindow{},
	}

	var id *identity.Identity
	if scan.identityFor != nil {
		id = scan.identityFor(name, shallowHome)
	}
	row.Email, row.Plan = formatIdentityDisplay(id)

	// A missing, older, or corrupt .claude.json all mean the same thing to the
	// reader: this profile has nothing to report yet.
	cached, err := usage.ReadCachedUsage(path, scan.now)
	if err != nil {
		return row
	}

	row.AccountUUID = cached.AccountUUID
	if !cached.FetchedAt.IsZero() {
		fetched := cached.FetchedAt
		row.FetchedAt = &fetched
	}
	row.Windows = cached.Windows
	return row
}

// quotaIdentity is the production identity lookup: the vault's stored
// credentials for a vault profile, the profile's own HOME for a shallow one.
func quotaIdentity(name, shallowHome string) *identity.Identity {
	if shallowHome != "" {
		id, err := identity.ExtractFromClaudeCredentials(filepath.Join(shallowHome, ".claude", ".credentials.json"))
		if err != nil {
			return nil
		}
		normalizeIdentityPlan(id)
		return id
	}
	return getVaultIdentity("claude", name)
}

// liveClaudeJSONPath is the .claude.json Claude Code writes for whichever
// account is currently active, resolved the same way the rest of caam resolves
// Claude's auth files.
func liveClaudeJSONPath() string {
	for _, f := range authfile.ClaudeAuthFiles().Files {
		if filepath.Base(f.Path) == ".claude.json" {
			return f.Path
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

func renderQuotaJSON(w io.Writer, rows []quotaRow) error {
	if rows == nil {
		rows = []quotaRow{}
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

// renderQuotaTable writes the dashboard. Bars are colored only when color is
// true (stdout is a TTY); the table is laid out in plain text first so the
// escape sequences can never disturb column alignment.
func renderQuotaTable(w io.Writer, rows []quotaRow, now time.Time, color bool) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No Claude profiles found.")
		return err
	}

	var table bytes.Buffer
	tw := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)

	fmt.Fprintf(tw, "PROFILE\tLANE\tEMAIL\tPLAN\t5H\tWEEKLY\t%s\tRESETS\tAS OF\n", strings.ToUpper(quotaScopedTitle(rows)))

	// bars[i] holds the plain bar cells of the i-th data line, in the order
	// they appear, so they can be colorized after the layout is fixed.
	bars := make([][]quotaBarCell, len(rows))

	for i, row := range rows {
		// The active marker is plain ASCII on purpose: glyphs such as "●" are
		// ambiguous-width and shift the row in some terminals.
		name := "  " + row.Profile
		if row.Active {
			name = "* " + row.Profile
		}
		lanes := strings.Join(row.Lanes, "+")
		if lanes == "" {
			lanes = "-"
		}

		if len(row.Windows) == 0 {
			// A trailing, un-tab-terminated cell sits outside the aligned
			// columns, so an account with no snapshot cannot stretch the table.
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\tno usage cache yet (run a session)\n", name, lanes, row.Email, row.Plan)
			continue
		}

		snapshot := row.Source != quotaSourceLive
		cells := []quotaBarCell{
			quotaCell(row.Windows, usage.CachedKindSession, snapshot),
			quotaCell(row.Windows, usage.CachedKindWeeklyAll, snapshot),
			quotaCell(row.Windows, usage.CachedKindWeeklyScoped, snapshot),
		}
		for _, c := range cells {
			if c.Percent >= 0 {
				bars[i] = append(bars[i], c)
			}
		}

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			name, lanes, row.Email, row.Plan, cells[0].Text, cells[1].Text, cells[2].Text,
			quotaResetsCell(row.Windows), quotaAsOfCell(row, now))
	}

	if err := tw.Flush(); err != nil {
		return err
	}

	lines := strings.Split(strings.TrimRight(table.String(), "\n"), "\n")
	if color {
		colorizeQuotaBars(lines, bars)
	}

	for _, line := range lines {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "\n%s\n", quotaFooter)
	return err
}

// colorizeQuotaBars recolors the already-laid-out bar cells in place.
func colorizeQuotaBars(lines []string, bars [][]quotaBarCell) {
	for i, rowBars := range bars {
		line := i + 1 // line 0 is the header
		if line >= len(lines) {
			break
		}
		lines[line] = colorizeQuotaLine(lines[line], rowBars, func(c quotaBarCell) string {
			return quotaBarStyle(c.Percent).Render(c.Text)
		})
	}
}

// colorizeQuotaLine replaces each cell's plain text with paint(cell), scanning
// left to right from the end of the previous match. Two cells on one row can
// render identically (two windows at 0%, say), so a search that restarts from
// the beginning of the line would land inside the cell already painted.
func colorizeQuotaLine(line string, cells []quotaBarCell, paint func(quotaBarCell) string) string {
	var out strings.Builder
	rest := line
	for _, cell := range cells {
		idx := strings.Index(rest, cell.Text)
		if idx < 0 {
			continue
		}
		out.WriteString(rest[:idx])
		out.WriteString(paint(cell))
		rest = rest[idx+len(cell.Text):]
	}
	out.WriteString(rest)
	return out.String()
}

// quotaBarStyle grades a bar by how much of the window is spent.
func quotaBarStyle(percent int) lipgloss.Style {
	switch {
	case percent < 50:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	case percent < 80:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	}
}

// quotaBarCell is a rendered bar and the percentage it stands for. Percent is
// -1 when the account has no such window, so the cell is a plain dash.
type quotaBarCell struct {
	Text    string
	Percent int
}

// quotaCell renders one window as a bar plus its percentage. Figures from a
// frozen snapshot are prefixed with "~": they are as of the AS OF column, not
// as of now.
func quotaCell(windows []usage.CachedWindow, kind string, snapshot bool) quotaBarCell {
	for _, w := range windows {
		if w.Kind != kind {
			continue
		}
		marker := " "
		if snapshot {
			marker = "~"
		}
		return quotaBarCell{
			Text:    fmt.Sprintf("%s %s%3d%%", quotaBar(w.Percent), marker, w.Percent),
			Percent: w.Percent,
		}
	}
	return quotaBarCell{Text: "-", Percent: -1}
}

// quotaResetsCell shows when the weekly window rolls over: the deadline that
// actually shapes a week of work.
func quotaResetsCell(windows []usage.CachedWindow) string {
	for _, w := range windows {
		if w.Kind == usage.CachedKindWeeklyAll && w.ResetsAt != nil {
			return w.ResetsAt.Local().Format("Mon Jan 2 15:04")
		}
	}
	return "-"
}

func quotaAsOfCell(row quotaRow, now time.Time) string {
	if row.FetchedAt == nil {
		return "-"
	}
	age := now.Sub(*row.FetchedAt)
	if age < 0 {
		age = 0
	}
	return formatQuotaAge(age)
}

// quotaScopedTitle names the per-model column after whichever model the
// accounts' weekly_scoped windows track.
func quotaScopedTitle(rows []quotaRow) string {
	for _, row := range rows {
		for _, w := range row.Windows {
			if w.Kind == usage.CachedKindWeeklyScoped && w.Label != "" && w.Label != usage.CachedKindWeeklyScoped {
				return w.Label
			}
		}
	}
	return "model"
}

// quotaBar draws a percentage as a fixed-width bar.
func quotaBar(percent int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := (percent*quotaBarWidth + 50) / 100
	if filled > quotaBarWidth {
		filled = quotaBarWidth
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", quotaBarWidth-filled)
}

// formatQuotaAge renders how stale a snapshot is, coarsely: the reader only
// needs to know whether to trust it.
func formatQuotaAge(age time.Duration) string {
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(age.Hours())/24)
	}
}
