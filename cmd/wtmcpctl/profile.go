package main

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/plugin"
	"github.com/LeGambiArt/wtmcp/internal/profile"
)

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage and validate agent profiles",
	Long: `Manage and validate agent profiles (profiles.d/).

Profiles restrict which tools an agent can discover and call, keyed by
the agent's TLS client-certificate identity (or the --profile flag on
stdio). See 'wtmcpctl profile check' to validate configuration.`,
}

var profileCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Validate profile configuration in profiles.d/",
	Args:  cobra.NoArgs,
	RunE:  runProfileCheck,
}

var profileListCmd = &cobra.Command{
	Use:   "list",
	Short: "List defined profiles",
	Args:  cobra.NoArgs,
	RunE:  runProfileList,
}

var profileTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Test which profile matches a given identity",
	Args:  cobra.NoArgs,
	RunE:  runProfileTest,
}

func init() {
	profileCheckCmd.Flags().Bool("with-plugins", false,
		"Also validate plugin name references against discovered plugins")
	profileTestCmd.Flags().String("cn", "", "Common Name to test")
	profileTestCmd.Flags().String("san-uri", "", "SAN URI to test")
	profileTestCmd.Flags().String("san-dns", "", "SAN DNS name to test")
	profileTestCmd.Flags().String("san-email", "", "SAN email to test")
	profileCmd.AddCommand(profileCheckCmd, profileListCmd, profileTestCmd)
}

// loadProfilesForCtl resolves the profiles.d directory from config and
// loads it. Returns the config, workdir, and load result.
func loadProfilesForCtl() (*config.Config, string, *config.ProfileLoadResult, error) {
	result, err := getDiscoveryResult()
	if err != nil {
		return nil, "", nil, err
	}
	profilesDir := config.ResolveProfilesDir(result.Config, result.Workdir)
	loaded, err := config.LoadProfiles(profilesDir)
	if err != nil {
		return nil, "", nil, err
	}
	return result.Config, result.Workdir, loaded, nil
}

func runProfileCheck(cmd *cobra.Command, _ []string) error {
	cfg, workdir, loaded, err := loadProfilesForCtl()
	if err != nil {
		return err
	}
	profilesDir := config.ResolveProfilesDir(cfg, workdir)

	problems := profile.Validate(loaded, cfg.Profiles.Default)

	if withPlugins, _ := cmd.Flags().GetBool("with-plugins"); withPlugins {
		result, derr := getDiscoveryResult()
		if derr != nil {
			return derr
		}
		problems = append(problems, profile.ValidatePluginRefs(loaded, result.Manager)...)
	}

	printProfileCheckReport(profilesDir, cfg.Profiles.Default, loaded, problems)

	if config.HasFatal(problems) {
		os.Exit(1)
	}
	return nil
}

func printProfileCheckReport(profilesDir, defaultProfile string, loaded *config.ProfileLoadResult, problems []config.ProfileLoadError) {
	fmt.Printf("profiles.d directory: %s\n\n", profilesDir)

	// Per-file loading status.
	parseErr := make(map[string]string)
	for _, e := range loaded.Errors {
		if e.File != "" {
			parseErr[e.File] = e.Message
		}
	}
	defCount := make(map[string]int)
	for _, file := range loaded.DefSource {
		defCount[file]++
	}
	ruleCount := make(map[string]int)
	for _, r := range loaded.Rules {
		ruleCount[r.File]++
	}

	fmt.Println("loading profile files:")
	if len(loaded.Files) == 0 {
		fmt.Println("  (none found)")
	}
	for _, file := range loaded.Files {
		if msg, bad := parseErr[file]; bad {
			fmt.Printf("  ✗ %s: %s\n", file, msg)
			continue
		}
		fmt.Printf("  ✓ %s (%d definitions, %d rules)\n", file, defCount[file], ruleCount[file])
	}

	// Definitions summary.
	defNames := sortedDefNames(loaded)
	fmt.Printf("\ndefinitions: %d\n", len(defNames))
	for _, name := range defNames {
		def := loaded.Definitions[name]
		fmt.Printf("  - %s: %s\n", name, definitionSummary(def))
	}

	// Rules listing.
	fmt.Printf("\nrules: %d (order-independent; each is one exact field match)\n", len(loaded.Rules))
	for _, r := range loaded.Rules {
		field, value := matchFieldValue(r.Match)
		fmt.Printf("  %s = %s → %s   [%s]\n", field, value, r.Profile, r.File)
	}

	// Default profile.
	fmt.Println()
	printDefaultLine(defaultProfile)

	// Errors and warnings.
	var errList, warnList []config.ProfileLoadError
	for _, p := range problems {
		if p.Fatal {
			errList = append(errList, p)
		} else {
			warnList = append(warnList, p)
		}
	}
	if len(errList) > 0 {
		fmt.Println("\nerrors:")
		for _, e := range errList {
			fmt.Printf("  ✗ %s\n", formatProblem(e))
		}
	}
	if len(warnList) > 0 {
		fmt.Println("\nwarnings:")
		for _, w := range warnList {
			fmt.Printf("  ! %s\n", formatProblem(w))
		}
	}

	fmt.Println()
	switch {
	case len(errList) == 0 && len(warnList) == 0:
		fmt.Println("status: ok")
	default:
		fmt.Printf("status: %d errors, %d warnings\n", len(errList), len(warnList))
	}
}

