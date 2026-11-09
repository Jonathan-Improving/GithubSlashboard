package provider

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

func comment(author, text string) model.Event {
	return model.Event{Timestamp: time.Unix(0, 0), Author: author, Kind: model.EventComment, Text: text}
}

func compactOne(author string, kind model.EventKind, text string) string {
	return CompactEvents([]model.Event{{Author: author, Kind: kind, Text: text}})[0].Text
}

func TestCompactEventsDoesNotModifyInput(t *testing.T) {
	in := []model.Event{comment("coderabbitai[bot]", "<!-- hidden --><p>hello</p>")}
	want := in[0].Text
	out := CompactEvents(in)
	if in[0].Text != want {
		t.Errorf("input mutated: %q", in[0].Text)
	}
	if out[0].Text == want {
		t.Errorf("output was not compacted: %q", out[0].Text)
	}
}

func TestCompactEventsKeepsOrderAuthorAndKind(t *testing.T) {
	ts := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	in := []model.Event{
		{Timestamp: ts, Author: "alice", Kind: model.EventReview, RoleHint: "reviewer", Text: "approved: ok"},
		{Timestamp: ts.Add(time.Hour), Author: "ci", Kind: model.EventCIStatus, Text: "failing: build"},
	}
	out := CompactEvents(in)
	if len(out) != 2 {
		t.Fatalf("len = %d", len(out))
	}
	for i := range in {
		if out[i].Timestamp != in[i].Timestamp || out[i].Author != in[i].Author ||
			out[i].Kind != in[i].Kind || out[i].RoleHint != in[i].RoleHint {
			t.Errorf("event %d metadata changed: %+v vs %+v", i, out[i], in[i])
		}
	}
}

func TestCompactEventsLeavesNonConversationKindsAlone(t *testing.T) {
	text := "<b>failing</b> <!-- x --> check"
	out := CompactEvents([]model.Event{{Author: "ci", Kind: model.EventCIStatus, Text: text}})
	if out[0].Text != text {
		t.Errorf("ci_status text changed: %q", out[0].Text)
	}
}

func TestCompactHandlesNilAndEmpty(t *testing.T) {
	if got := CompactEvents(nil); len(got) != 0 {
		t.Errorf("nil trail gave %d events", len(got))
	}
	if got := compactOne("alice", model.EventComment, ""); got != "" {
		t.Errorf("empty text became %q", got)
	}
}

func TestCompactNeverTruncates(t *testing.T) {
	long := strings.Repeat("a plain sentence about the change. ", 2000)
	if got := compactOne("alice", model.EventComment, long); got != strings.TrimSpace(long) {
		t.Errorf("plain text of %d chars came back as %d chars", len(long), len(got))
	}
}

func TestCompactPlainMarkdownFromAPersonIsUnchanged(t *testing.T) {
	text := "Looks good, but please check `foo(a, b)` and see [the docs](https://example.com/docs).\n\n- one\n- two"
	if got := compactOne("alice", model.EventComment, text); got != text {
		t.Errorf("plain markdown changed:\n got %q\nwant %q", got, text)
	}
}

func TestCompactConvertsHTMLToMarkdown(t *testing.T) {
	got := compactOne("alice", model.EventComment, "<p>Please <strong>fix</strong> the <code>nil</code> check.</p><ul><li>one</li><li>two</li></ul>")
	for _, want := range []string{"**fix**", "`nil`", "one", "two"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "<") {
		t.Errorf("tags remain: %q", got)
	}
}

