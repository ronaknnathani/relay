package programui

import (
	"io/fs"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// assetNames lists every file the browser is allowed to load.
var assetNames = []string{"assets/index.html", "assets/app.css", "assets/app.js"}

// forbiddenAssetSubstrings keeps the embedded UI local-only and free of any
// API that turns data into markup.
var forbiddenAssetSubstrings = []string{
	"http://",
	"https://",
	"innerHTML",
	"outerHTML",
	"insertAdjacentHTML",
	"document.write",
	"eval(",
	"new Function",
	"@font-face",
	"@import",
}

func readAsset(t *testing.T, name string) string {
	t.Helper()
	data, err := fs.ReadFile(embeddedAssets, name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func requireContains(t *testing.T, name, content string, wanted []string) {
	t.Helper()
	for _, want := range wanted {
		if !strings.Contains(content, want) {
			t.Errorf("%s is missing %q", name, want)
		}
	}
}

func requireAbsent(t *testing.T, name, content string, unwanted []string) {
	t.Helper()
	for _, banned := range unwanted {
		if strings.Contains(content, banned) {
			t.Errorf("%s still contains %q", name, banned)
		}
	}
}

func TestEmbeddedAssetsStayLocalAndSemantic(t *testing.T) {
	for _, name := range assetNames {
		content := readAsset(t, name)
		for _, forbidden := range forbiddenAssetSubstrings {
			if strings.Contains(content, forbidden) {
				t.Errorf("%s contains forbidden %q", name, forbidden)
			}
		}
	}

	index := readAsset(t, "assets/index.html")
	requireContains(t, "index.html", index, []string{
		`<main id="views">`,
		"<section",
		"<table>",
		`<svg id="graph"`,
		`role="img"`,
		`id="graph-nodes"`,
		`id="graph-edges"`,
		`id="ledger-rows"`,
		`id="detail-body"`,
		`<input id="filter"`,
		`id="status-filters"`,
		`id="goal-body"`,
		`id="reconnect"`,
		`id="refresh"`,
		`aria-live="polite"`,
		`<label for="filter">`,
		`class="visually-hidden"`,
		"<caption",
		`<a class="skip-link"`,
	})

	if strings.Count(index, "<script") != 1 || !strings.Contains(index, `<script src="/app.js"></script>`) {
		t.Error("index.html must load exactly one local script, /app.js, with no defer so the theme lands before paint")
	}
	if strings.Count(index, "<link ") != 1 || !strings.Contains(index, `href="/app.css"`) {
		t.Error("index.html must link exactly one local stylesheet, /app.css")
	}
}

func TestIndexBootstrapsTheLightThemeBeforePaint(t *testing.T) {
	index := readAsset(t, "assets/index.html")
	requireContains(t, "index.html", index, []string{
		`<html lang="en" data-theme="light">`,
		`<meta name="color-scheme" content="light dark">`,
		`id="theme-toggle"`,
		`aria-label="Switch to dark theme"`,
	})

	head := index[strings.Index(index, "<head>"):strings.Index(index, "</head>")]
	if !strings.Contains(head, `<script src="/app.js">`) {
		t.Error("app.js must load inside <head> so the stored theme applies before the first paint")
	}
	if strings.Contains(head, "defer") || strings.Contains(head, "async") {
		t.Error("the theme bootstrap script must not be deferred or async")
	}

	script := readAsset(t, "assets/app.js")
	requireContains(t, "app.js", script, []string{
		`const THEME_KEY = "relay.program.theme"`,
		"window.localStorage.getItem(THEME_KEY)",
		"window.localStorage.setItem(THEME_KEY, next)",
		"document.documentElement.dataset.theme",
		`applyTheme(storedTheme() || "light")`,
		"function toggleTheme()",
		`dom.themeToggle.setAttribute("aria-label"`,
	})
	if !strings.Contains(script, "function start()") ||
		!strings.Contains(script, `document.addEventListener("DOMContentLoaded", start)`) {
		t.Error("app.js must defer its own boot to DOMContentLoaded while the theme applies immediately")
	}
	if strings.Index(script, "applyTheme(storedTheme()") > strings.Index(script, "function start()") {
		t.Error("the theme must be applied before the app boot code")
	}
}

func TestIndexExposesFourTabsAndDefaultsToRoadmap(t *testing.T) {
	index := readAsset(t, "assets/index.html")
	requireContains(t, "index.html", index, []string{
		`role="tablist"`,
		`id="tab-roadmap"`,
		`id="tab-tasks"`,
		`id="tab-decisions"`,
		`id="tab-goal"`,
		`id="panel-roadmap"`,
		`id="panel-tasks"`,
		`id="panel-decisions"`,
		`id="panel-goal"`,
		`aria-controls="panel-roadmap"`,
		`aria-labelledby="tab-roadmap"`,
	})
	if strings.Count(index, `role="tab"`) != 4 || strings.Count(index, `role="tabpanel"`) != 4 {
		t.Error("index.html must declare exactly four tabs and four tab panels")
	}
	if !strings.Contains(index, `id="tab-roadmap" class="segment" type="button" role="tab" aria-selected="true"`) {
		t.Error("Roadmap must be the selected tab in the served markup")
	}
	for _, hidden := range []string{"panel-tasks", "panel-decisions", "panel-goal"} {
		_, after, found := strings.Cut(index, `id="`+hidden+`"`)
		if !found {
			t.Errorf("index.html has no %s section", hidden)
			continue
		}
		tag, _, _ := strings.Cut(after, ">")
		if !strings.Contains(tag, "hidden") {
			t.Errorf("%s must start hidden so only Roadmap renders on load", hidden)
		}
	}

	script := readAsset(t, "assets/app.js")
	requireContains(t, "app.js", script, []string{
		`const TABS = ["roadmap", "tasks", "decisions", "goal"]`,
		"function selectTab(",
		"function onTabKey(",
		`panel.hidden = key !== tab`,
	})
}

func TestIndexReplacesTheDetailRailWithAnOnDemandDrawer(t *testing.T) {
	index := readAsset(t, "assets/index.html")
	requireContains(t, "index.html", index, []string{
		`<div id="drawer" class="drawer" hidden>`,
		`id="drawer-scrim"`,
		`role="dialog"`,
		`aria-modal="true"`,
		`aria-labelledby="drawer-title"`,
		`id="drawer-close"`,
		`aria-label="Close task detail"`,
	})
	requireAbsent(t, "index.html", index, []string{
		"<aside",
		"column--detail",
		"column--primary",
		`class="instrument"`,
		"micro-label",
	})
	if strings.Index(index, `id="drawer"`) > strings.Index(index, `id="detail-panel"`) {
		t.Error("the detail panel must live inside the drawer, not as a permanent column")
	}

	script := readAsset(t, "assets/app.js")
	requireContains(t, "app.js", script, []string{
		"function openDrawer(instant)",
		"function closeDrawer()",
		"function onDrawerKey(event)",
		"function focusables()",
		`event.key === "Escape"`,
		`event.key !== "Tab"`,
		"state.drawerReturn",
		`dom.drawerPanel.focus({ preventScroll: true })`,
	})
}

func TestRoadmapRunsTopToBottom(t *testing.T) {
	index := readAsset(t, "assets/index.html")
	requireContains(t, "index.html", index, []string{
		`<svg id="graph"`,
		`id="graph-edges"`,
		`id="graph-nodes" class="roadmap__stages"`,
		"Dependencies flow top to bottom",
		"run in parallel",
	})
	requireAbsent(t, "index.html", index, []string{
		"Dependencies run left to right",
		"left to right",
	})

	styles := readAsset(t, "assets/app.css")
	_, stages, found := strings.Cut(styles, ".roadmap__stages {")
	if !found {
		t.Fatal("app.css no longer lays out roadmap stages")
	}
	stageRule, _, _ := strings.Cut(stages, "}")
	if !strings.Contains(stageRule, "flex-direction: column") {
		t.Errorf("stages must stack top to bottom, got:\n%s", stageRule)
	}

	_, roadmap, _ := strings.Cut(styles, ".roadmap {")
	roadmapRule, _, _ := strings.Cut(roadmap, "}")
	if strings.Contains(roadmapRule, "overflow-x") {
		t.Errorf("the roadmap must not scroll sideways any more, got:\n%s", roadmapRule)
	}

	_, content, _ := strings.Cut(styles, ".roadmap__content {")
	contentRule, _, _ := strings.Cut(content, "}")
	if strings.Contains(contentRule, "inline-block") || strings.Contains(contentRule, "min-width: 100%") {
		t.Errorf("the roadmap content no longer needs to grow past the viewport, got:\n%s", contentRule)
	}

	// Tasks in one stage share a responsive row so parallelism is visible.
	_, row, found := strings.Cut(styles, ".stage__row {")
	if !found {
		t.Fatal("app.css has no .stage__row for tasks inside a stage")
	}
	rowRule, _, _ := strings.Cut(row, "}")
	for _, want := range []string{"display: grid", "repeat(auto-fill", "minmax(min(260px, 100%), 1fr)"} {
		if !strings.Contains(rowRule, want) {
			t.Errorf(".stage__row is missing %q, got:\n%s", want, rowRule)
		}
	}

	_, card, _ := strings.Cut(styles, "\n.card {")
	cardRule, _, _ := strings.Cut(card, "}")
	if strings.Contains(cardRule, "width: 240px") {
		t.Error("cards must size to their grid track instead of a fixed column width")
	}

	script := readAsset(t, "assets/app.js")
	requireContains(t, "app.js", script, []string{
		`make("div", "stage__row")`,
		`card.dataset.stage = String(index)`,
		"function downwardPath(from, to)",
		"function cardInStage(order, current, step)",
		"` V ${round(y2)}`",
	})
	if strings.Contains(script, "C ${x1 + bend}") {
		t.Error("app.js still draws the old left-to-right bezier connectors")
	}
}

func TestDrawerFillsMostOfTheScreen(t *testing.T) {
	styles := readAsset(t, "assets/app.css")
	_, panel, found := strings.Cut(styles, ".drawer__panel {")
	if !found {
		t.Fatal("app.css no longer styles the drawer panel")
	}
	rule, _, _ := strings.Cut(panel, "}")
	if !strings.Contains(rule, "width: min(80vw,") {
		t.Errorf("the desktop drawer must be about 80vw, got:\n%s", rule)
	}
	for _, edge := range []string{"top: 0;", "bottom: 0;"} {
		if !strings.Contains(rule, edge) {
			t.Errorf("the drawer must span the full viewport height, missing %q", edge)
		}
	}
	if strings.Contains(rule, "min(540px") || strings.Contains(rule, "25rem") {
		t.Error("the drawer must not be capped at the old narrow width")
	}

	_, medium, _ := strings.Cut(styles, "@media (max-width: 960px)")
	mediumBlock, _, _ := strings.Cut(medium, "@media (max-width: 560px)")
	if !strings.Contains(mediumBlock, "width: 92vw;") {
		t.Errorf("at 960px the drawer should take about 92vw, got:\n%s", mediumBlock)
	}

	_, small, _ := strings.Cut(styles, "@media (max-width: 560px)")
	smallBlock, _, _ := strings.Cut(small, "@media (prefers-reduced-motion")
	if !strings.Contains(smallBlock, "width: 100vw;") {
		t.Errorf("on phones the drawer should be full screen, got:\n%s", smallBlock)
	}
}

func TestDrawerHasAStickySectionBar(t *testing.T) {
	index := readAsset(t, "assets/index.html")
	requireContains(t, "index.html", index, []string{
		`<div id="drawer-scroll" class="drawer__scroll">`,
		`<div id="drawer-nav" class="drawer__nav" role="toolbar" aria-label="Task sections">`,
		`<div id="detail-body" class="drawer__body">`,
	})
	if strings.Index(index, `id="drawer-nav"`) > strings.Index(index, `id="detail-body"`) {
		t.Error("the section bar must sit above the detail body")
	}

	styles := readAsset(t, "assets/app.css")
	_, nav, found := strings.Cut(styles, ".drawer__nav {")
	if !found {
		t.Fatal("app.css has no drawer section bar")
	}
	navRule, _, _ := strings.Cut(nav, "}")
	for _, want := range []string{"position: sticky", "top: 0", "overflow-x: auto", "border-bottom: 1px solid var(--border)"} {
		if !strings.Contains(navRule, want) {
			t.Errorf("the section bar is missing %q, got:\n%s", want, navRule)
		}
	}

	_, tab, _ := strings.Cut(styles, ".drawer__tab {")
	tabRule, _, _ := strings.Cut(tab, "}")
	if !strings.Contains(tabRule, "border-bottom: 2px solid transparent") {
		t.Errorf("section tabs should use an underline indicator, not a pill, got:\n%s", tabRule)
	}
	if strings.Contains(tabRule, "border-radius: 999px") {
		t.Error("section tabs must not be pills")
	}
	if !strings.Contains(styles, `.drawer__tab[aria-current="true"] {`) {
		t.Error("the active section tab needs a visible indicator")
	}
	if !strings.Contains(styles, ".drawer__tab:active {\n  transform: scale(0.98);\n}") {
		t.Error("section tabs need pressed feedback")
	}

	script := readAsset(t, "assets/app.js")
	requireContains(t, "app.js", script, []string{
		"const DETAIL_SECTIONS = [",
		`["overview", "Overview"]`,
		`["dependencies", "Dependencies"]`,
		`["workflow", "Workflow"]`,
		`["pull-request", "Pull request"]`,
		`["worker", "Worker"]`,
		`["notes", "Notes"]`,
		`["decisions", "Decisions"]`,
		`["contracts", "Contracts"]`,
		`["files", "Files"]`,
		`["warnings", "Warnings"]`,
		"function renderDetailNav(present)",
		"function showDetailSection(key)",
		"function markDetailSection(key)",
		"function onDetailNavKey(event)",
		"function syncDetailSection()",
		"function fitDetailTail()",
		`node.id = ` + "`section-${key}`",
		`node.dataset.section = key`,
		`tab.setAttribute("aria-controls", ` + "`section-${key}`" + `)`,
		`tab.setAttribute("aria-current", active ? "true" : "false")`,
		"dom.drawerScroll.scrollTop += sectionBox.top - scrollBox.top - offset;",
	})

	// Only sections that were actually built become tabs.
	if !strings.Contains(script, "const node = built.get(key);\n    if (!node) {\n      return;\n    }") {
		t.Error("absent sections must be skipped rather than rendered as dead tabs")
	}
	for _, key := range []string{"ArrowRight", "ArrowLeft", "Home", "End"} {
		if !strings.Contains(script, `event.key === "`+key+`"`) {
			t.Errorf("the section bar must handle %s", key)
		}
	}
	// Scroll position and active section survive a poll.
	requireContains(t, "app.js", script, []string{
		"const sameTask = item !== null && state.detailItem === item.id;",
		"const keptScroll = sameTask ? dom.drawerScroll.scrollTop : 0;",
		`const keptSection = sameTask ? state.detailSection : "";`,
		"dom.drawerScroll.scrollTop = keptScroll;",
	})
}

func TestSectionNavigationNeverAnimates(t *testing.T) {
	styles := readAsset(t, "assets/app.css")
	requireAbsent(t, "app.css", styles, []string{"scroll-behavior"})

	script := readAsset(t, "assets/app.js")
	requireAbsent(t, "app.js", script, []string{
		`behavior: "smooth"`,
		"scrollIntoView({ behavior",
	})
	// Section jumps assign scrollTop directly; nothing eases them.
	if !strings.Contains(script, "dom.drawerScroll.scrollTop += sectionBox.top") {
		t.Error("section jumps must assign scrollTop directly so they are instant")
	}
}

func TestStylesUseTheLightFirstTokenSetAndADarkCounterpart(t *testing.T) {
	styles := strings.ToLower(readAsset(t, "assets/app.css"))
	const cancelledLaneToken = "--lane-cancelled:" //nolint:misspell // Matches Relay's persisted status spelling.
	requireContains(t, "app.css", styles, []string{
		"color-scheme: light;",
		"--canvas: #f7f5ef;",
		"--surface: #fffdf8;",
		"--second: #f1eee6;",
		"--ink: #26231f;",
		"--muted: #6b665e;",
		"--border: #ded8ce;",
		"--border-strong: #c8c0b4;",
		"--accent: #d97757;",
		"--accent-text: #b4491f;",
		"--green: #2c6a45;",
		"--amber: #8f5a12;",
		"--red: #b4342a;",
		"--accent-wash: #fbf0ea;",
		`[data-theme="dark"]`,
		"color-scheme: dark;",
		"--lane-pending:",
		"--lane-dispatched:",
		"--lane-in-review:",
		"--lane-blocked:",
		"--lane-merged:",
		cancelledLaneToken,
		"--font-sans: -apple-system",
		"--font-mono: ui-monospace",
		":focus-visible",
	})
	if !strings.Contains(styles, "segoe ui") {
		t.Error("app.css must fall back to the system UI sans stack")
	}
	requireAbsent(t, "app.css", styles, []string{
		"text-transform: uppercase",
		"letter-spacing: 0.14em",
		"url(",
		// The cool blue-grey palette must be gone, not merely overridden.
		"#f5f7fa",
		"#ffffff;",
		"#172033",
		"#667085",
		"#e4e7ec",
		"#2563eb",
		"--blue:",
		"--hairline:",
		"--sunk:",
		"--blue-wash:",
	})
}

// relativeLuminance implements the WCAG 2.1 definition for an sRGB hex color.
func relativeLuminance(t *testing.T, hex string) float64 {
	t.Helper()
	if len(hex) != 7 || hex[0] != '#' {
		t.Fatalf("color %q is not a six digit hex value", hex)
	}
	channel := func(offset int) float64 {
		value, err := strconv.ParseUint(hex[offset:offset+2], 16, 8)
		if err != nil {
			t.Fatalf("parse color %q: %v", hex, err)
		}
		scaled := float64(value) / 255
		if scaled <= 0.03928 {
			return scaled / 12.92
		}
		return math.Pow((scaled+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(1) + 0.7152*channel(3) + 0.0722*channel(5)
}

func contrastRatio(t *testing.T, foreground, background string) float64 {
	t.Helper()
	lighter, darker := relativeLuminance(t, foreground), relativeLuminance(t, background)
	if lighter < darker {
		lighter, darker = darker, lighter
	}
	return (lighter + 0.05) / (darker + 0.05)
}

// lightToken reads one custom property out of the light :root block.
func lightToken(t *testing.T, styles, name string) string {
	t.Helper()
	root, _, found := strings.Cut(styles, `[data-theme="dark"]`)
	if !found {
		t.Fatal("app.css has no dark theme block to bound the light tokens")
	}
	_, after, found := strings.Cut(root, name+": ")
	if !found {
		t.Fatalf("app.css does not define %s in the light theme", name)
	}
	value, _, _ := strings.Cut(after, ";")
	return strings.TrimSpace(value)
}

func TestLightStatusTokensMeetAAForNormalText(t *testing.T) {
	styles := strings.ToLower(readAsset(t, "assets/app.css"))
	surface := lightToken(t, styles, "--surface")

	// Every one of these renders as normal-size text on --surface: status
	// words, flags, the next-action label, PR numbers and table cells.
	for _, name := range []string{"--ink", "--muted", "--accent-text", "--green", "--amber", "--red"} {
		token := lightToken(t, styles, name)
		if ratio := contrastRatio(t, token, surface); ratio < 4.5 {
			t.Errorf("light %s (%s) on %s is %.2f:1, below the 4.5:1 AA floor for normal text",
				name, token, surface, ratio)
		}
	}

	// Every status color also has to clear AA over the page canvas, because
	// status text renders directly on it in the roadmap and the header.
	canvas := lightToken(t, styles, "--canvas")
	for _, name := range []string{"--accent-text", "--green", "--amber", "--red"} {
		token := lightToken(t, styles, name)
		if ratio := contrastRatio(t, token, canvas); ratio < 4.5 {
			t.Errorf("light %s (%s) on %s is %.2f:1, below 4.5:1", name, token, canvas, ratio)
		}
	}
}

func TestHiddenAttributeBeatsComponentDisplayRules(t *testing.T) {
	styles := readAsset(t, "assets/app.css")
	if !strings.Contains(styles, "[hidden] {\n  display: none !important;\n}") {
		t.Fatal("app.css must force [hidden] to display: none; component display rules override the UA default")
	}

	// These are the elements toggled through the hidden attribute that also
	// declare their own display, which is exactly the combination that breaks.
	for _, selector := range []string{".chip {", ".next__run {", ".drawer {"} {
		_, block, found := strings.Cut(styles, selector)
		if !found {
			t.Errorf("app.css no longer defines %s", selector)
			continue
		}
		rule, _, _ := strings.Cut(block, "}")
		if !strings.Contains(rule, "display:") && !strings.Contains(rule, "position: fixed") {
			t.Errorf("%s no longer needs the [hidden] guard; drop the comment if the rule really changed", selector)
		}
	}

	script := readAsset(t, "assets/app.js")
	requireContains(t, "app.js", script, []string{
		"dom.warningCount.hidden = total === 0",
		"dom.nextRun.hidden = !command",
	})
}

func TestScriptItemIDRegexMatchesTheServerContract(t *testing.T) {
	script := readAsset(t, "assets/app.js")
	requireContains(t, "app.js", script, []string{
		"const ITEM_ID = /^w[1-9][0-9]*$/;",
		"const MAX_ITEM_ID = 32;",
		"decoded.length <= MAX_ITEM_ID && ITEM_ID.test(decoded)",
		"ITEM_ID.test(state.selected) ? `?item=${encodeURIComponent(state.selected)}` : \"\"",
	})
	if strings.Contains(script, "[A-Za-z0-9_.:-]{1,64}") {
		t.Error("app.js still accepts hash IDs the API rejects with 400")
	}

	// The client bound must be the server bound, or a stale hash poisons every poll.
	if !strings.Contains(script, strconv.Itoa(maxDetailItemLength)) {
		t.Errorf("app.js must cap item IDs at the server limit of %d", maxDetailItemLength)
	}
	for _, id := range []string{"w1", "w42", "w1000"} {
		if _, ok := normalizeDetailItem(id); !ok {
			t.Errorf("server rejects %q, so the client regex is wrong", id)
		}
	}
	for _, id := range []string{"w0", "w01", "wx", "1", "w-1", "w1.2", strings.Repeat("w1", 20)} {
		if _, ok := normalizeDetailItem(id); ok {
			t.Errorf("server accepts %q, so the client regex is too strict", id)
		}
	}
}

func TestScriptGroupsWarningsBySourceBeforeProgram(t *testing.T) {
	script := readAsset(t, "assets/app.js")
	_, body, found := strings.Cut(script, "function warningGroups()")
	if !found {
		t.Fatal("app.js no longer groups warnings")
	}
	table, _, _ := strings.Cut(body, "const seen")

	program := strings.Index(table, `["Program"`)
	projects := strings.Index(table, `["Child projects"`)
	github := strings.Index(table, `["GitHub"`)
	mailbox := strings.Index(table, `["Mailbox"`)
	if program < 0 || projects < 0 || github < 0 || mailbox < 0 {
		t.Fatalf("warningGroups no longer lists every source:\n%s", table)
	}
	for name, index := range map[string]int{"Child projects": projects, "GitHub": github, "Mailbox": mailbox} {
		if index > program {
			t.Errorf("%s must be deduplicated before Program, or the source group renders empty", name)
		}
	}
}

func TestScriptRestoresFocusAndDoesNotChurnLiveRegions(t *testing.T) {
	script := readAsset(t, "assets/app.js")
	requireContains(t, "app.js", script, []string{
		"function returnFocusTo(node)",
		"node === document.body",
		"return document.activeElement === node;",
		"if (!returnFocusTo(back)) {\n    focusSelection();\n  }",
		"function announce(node, message)",
		"if (node.textContent === message) {\n    return;\n  }",
		"announce(dom.feedState, message)",
		"announce(dom.reconnect, message)",
		"announce(dom.ledgerCount,",
	})
	if !strings.Contains(script, "function hideReconnect() {\n  if (dom.reconnect.hidden) {\n    return;\n  }") {
		t.Error("hideReconnect must no-op when already hidden instead of clearing a live region every poll")
	}

	// Every element the markup marks as a live region must be written through announce.
	index := readAsset(t, "assets/index.html")
	for _, id := range []string{"feed-state", "ledger-count", "reconnect"} {
		_, tag, found := strings.Cut(index, `id="`+id+`"`)
		if !found {
			t.Fatalf("index.html has no #%s", id)
		}
		open, _, _ := strings.Cut(tag, ">")
		if !strings.Contains(open, `aria-live="polite"`) {
			t.Errorf("#%s should stay a polite live region", id)
		}
		if strings.Contains(script, "dom."+strings.ReplaceAll(id, "-", "")+".textContent =") {
			t.Errorf("#%s is still written directly instead of through announce", id)
		}
	}
}

// cssRule returns the declarations of the first rule with the given selector.
func cssRule(t *testing.T, styles, selector string) string {
	t.Helper()
	_, after, found := strings.Cut(styles, selector+" {")
	if !found {
		t.Fatalf("app.css has no %s rule", selector)
	}
	rule, _, _ := strings.Cut(after, "}")
	return rule
}

// cssPixels reads a single px declaration out of a rule.
func cssPixels(t *testing.T, rule, property string) float64 {
	t.Helper()
	_, after, found := strings.Cut(rule, property+": ")
	if !found {
		t.Fatalf("rule has no %s:\n%s", property, rule)
	}
	value, _, _ := strings.Cut(after, ";")
	value = strings.TrimSuffix(strings.Fields(value)[0], "px")
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		t.Fatalf("parse %s %q: %v", property, value, err)
	}
	return number
}

func TestDrawerTypographyIsNotAMicroInspector(t *testing.T) {
	styles := readAsset(t, "assets/app.css")

	head := cssRule(t, styles, ".drawer__head")
	if padding := cssPixels(t, head, "padding"); padding < 28 || padding > 36 {
		t.Errorf("drawer header padding is %gpx, want 28-36px", padding)
	}
	title := cssRule(t, styles, ".drawer__head h2")
	if size := cssPixels(t, title, "font-size"); size < 24 || size > 28 {
		t.Errorf("drawer title is %gpx, want 24-28px", size)
	}
	close := cssRule(t, styles, "#drawer-close")
	if width := cssPixels(t, close, "width"); width < 40 {
		t.Errorf("close control is %gpx wide, want a 40px hit area", width)
	}
	if height := cssPixels(t, close, "height"); height < 40 {
		t.Errorf("close control is %gpx tall, want a 40px hit area", height)
	}

	body := cssRule(t, styles, ".drawer__body")
	if size := cssPixels(t, body, "font-size"); size < 16 {
		t.Errorf("drawer body text is %gpx, want at least 16px", size)
	}
	if !strings.Contains(body, "line-height: 1.6") {
		t.Errorf("drawer body needs a 1.6 line height:\n%s", body)
	}
	if !strings.Contains(body, "max-width: 1180px") || !strings.Contains(body, "margin: 0 auto") {
		t.Errorf("drawer body should be centered at a readable maximum:\n%s", body)
	}
	if padding := cssPixels(t, body, "padding"); padding < 32 || padding > 48 {
		t.Errorf("drawer body padding is %gpx, want 32-48px on desktop", padding)
	}

	section := cssRule(t, styles, ".detail-section")
	if gap := cssPixels(t, section, "padding-top"); gap < 28 {
		t.Errorf("sections are separated by %gpx, want at least 28px", gap)
	}
	if !strings.Contains(section, "border-top: 1px solid var(--border)") {
		t.Errorf("sections separate with a hairline, not a box:\n%s", section)
	}
	heading := cssRule(t, styles, ".detail-section > h3")
	if size := cssPixels(t, heading, "font-size"); size < 16 || size > 18 {
		t.Errorf("section headings are %gpx, want 16-18px", size)
	}
	if !strings.Contains(heading, "color: var(--ink)") {
		t.Errorf("section headings should be charcoal, not muted:\n%s", heading)
	}

	facts := cssRule(t, styles, ".kv")
	if size := cssPixels(t, facts, "font-size"); size < 15 {
		t.Errorf("fact values are %gpx, want at least 15px", size)
	}
	if !strings.Contains(facts, "gap: 20px") {
		t.Errorf("fact rows need a 20px gap:\n%s", facts)
	}
	if term := cssPixels(t, cssRule(t, styles, ".kv dt"), "font-size"); term != 13 {
		t.Errorf("fact terms are %gpx, want 13px", term)
	}
	// Two fact columns only once the drawer is genuinely wide.
	_, wide, found := strings.Cut(styles, "@media (min-width: 1280px)")
	if !found {
		t.Fatal("app.css should only split facts into two columns on a wide drawer")
	}
	wideBlock, _, _ := strings.Cut(wide, "\n}")
	if !strings.Contains(wideBlock, "repeat(2,") {
		t.Errorf("the wide fact grid should be two pairs:\n%s", wideBlock)
	}
	if strings.Contains(wideBlock, "repeat(3,") || strings.Contains(wideBlock, "repeat(4,") {
		t.Error("facts must never collapse into three or four tiny columns")
	}
	if !strings.Contains(styles, "overflow-wrap: anywhere") {
		t.Error("long paths and refs must be able to wrap")
	}

	phase := cssRule(t, styles, ".phase")
	if size := cssPixels(t, phase, "font-size"); size < 13 || size > 14 {
		t.Errorf("workflow phase chips are %gpx, want 13-14px", size)
	}
	if padding := cssPixels(t, phase, "padding"); padding < 10 || padding > 12 {
		t.Errorf("phase chips have %gpx padding, want 10-12px", padding)
	}
	for _, state := range []string{`.phase[data-status="done"]`, `.phase[data-current="true"]`} {
		if !strings.Contains(styles, state) {
			t.Errorf("phases need a distinct %s treatment", state)
		}
	}
	if !strings.Contains(styles, "background-color: var(--green-wash)") {
		t.Error("done phases should read differently from pending ones, not just by border")
	}

	artifact := cssRule(t, styles, ".artifact-text")
	if size := cssPixels(t, artifact, "font-size"); size < 14 {
		t.Errorf("artifact text is %gpx, want at least 14px", size)
	}
	if padding := cssPixels(t, artifact, "padding"); padding < 24 {
		t.Errorf("artifact panel padding is %gpx, want at least 24px so text does not hug the edge", padding)
	}
	if !strings.Contains(artifact, "line-height: 1.65") ||
		!strings.Contains(artifact, "max-width: 100%") ||
		!strings.Contains(artifact, "max-height: 65vh") {
		t.Errorf("artifact panel should be a comfortable reading surface:\n%s", artifact)
	}

	// .reason-list shares this rule, so checking .note-list covers both.
	for _, selector := range []string{".note-list", ".decision__question", ".decision__options"} {
		if size := cssPixels(t, cssRule(t, styles, selector), "font-size"); size < 15 {
			t.Errorf("%s is %gpx, want at least 15px", selector, size)
		}
	}
	decision := cssRule(t, styles, ".decision")
	if padding := cssPixels(t, decision, "padding"); padding < 18 || padding > 20 {
		t.Errorf("decision cards have %gpx padding, want 18-20px", padding)
	}
	if !strings.Contains(styles, ".decision--open {\n  border-left-color: var(--accent);") {
		t.Error("open decisions should carry the warm accent strip")
	}

	// Warnings and blockers are quiet warm areas, not shouting boxes.
	note := cssRule(t, styles, ".detail-note")
	if !strings.Contains(note, "background-color: var(--amber-wash)") {
		t.Errorf("warnings should sit on a warm amber area:\n%s", note)
	}
	if !strings.Contains(styles, ".detail-section--blocking .detail-note {\n  background-color: var(--red-wash);") {
		t.Error("only blocking sections escalate to the red area")
	}
}

func TestDrawerSectionBarIsWarmAndComfortable(t *testing.T) {
	styles := readAsset(t, "assets/app.css")
	nav := cssRule(t, styles, ".drawer__nav")
	if padding := cssPixels(t, nav, "padding"); padding != 0 {
		t.Errorf("the section bar should have no vertical padding, got %g", padding)
	}
	if !strings.Contains(nav, "padding: 0 40px") {
		t.Errorf("section bar padding must line up with the body:\n%s", nav)
	}
	if !strings.Contains(nav, "background-color: var(--surface)") {
		t.Errorf("the sticky bar needs a warm opaque surface:\n%s", nav)
	}
	if !strings.Contains(nav, "box-shadow: var(--lift-nav)") {
		t.Errorf("the sticky bar needs a subtle shadow under its hairline:\n%s", nav)
	}

	tab := cssRule(t, styles, ".drawer__tab")
	if size := cssPixels(t, tab, "font-size"); size != 14 {
		t.Errorf("section tab labels are %gpx, want 14px", size)
	}
	if gap := cssPixels(t, tab, "margin-right"); gap < 20 || gap > 32 {
		t.Errorf("section tabs are spaced %gpx apart, want 20-32px", gap)
	}
	if !strings.Contains(styles, `.drawer__tab[aria-current="true"] {`) ||
		!strings.Contains(styles, "border-bottom-color: var(--accent)") {
		t.Error("the active section needs a coral underline")
	}
}

func TestWarmPaletteReachesTheWholeInterface(t *testing.T) {
	styles := readAsset(t, "assets/app.css")

	// Selection, emphasis and the roadmap all speak clay, never electric blue.
	for selector, want := range map[string]string{
		".card[data-selected=\"true\"]": "border-color: var(--accent)",
		".next":                         "border-left: 3px solid var(--accent)",
		".next__label":                  "color: var(--accent-text)",
		".card__pr":                     "color: var(--accent-text)",
		".edge":                         "stroke: var(--border-strong)",
		".edge--active":                 "stroke: var(--accent)",
		"a":                             "color: var(--accent-text)",
	} {
		if !strings.Contains(cssRule(t, styles, selector), want) {
			t.Errorf("%s should use the warm accent (%s)", selector, want)
		}
	}
	if !strings.Contains(cssRule(t, styles, "tbody tr[aria-selected=\"true\"]"), "background-color: var(--accent-wash)") {
		t.Error("selected table rows should use the warm accent wash")
	}

	card := cssRule(t, styles, "\n.card")
	if size := cssPixels(t, cssRule(t, styles, ".card__title"), "font-size"); size != 16 {
		t.Errorf("roadmap card titles are %gpx, want 16px", size)
	}
	if padding := cssPixels(t, card, "padding"); padding < 16 || padding > 18 {
		t.Errorf("roadmap cards have %gpx padding, want 16-18px", padding)
	}
	if !strings.Contains(card, "border-radius: 12px") {
		t.Errorf("roadmap cards should use a 12px radius:\n%s", card)
	}

	// The dark theme is warm charcoal, not blue-black.
	_, dark, _ := strings.Cut(strings.ToLower(styles), `[data-theme="dark"]`)
	darkBlock, _, _ := strings.Cut(dark, "\n}")
	for _, want := range []string{"--canvas: #1a1817", "--surface: #232020", "--ink: #f0ece5", "--accent: #e2886a"} {
		if !strings.Contains(darkBlock, want) {
			t.Errorf("dark theme is missing %q:\n%s", want, darkBlock)
		}
	}
}

func TestStylesDropBlueprintChromeAndBroadMotion(t *testing.T) {
	styles := readAsset(t, "assets/app.css")
	requireAbsent(t, "app.css", styles, []string{
		"transition: all",
		"@keyframes",
		"trace-drift",
		"animation:",
		"scale(0)",
		"scroll-behavior: smooth",
		"ease-in ",
		"linear-gradient(to right, rgba",
		"blueprint",
	})

	script := readAsset(t, "assets/app.js")
	requireAbsent(t, "app.js", script, []string{
		`behavior: "smooth"`,
		"requestAnimationFrame",
	})
}

func TestStylesUseExactPropertyTransitionsUnderThreeHundredMilliseconds(t *testing.T) {
	styles := readAsset(t, "assets/app.css")
	requireContains(t, "app.css", styles, []string{
		"--ease-out: cubic-bezier(0.23, 1, 0.32, 1);",
		"transform: scale(0.98);",
		"transition:\n    opacity var(--panel) var(--ease-out),\n    transform var(--panel) var(--ease-out);",
	})

	for _, selector := range []string{".button:active", ".chip:active", ".segment:active", ".card:active"} {
		if !strings.Contains(styles, selector) {
			t.Errorf("app.css must give %s pressed feedback", selector)
		}
	}

	durations := regexp.MustCompile(`(\d+)ms`).FindAllStringSubmatch(styles, -1)
	if len(durations) == 0 {
		t.Fatal("app.css declares no transition durations")
	}
	for _, match := range durations {
		value, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatal(err)
		}
		if value > 300 {
			t.Errorf("app.css uses a %dms duration; UI motion must stay under 300ms", value)
		}
	}
}

func TestStylesGateHoverStayResponsiveAndRespectReducedMotion(t *testing.T) {
	styles := readAsset(t, "assets/app.css")
	requireContains(t, "app.css", styles, []string{
		"@media (hover: hover) and (pointer: fine)",
		"@media (max-width: 960px)",
		"@media (max-width: 560px)",
		"@media (prefers-reduced-motion: reduce)",
	})

	hoverStart := strings.Index(styles, "@media (hover: hover) and (pointer: fine)")
	if strings.Count(styles[:hoverStart], ":hover") != 0 {
		t.Error("hover rules must sit behind the fine-pointer media query")
	}

	_, mobile, foundMobile := strings.Cut(styles, "@media (max-width: 560px)")
	if !foundMobile {
		t.Fatal("app.css lacks a small-viewport block")
	}
	if !strings.Contains(mobile, ".drawer__panel") || !strings.Contains(mobile, "width: 100%;") {
		t.Error("the drawer must go full screen on small viewports")
	}

	_, reduced, found := strings.Cut(styles, "@media (prefers-reduced-motion: reduce)")
	if !found {
		t.Fatal("app.css lacks a reduced-motion block")
	}
	if !strings.Contains(reduced, ".drawer__panel") || !strings.Contains(reduced, "transform: none") {
		t.Errorf("reduced motion must drop drawer movement:\n%s", reduced)
	}
	if !strings.Contains(reduced, ".card:active") {
		t.Error("reduced motion must drop the pressed-scale transforms")
	}
}

func TestScriptKeepsPollingSelectionAndLinkSafety(t *testing.T) {
	script := readAsset(t, "assets/app.js")
	requireContains(t, "app.js", script, []string{
		"/api/program",
		"AbortController",
		"const BACKOFF = [3000, 6000, 12000]",
		"const POLL_INTERVAL = 3000",
		"setTimeout(",
		"createElement",
		"textContent",
		`rel", "noopener noreferrer"`,
		`protocol === "https:"`,
		"Pull request · stale GitHub cache",
		"Stale since",
		"pr.stale",
		"function writeHash()",
		"function readHash()",
		"function matchesFilter(",
		`event.key === "j"`,
		`event.key === "k"`,
		`event.key === "ArrowDown"`,
		`event.key === "ArrowUp"`,
	})

	if !strings.Contains(script, `event.key === "/" && state.tab === "tasks"`) {
		t.Error("the filter shortcut must only fire on the Tasks tab")
	}
	if !strings.Contains(script, `event.key === "g" && state.tab === "roadmap"`) {
		t.Error("the g shortcut must only fire on the Roadmap tab")
	}
	if !strings.Contains(script, `card.scrollIntoView({ block: "nearest", inline: "nearest" })`) {
		t.Error("revealing a roadmap card must be an instant jump, not a smooth scroll")
	}
}

func TestScriptDerivesTheDisplayTitleAndDeduplicatesWarnings(t *testing.T) {
	script := readAsset(t, "assets/app.js")
	requireContains(t, "app.js", script, []string{
		"program.display_title",
		"text(program.summary)",
		"function warningGroups()",
		"seen.has(message)",
		"function renderWarningCount()",
		`["Child projects", list(health.projects && health.projects.warnings)]`,
	})
	if !strings.Contains(script, `dom.warningCount.hidden = total === 0`) {
		t.Error("the header warning chip must stay hidden when there is nothing to report")
	}
	if strings.Contains(script, "text(program.title, \"Untitled program\");\n  document.title") {
		t.Error("the raw program title must not become the page heading")
	}

	index := readAsset(t, "assets/index.html")
	requireContains(t, "index.html", index, []string{
		`id="program-summary"`,
		`id="warning-count"`,
		`<details id="diagnostics" class="disclosure">`,
	})
	if strings.Index(index, `id="warnings"`) < strings.Index(index, `id="diagnostics"`) {
		t.Error("warnings must live inside the collapsed Diagnostics disclosure")
	}
}