func printDefaultLine(def string) {
	if def == "" {
		fmt.Println("default profile: (none) — unmatched agents are DENIED all tools (fail closed)")
		return
	}
	fmt.Printf("default profile: %s  (unmatched agents → this profile)\n", def)
}

func formatProblem(p config.ProfileLoadError) string {
	if p.File != "" {
		return fmt.Sprintf("%s: %s", p.File, p.Message)
	}
	return p.Message
}

func runProfileList(_ *cobra.Command, _ []string) error {
	cfg, _, loaded, err := loadProfilesForCtl()
	if err != nil {
		return err
	}

	defNames := sortedDefNames(loaded)
	if len(defNames) == 0 {
		fmt.Println("no profiles defined")
		return nil
	}

	// Compile the filters so the TOOLS column can report how many of the
	// discovered tools each profile can actually call, rather than a raw
	// pattern count (one ".*" pattern can match many tools, or none).
	resolver, err := profile.NewResolver(cfg.Profiles.Default, loaded)
	if err != nil {
		return fmt.Errorf("profile configuration invalid: %w (run 'wtmcpctl profile check')", err)
	}
	manifests, err := discoverPlugins()
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "PROFILE\tPLUGINS\tTOOLS\tDENY\tSOURCE") //nolint:errcheck // tabwriter, flushed below
	for _, name := range defNames {
		def := loaded.Definitions[name]
		// TOOLS and DENY report how many discovered tools the profile can
		// and cannot call, computed from the compiled filter, rather than
		// raw pattern counts. Both mirror the split shown by "profile test".
		tools, deny := "?", 0
		if filter, ok := resolver.FilterByName(name); ok {
			allowed, denied := classifyTools(filter, manifests)
			tools = toolCountLabel(len(allowed), len(allowed)+len(denied))
			deny = len(denied)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", //nolint:errcheck // tabwriter, flushed below
			name, pluginSummary(def), tools, deny, loaded.DefSource[name])
	}
	return w.Flush()
}

func runProfileTest(cmd *cobra.Command, _ []string) error {
	cfg, _, loaded, err := loadProfilesForCtl()
	if err != nil {
		return err
	}

	resolver, err := profile.NewResolver(cfg.Profiles.Default, loaded)
	if err != nil {
		return fmt.Errorf("profile configuration invalid: %w (run 'wtmcpctl profile check')", err)
	}

	cn, _ := cmd.Flags().GetString("cn")
	sanURI, _ := cmd.Flags().GetString("san-uri")
	sanDNS, _ := cmd.Flags().GetString("san-dns")
	sanEmail, _ := cmd.Flags().GetString("san-email")

	if cn == "" && sanURI == "" && sanDNS == "" && sanEmail == "" {
		return fmt.Errorf("specify at least one of --cn, --san-uri, --san-dns, --san-email")
	}

	id := profile.Identity{CN: cn}
	if sanURI != "" {
		id.SANURIs = []string{sanURI}
	}
	if sanDNS != "" {
		id.SANDNSs = []string{sanDNS}
	}
	if sanEmail != "" {
		id.SANEmails = []string{sanEmail}
	}

	if !resolver.Configured() {
		fmt.Println("no profiles configured — all tools visible (no filtering)")
		return nil
	}

	res := resolver.Resolve(id)
	switch {
	case res.Ambiguous:
		fmt.Printf("identity matched multiple profiles %v\n", res.Matched)
		fmt.Println("  → DENY ALL (fail closed) — an identity must map to exactly one profile")
	case res.MatchedField != "":
		fmt.Printf("matched rule: %s = %s   [%s]\n", res.MatchedField, res.MatchedValue, res.MatchedFile)
		fmt.Printf("  profile: %s\n", res.Profile)
	case res.UsedDefault:
		fmt.Println("no rule matched")
		fmt.Printf("default profile: %s\n", res.Profile)
	default:
		fmt.Println("no rule matched")
		fmt.Printf("default profile: (none) → DENY ALL, fail closed\n")
	}

	// Enumerate discovered tools and classify them under the filter.
	result, derr := getDiscoveryResult()
	if derr != nil {
		return derr
	}
	printAllowedDenied(res.Filter, result.Manager.Manifests())
	return nil
}