func TestCompactDoesNotEscapeExistingMarkdown(t *testing.T) {
	got := compactOne("alice", model.EventComment, "<p>**Note:** use `a_b` here</p>")
	if strings.Contains(got, `\`) {
		t.Errorf("escaping added backslashes: %q", got)
	}
}

func TestCompactDropsHTMLCommentsAndImages(t *testing.T) {
	text := `<!-- generated -->Fix needed <picture><source srcset="https://x/dark.svg"><img src="https://x/a.svg" alt="icon"></picture> here ![badge](https://x/b.svg)`
	got := compactOne("alice", model.EventComment, text)
	for _, bad := range []string{"generated", "svg", "http", "<img", "!["} {
		if strings.Contains(got, bad) {
			t.Errorf("%q survived in %q", bad, got)
		}
	}
	if !strings.Contains(got, "Fix needed") {
		t.Errorf("content lost: %q", got)
	}
}

func TestCompactDropsTableDividerRows(t *testing.T) {
	text := "| Check | Status |\n| :--- | :---: |\n| build | pass |"
	got := compactOne("alice", model.EventComment, text)
	if strings.Contains(got, ":---") {
		t.Errorf("divider row survived: %q", got)
	}
	if !strings.Contains(got, "build") || !strings.Contains(got, "pass") {
		t.Errorf("table content lost: %q", got)
	}
}

func TestCompactCollapsesBlankRuns(t *testing.T) {
	got := compactOne("alice", model.EventComment, "one\n\n\n\n\ntwo")
	if got != "one\n\ntwo" {
		t.Errorf("got %q", got)
	}
}

func TestCompactBotBoilerplateSectionKeepsTitleDropsBody(t *testing.T) {
	text := `<details><summary>Pre-merge checks | ✅ 4 | ❌ 1</summary><p>long explanation of every check</p></details>` +
		`<details><summary>Walkthrough</summary><p>a restated walkthrough of the change</p></details>` +
		`<p>Actionable comments posted: 2</p>`
	got := compactOne("coderabbitai[bot]", model.EventReview, text)
	for _, want := range []string{"Pre-merge checks | ✅ 4 | ❌ 1", "Walkthrough", "Actionable comments posted: 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	for _, bad := range []string{"long explanation", "restated walkthrough"} {
		if strings.Contains(got, bad) {
			t.Errorf("boilerplate body %q survived in %q", bad, got)
		}
	}
}

func TestCompactBotSectionTitleMatchIgnoresLeadingSymbols(t *testing.T) {
	text := `<details><summary>🤖 Prompt for AI Agents</summary><pre>copy-paste prompt body</pre></details>`
	got := compactOne("coderabbitai[bot]", model.EventReview, text)
	if strings.Contains(got, "copy-paste prompt body") {
		t.Errorf("emoji-prefixed boilerplate body survived: %q", got)
	}
}

func TestCompactBotFindingSectionIsKept(t *testing.T) {
	text := `<details><summary>Nitpick comments (1)</summary><p>Rename this variable, it shadows the outer one.</p></details>`
	got := compactOne("coderabbitai[bot]", model.EventReview, text)
	if !strings.Contains(got, "Rename this variable") {
		t.Errorf("a finding was dropped: %q", got)
	}
}

func TestCompactPersonsCollapsibleSectionIsKept(t *testing.T) {
	text := `<details><summary>Walkthrough</summary><p>my own notes on the walkthrough</p></details>`
	got := compactOne("alice", model.EventComment, text)
	if !strings.Contains(got, "my own notes") {
		t.Errorf("a person's section body was dropped: %q", got)
	}
}

func TestCompactBotLinkTargetsDroppedPersonLinksKept(t *testing.T) {
	text := "See [the report](https://example.com/report?utm_source=bot) for details."
	if got := compactOne("codecov[bot]", model.EventComment, text); strings.Contains(got, "example.com") || !strings.Contains(got, "the report") {
		t.Errorf("bot link not reduced to its text: %q", got)
	}
	if got := compactOne("alice", model.EventComment, text); !strings.Contains(got, "https://example.com/report") {
		t.Errorf("person's link target lost: %q", got)
	}
}

func TestCompactBotQuotedDetailsInAlertAreRemoved(t *testing.T) {
	text := "Real finding.\n\n> [!TIP]\n> <details>\n> <summary>Share</summary>\n> promotional text\n> </details>\n"
	got := compactOne("coderabbitai[bot]", model.EventComment, text)
	if strings.Contains(got, "promotional text") {
		t.Errorf("quoted boilerplate survived: %q", got)
	}
	if !strings.Contains(got, "Real finding.") {
		t.Errorf("content lost: %q", got)
	}
}

func TestCompactBotMarkerRegionsAreRemoved(t *testing.T) {
	text := "<p>Review done.</p><!-- announcements_start --><p>Try our new feature!</p><!-- announcements_end -->"
	got := compactOne("coderabbitai[bot]", model.EventComment, text)
	if strings.Contains(got, "new feature") {
		t.Errorf("announcement survived: %q", got)
	}
	if !strings.Contains(got, "Review done.") {
		t.Errorf("content lost: %q", got)
	}
}

func TestCompactHumanTextWithCodeFenceIsNotParsedAsHTML(t *testing.T) {
	text := "Use this:\n```html\n<div class=\"x\">hi</div>\n```\n"
	got := compactOne("alice", model.EventComment, text)
	if !strings.Contains(got, `<div class="x">hi</div>`) {
		t.Errorf("code sample corrupted: %q", got)
	}
}

func TestCompactReviewKeepsStatePrefix(t *testing.T) {
	got := compactOne("alice", model.EventReview, "changes_requested: <p>please add a <b>test</b></p>")
	if !strings.HasPrefix(got, "changes_requested:") || !strings.Contains(got, "**test**") {
		t.Errorf("got %q", got)
	}
}

func TestCompactIsSafeForConcurrentUse(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if got := compactOne("coderabbitai[bot]", model.EventComment, "<p>hello <b>world</b></p>"); !strings.Contains(got, "**world**") {
					t.Errorf("got %q", got)
					return
				}
			}
		}()
	}
	wg.Wait()
}
