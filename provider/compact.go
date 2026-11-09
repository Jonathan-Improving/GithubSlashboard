package provider

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// botAuthorSuffix marks a GitHub App account. Boilerplate removal that would be
// unsafe on a person's words (their collapsible sections, their link targets)
// applies to these authors only.
const botAuthorSuffix = "[bot]"

// boilerplateSections lists the collapsible-section titles that review bots
// wrap around reference material the status judgment never needs: a restated
// walkthrough of the change, a settings echo, a copy-paste prompt for another
// AI agent, a share widget. A matching section keeps its title line (which can
// itself carry a signal, such as "Pre-merge checks | 4 passed | 1 failed") and
// loses its body. Matching is a prefix test on the lowercased title with any
// leading symbols removed.
var boilerplateSections = []string{
	"walkthrough",
	"pre-merge checks",
	"full details",
	"share",
	"review info",
	"recent review info",
	"run configuration",
	"files selected for processing",
	"files ignored due to path filters",
	"commits",
	"additional details and impacted files",
	"diagram",
	"prompt for ai agents",
	"prompt for all review comments",
	"prompt to fix",
	"additional context",
	"tips",
}

// botBoilerplateLines lists line prefixes that review bots repeat on every post
// outside any collapsible section: thanks, upsell and autofix checkboxes.
var botBoilerplateLines = []string{
	"thanks for using",
	"- [ ] **autofix**",
	"- [ ] fix comments",
	"- [ ] <!--",
	"- [ ] \U0001FA84 fix comments",
	"---",
	"**included review availability",
	"prompt to fix review comments",
	"\U0001F916 prompt to fix review comments",
	"\U0001F916 prompt for ai agents",
	"prompt for ai agents",
	"\U0001F3AF customize your risk assessments",
	"\u2705 automatic approvals",
}

var (
	// markupPattern recognises a body that carries HTML worth converting: a
	// comment, or a tag from the set GitHub renders and review bots emit.
	markupPattern = regexp.MustCompile(`(?i)<!--|</?(?:details|summary|picture|source|img|a|br|p|div|span|table|thead|tbody|tr|td|th|blockquote|sup|sub|b|i|strong|em|code|pre|ul|ol|li|h[1-6]|hr|dl|dd|dt|svg|kbd)\b[^>]*>`)

	markdownImage = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	markdownLink  = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	tableDivider  = regexp.MustCompile(`^\|?(\s*:?-{2,}:?\s*\|)+\s*:?-*:?\s*\|?$`)
	blankRun      = regexp.MustCompile(`\n{3,}`)

	// botEntities decodes the entities left in a bot's text where it nested
	// Markdown inside an HTML element, so a quote marker or ampersand arrives
	// encoded.
	botEntities = strings.NewReplacer("&gt;", ">", "&lt;", "<", "&amp;", "&")
	codeFence   = "```"
)

// botMarkerRegions are the comment-delimited regions a review bot fills with
// announcements and tips on every post. Removing the region by its delimiters is
// exact where matching its prose would not be.
var botMarkerRegions = []*regexp.Regexp{
	regexp.MustCompile(`(?s)<!-- announcements_start -->.*?<!-- announcements_end -->`),
	regexp.MustCompile(`(?s)<!-- tips_start -->.*?(?:<!-- tips_end -->|$)`),
	regexp.MustCompile(`(?s)<!-- autopilot:start -->.*?<!-- autopilot:end -->`),
}

// quotedHTMLLine matches a line that is a Markdown quote around an HTML tag, such
// as "> <details>". The HTML parser would read the quote marker as text and keep
// the section it wraps, so the marker is removed before parsing.
var quotedHTMLLine = regexp.MustCompile(`(?m)^(?:>\s?)+(\s*</?[a-zA-Z])`)

// htmlConverter turns parsed HTML into Markdown. Escaping is off because the
// input is GitHub-flavoured Markdown with inline HTML, not a web page: escaping
// would turn the Markdown already present into backslash noise. The converter
// is safe for concurrent use, so classification workers share it.
var htmlConverter = converter.NewConverter(
	converter.WithEscapeMode(converter.EscapeModeDisabled),
	converter.WithPlugins(base.NewBasePlugin(), commonmark.NewCommonmarkPlugin(), table.NewTablePlugin()),
)