// classifyTools splits the discovered tools into those the filter allows
// and those it denies. Both slices are sorted by tool name.
func classifyTools(filter *profile.Filter, manifests map[string]*plugin.Manifest) (allowed, denied []string) {
	for _, m := range manifests {
		for _, t := range m.Tools {
			if filter.IsAllowed(m.Name, t.Name) {
				allowed = append(allowed, t.Name)
			} else {
				denied = append(denied, t.Name)
			}
		}
	}
	sort.Strings(allowed)
	sort.Strings(denied)
	return allowed, denied
}

func printAllowedDenied(filter *profile.Filter, manifests map[string]*plugin.Manifest) {
	allowed, denied := classifyTools(filter, manifests)

	fmt.Printf("\nallowed tools (%d):\n", len(allowed))
	for _, t := range allowed {
		fmt.Printf("  %s\n", t)
	}
	fmt.Printf("\ndenied tools (%d):\n", len(denied))
	for _, t := range denied {
		fmt.Printf("  %s\n", t)
	}
}

// --- small formatting helpers ---

func sortedDefNames(loaded *config.ProfileLoadResult) []string {
	return slices.Sorted(maps.Keys(loaded.Definitions))
}

// matchFieldValue returns the single set (field, value) of a match, or
// a diagnostic string when the match is not exactly one field.
func matchFieldValue(m config.ProfileMatch) (string, string) {
	var pairs [][2]string
	if m.CN != "" {
		pairs = append(pairs, [2]string{"cn", m.CN})
	}
	if m.SANURI != "" {
		pairs = append(pairs, [2]string{"san_uri", m.SANURI})
	}
	if m.SANDNS != "" {
		pairs = append(pairs, [2]string{"san_dns", m.SANDNS})
	}
	if m.SANEmail != "" {
		pairs = append(pairs, [2]string{"san_email", m.SANEmail})
	}
	switch len(pairs) {
	case 1:
		return pairs[0][0], pairs[0][1]
	case 0:
		return "(no field)", ""
	default:
		return "(multiple fields)", ""
	}
}

func definitionSummary(def config.ProfileDefinition) string {
	allowPlugins := len(def.Allow)
	s := fmt.Sprintf("%d allow %s", allowPlugins, plural(allowPlugins, "plugin", "plugins"))
	if _, wild := def.Allow["*"]; wild {
		s += " (wildcard)"
	}
	if len(def.Deny) > 0 {
		s += fmt.Sprintf(", %d deny %s", len(def.Deny), plural(len(def.Deny), "plugin", "plugins"))
	}
	return s
}

// pluginSummary returns the PLUGINS column for profile list: the number of
// allow entries, annotated with (*) when the wildcard plugin is present.
func pluginSummary(def config.ProfileDefinition) string {
	n := len(def.Allow)
	if _, wild := def.Allow["*"]; wild {
		return fmt.Sprintf("%d (*)", n)
	}
	return fmt.Sprintf("%d", n)
}

// toolCountLabel renders the TOOLS column: "all" when a profile grants
// every discovered tool, otherwise the exact allowed count.
func toolCountLabel(allowed, total int) string {
	if total > 0 && allowed == total {
		return "all"
	}
	return fmt.Sprintf("%d", allowed)
}

func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}