// CompactEvents returns a copy of events whose comment and review text has had
// its HTML converted to Markdown and its wasteful content removed, so the model
// is not charged for markup, tracking links and bot boilerplate. Other event
// kinds are small, structured and passed through. The input slice is never
// modified: the same trail still feeds the unchanged-input fingerprint
// untouched. Text is never truncated; only content with no bearing on status is
// removed.
func CompactEvents(events []model.Event) []model.Event {
	out := make([]model.Event, len(events))
	copy(out, events)
	for i := range out {
		if out[i].Kind != model.EventComment && out[i].Kind != model.EventReview {
			continue
		}
		out[i].Text = compactText(out[i].Text, strings.HasSuffix(out[i].Author, botAuthorSuffix))
	}
	return out
}

// compactText cleans one comment or review body. Any conversion failure leaves
// the text as GitHub sent it.
func compactText(text string, bot bool) string {
	if text == "" {
		return text
	}
	if markupPattern.MatchString(text) && (bot || !strings.Contains(text, codeFence)) {
		if md, ok := convertHTML(text, bot); ok {
			text = md
		}
	}
	return stripWaste(text, bot)
}

// convertHTML parses text as HTML, drops decorative and boilerplate nodes, and
// renders the rest as Markdown.
func convertHTML(text string, bot bool) (string, bool) {
	if bot {
		for _, region := range botMarkerRegions {
			text = region.ReplaceAllString(text, "")
		}
		text = quotedHTMLLine.ReplaceAllString(text, "$1")
	}
	doc, err := xhtml.Parse(strings.NewReader(text))
	if err != nil {
		return "", false
	}
	pruneNode(doc, bot)
	md, err := htmlConverter.ConvertNode(doc)
	if err != nil {
		return "", false
	}
	return string(md), true
}

// pruneNode removes, beneath n, everything the model gains nothing from:
// comments, images and scripts always; a bot's boilerplate sections down to
// their title.
func pruneNode(n *xhtml.Node, bot bool) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		switch {
		case isDecoration(c):
			n.RemoveChild(c)
		case bot && c.Type == xhtml.ElementNode && c.DataAtom == atom.Details:
			if title, ok := boilerplateTitle(c); ok {
				n.InsertBefore(titleParagraph(title), c)
				n.RemoveChild(c)
			} else {
				pruneNode(c, bot)
			}
		default:
			pruneNode(c, bot)
		}
		c = next
	}
}

func isDecoration(n *xhtml.Node) bool {
	if n.Type == xhtml.CommentNode {
		return true
	}
	if n.Type != xhtml.ElementNode {
		return false
	}
	switch n.DataAtom {
	case atom.Img, atom.Picture, atom.Source, atom.Svg, atom.Script, atom.Style:
		return true
	}
	return false
}

// boilerplateTitle reports whether details is a boilerplate section and returns
// its title text.
func boilerplateTitle(details *xhtml.Node) (string, bool) {
	for s := details.FirstChild; s != nil; s = s.NextSibling {
		if s.Type != xhtml.ElementNode || s.DataAtom != atom.Summary {
			continue
		}
		title := strings.Join(strings.Fields(nodeText(s)), " ")
		key := strings.ToLower(strings.TrimLeftFunc(title, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}))
		for _, prefix := range boilerplateSections {
			if strings.HasPrefix(key, prefix) {
				return title, true
			}
		}
		return "", false
	}
	return "", false
}

func nodeText(n *xhtml.Node) string {
	var b strings.Builder
	var walk func(*xhtml.Node)
	walk = func(m *xhtml.Node) {
		if m.Type == xhtml.TextNode {
			b.WriteString(m.Data)
		}
		for c := m.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func titleParagraph(title string) *xhtml.Node {
	p := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.P, Data: "p"}
	p.AppendChild(&xhtml.Node{Type: xhtml.TextNode, Data: title})
	return p
}

// stripWaste removes Markdown-level waste that survives conversion: image
// syntax, table dividers, empty quote lines, blank-line runs and, for bots,
// link targets and repeated boilerplate lines.
func stripWaste(text string, bot bool) string {
	if bot {
		text = botEntities.Replace(text)
	}
	text = markdownImage.ReplaceAllString(text, "$1")
	if bot {
		text = markdownLink.ReplaceAllString(text, "$1")
	}
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && tableDivider.MatchString(trimmed) {
			continue
		}
		if trimmed != "" && strings.Trim(trimmed, "> ") == "" {
			continue
		}
		if bot && hasBoilerplatePrefix(trimmed) {
			continue
		}
		kept = append(kept, line)
	}
	text = strings.Join(kept, "\n")
	text = blankRun.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

func hasBoilerplatePrefix(line string) bool {
	lower := strings.ToLower(line)
	for _, prefix := range botBoilerplateLines {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}
