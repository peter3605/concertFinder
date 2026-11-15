package email

import (
	"fmt"
	"html"
	"strings"

	"github.com/peterho/concertfinder/internal/concerts"
)

// Audience is who a notification email goes to and where its links point.
type Audience struct {
	Email string
	// Spotify is whether the recipient has a Spotify identity. Only then does
	// the footer say "Powered by Spotify": docs/public-path.md §7 puts the
	// attribution where Spotify-derived data appears, and since CF-27 a
	// picks- or genre-only account has none.
	Spotify bool
	// SiteURL is the site's origin, e.g. https://concertfinder.app. The feed,
	// the email preferences page and the logo hang off it.
	SiteURL string
	// UnsubscribeURL is the one-click opt-out from every email; see
	// db.OptOutAllEmail.
	UnsubscribeURL string
}

func (a Audience) feedURL() string  { return strings.TrimRight(a.SiteURL, "/") + "/" }
func (a Audience) prefsURL() string { return strings.TrimRight(a.SiteURL, "/") + "/settings" }

// logoURL is the app icon the site already serves. It must be absolute:
// a mail client has no base URL to resolve a relative one against.
func (a Audience) logoURL() string { return strings.TrimRight(a.SiteURL, "/") + "/icon-512.png" }

// RenderDigest builds the weekly digest from net-new concerts.
//
// Concerts are folded into events first, exactly as the web list does: a
// festival that matched six of the user's artists is six Concert rows sharing
// a date, venue and city, and is one night out. Grouping is presentational
// only -- the caller's net-new bookkeeping stays keyed on dedup_key, one per
// (artist, show), so an act added to a bill the user was already emailed
// about still mails on its own.
//
// moreEvents is what CapDigest held back: those shows are not listed, the
// subject and intro count them, and the body links to the feed for the rest.
//
// chosen, when non-nil, splits the listing into the user's own artists
// (concerts.ViewerMatches.Chosen) and genre matches, in that order, as two
// sections (CF-B26). Without the split the month grouping's date sort would
// interleave them again and undo the order the caller capped by.
//
// Each act's Reason, when the caller has tagged one (ViewerMatches.Tag), is
// the card's one-line "why".
func RenderDigest(to Audience, newConcerts []concerts.Concert, moreEvents int, chosen func(concerts.Concert) bool) Message {
	events := concerts.GroupEvents(newConcerts)
	total := len(events) + moreEvents
	sections := digestSections(newConcerts, events, chosen)
	lead := sectionOrder(sections)

	subject := digestSubject(lead, total)
	intro := fmt.Sprintf("%d new show%s near you since your last digest.", total, plural(total))
	l := layout{
		to:        to,
		subject:   subject,
		preheader: preheader(lead, total),
		title:     "New shows near you",
		intro:     intro,
		sections:  sections,
		more:      moreEvents,
		footnote:  "You get this email once a week because the weekly digest is on.",
	}
	return Message{To: to.Email, Subject: subject, Text: l.text(), HTML: l.html(), UnsubscribeURL: to.UnsubscribeURL}
}

// RenderInstantNotify composes an email for one or more newly discovered
// concerts by artists the user subscribed to. It shares the digest's layout
// but keeps its own subject and intro, so recipients can tell the two apart
// at a glance and set inbox rules on them.
//
// Grouping is safe here even though the email is per subscription: the caller
// has already narrowed fresh to subscribed artists only, so a merged entry can
// only ever name artists the user asked about -- it never reveals the rest of
// the bill. Without the fold, three subscriptions turning up at one festival
// announcement mail "3 new shows" for one night.
//
// fresh must be non-empty; SendInstantNotifyWorker returns early when there is
// nothing to send.
func RenderInstantNotify(to Audience, fresh []concerts.Concert) Message {
	events := concerts.GroupEvents(fresh)
	subject := fmt.Sprintf("New show: %s", actsLine(events[0].Acts))
	intro := fmt.Sprintf("A new show just landed for %s, %s you're following.",
		actsLine(events[0].Acts), followingNoun(len(events[0].Acts)))
	if len(events) > 1 {
		subject = fmt.Sprintf("%d new shows for artists you follow", len(events))
		intro = fmt.Sprintf("%d new shows just landed for artists you follow.", len(events))
	}
	l := layout{
		to:        to,
		subject:   subject,
		preheader: preheader(events, len(events)),
		title:     "New show alert",
		intro:     intro,
		sections:  []digestSection{{months: groupByMonth(events)}},
		footnote:  "You get these emails because instant alerts are on for artists you follow.",
	}
	return Message{To: to.Email, Subject: subject, Text: l.text(), HTML: l.html(), UnsubscribeURL: to.UnsubscribeURL}
}

// followingNoun keeps the single-event intro grammatical when one show turns
// out to carry several of the user's subscriptions.
func followingNoun(acts int) string {
	if acts == 1 {
		return "an artist"
	}
	return "artists"
}

// sectionOrder flattens sections into the order the body lists them, which
// is the order the subject and preheader should name them in: the user's own
// artists before genre matches.
func sectionOrder(sections []digestSection) []concerts.Event {
	var out []concerts.Event
	for _, s := range sections {
		for _, g := range s.months {
			out = append(out, g.items...)
		}
	}
	return out
}

// headliner is the name an event is announced under in a subject line: its
// first act. A festival's whole matched lineup belongs in the body.
func headliner(e concerts.Event) string {
	if len(e.Acts) == 0 {
		return e.Venue
	}
	return e.Acts[0].Artist.Name
}

// digestSubject names the leading acts rather than only counting:
// "Doja Cat, Common People and 18 more near you". A count alone says nothing
// about whether the email is worth opening.
func digestSubject(lead []concerts.Event, total int) string {
	switch {
	case len(lead) == 0:
		return fmt.Sprintf("%d new show%s near you", total, plural(total))
	case total == 1:
		return headliner(lead[0]) + " is playing near you"
	case len(lead) == 1:
		return fmt.Sprintf("%s and %d more near you", headliner(lead[0]), total-1)
	case total == 2:
		return fmt.Sprintf("%s and %s near you", headliner(lead[0]), headliner(lead[1]))
	default:
		return fmt.Sprintf("%s, %s and %d more near you", headliner(lead[0]), headliner(lead[1]), total-2)
	}
}

// preheaderNames is how many acts the hidden preview line names.
const preheaderNames = 4

// preheader is the inbox preview line, which clients otherwise fill with
// whatever text comes first in the body (the logo's alt text, then the
// title). It names a few more acts than the subject has room for.
func preheader(lead []concerts.Event, total int) string {
	names := make([]string, 0, preheaderNames)
	for _, e := range lead {
		if len(names) == preheaderNames {
			break
		}
		names = append(names, headliner(e))
	}
	rest := total - len(names)
	var list string
	switch {
	case len(names) == 0:
		return ""
	case rest > 0:
		list = strings.Join(names, ", ") + fmt.Sprintf(" and %d more", rest)
	case len(names) == 1:
		list = names[0]
	default:
		list = strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
	if total == 1 {
		e := lead[0]
		return fmt.Sprintf("%s at %s, %s", list, e.Venue, e.LocalDayTime().Format("Mon Jan 2"))
	}
	return list
}

// sourceLabels names ticket sources for people, mirroring SOURCE_LABELS in
// web/src/lib/types.ts. Bandsintown is no longer produced but stored rows
// still carry its links until the janitor ages them out.
var sourceLabels = map[concerts.Source]string{
	concerts.SourceTicketmaster: "Ticketmaster",
	concerts.SourceOfficial:     "Official site",
	concerts.SourceSongkick:     "Songkick",
	concerts.SourceBandsintown:  "Bandsintown",
}

func sourceLabel(s concerts.Source) string {
	if l, ok := sourceLabels[s]; ok {
		return l
	}
	if s == "" {
		return "Tickets"
	}
	return string(s)
}

// ticketLinks picks the one link the "Get tickets" button opens and the
// other sources worth naming beside it. Links arrive in priority order
// (concerts.SortLinks), so the first is the button. Each further source is
// offered once, under its name: GroupEvents unions links by URL, and an
// event commonly carries two distinct Ticketmaster URLs, which the old
// digest printed as "ticketmaster ticketmaster".
func ticketLinks(links []concerts.TicketLink) (primary concerts.TicketLink, others []concerts.TicketLink, ok bool) {
	if len(links) == 0 {
		return concerts.TicketLink{}, nil, false
	}
	primary = links[0]
	seen := map[concerts.Source]bool{primary.Source: true}
	for _, l := range links[1:] {
		if seen[l.Source] {
			continue
		}
		seen[l.Source] = true
		others = append(others, l)
	}
	return primary, others, true
}

// reasonLine is the card's one-line "why": the act's own Reason for a
// single act, and the first act that has one, named, for a bill. Empty when
// no act carries one, which is the ordinary case for a genre match.
func reasonLine(acts []concerts.Act) string {
	for _, a := range acts {
		if a.Reason == "" {
			continue
		}
		if len(acts) == 1 {
			return a.Reason
		}
		return a.Artist.Name + ": " + a.Reason
	}
	return ""
}

func stateSuffix(state string) string {
	if state == "" {
		return ""
	}
	return ", " + state
}

func place(e concerts.Event) string {
	return e.Venue + " · " + e.City + stateSuffix(e.State)
}

// layout is the one template both notification emails render through.
type layout struct {
	to        Audience
	subject   string
	preheader string
	title     string
	intro     string
	sections  []digestSection
	more      int
	footnote  string
}

func (l layout) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s\n\n", l.title, l.intro)
	for _, sec := range l.sections {
		if sec.title != "" {
			fmt.Fprintf(&b, "### %s ###\n\n", sec.title)
		}
		for _, g := range sec.months {
			fmt.Fprintf(&b, "== %s ==\n\n", g.label)
			for _, e := range g.items {
				fmt.Fprintf(&b, "%s — %s\n", actsLine(e.Acts), e.LocalDayTime().Format("Mon Jan 2"))
				if e.Name != "" {
					fmt.Fprintf(&b, "  %s\n", e.Name)
				}
				fmt.Fprintf(&b, "  %s\n", place(e))
				if r := reasonLine(e.Acts); r != "" {
					fmt.Fprintf(&b, "  %s\n", r)
				}
				if primary, others, ok := ticketLinks(e.Links); ok {
					fmt.Fprintf(&b, "  Get tickets: %s\n", primary.URL)
					for _, o := range others {
						fmt.Fprintf(&b, "  Also on %s: %s\n", sourceLabel(o.Source), o.URL)
					}
				}
				b.WriteString("\n")
			}
		}
	}
	if l.more > 0 {
		fmt.Fprintf(&b, "...and %d more.\n", l.more)
	}
	fmt.Fprintf(&b, "See all in ConcertFinder: %s\n", l.to.feedURL())
	b.WriteString("\n---\n")
	fmt.Fprintf(&b, "%s\n", l.footnote)
	fmt.Fprintf(&b, "Manage email preferences: %s\n", l.to.prefsURL())
	fmt.Fprintf(&b, "Unsubscribe: %s\n", l.to.UnsubscribeURL)
	if l.to.Spotify {
		b.WriteString("Powered by Spotify\n")
	}
	return b.String()
}

// Colours. Light values are inline, because that is all most clients honour;
// the dark ones live in one <style> block keyed by class, for the clients
// that read prefers-color-scheme (Apple Mail, iOS Mail, Outlook.com). Gmail
// ignores the block and applies its own inversion to the light values.
const (
	fontStack = `-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif`
	cPage     = "#f4f4f5"
	cCard     = "#ffffff"
	cBorder   = "#e4e4e7"
	cText     = "#18181b"
	cMuted    = "#52525b"
	cReason   = "#15803d"
	// cButton is darker than the site's primary green so white text on it
	// stays readable (about 5:1, where the site's green is about 2.5:1).
	cButton = "#15803d"
)

const styleBlock = `<style>` +
	`:root{color-scheme:light dark;supported-color-schemes:light dark}` +
	`a{text-decoration:none}` +
	`@media (prefers-color-scheme:dark){` +
	`.bg{background:#09090b!important}` +
	`.cd{background:#18181b!important;border-color:#27272a!important}` +
	`.tx{color:#fafafa!important}` +
	`.mu{color:#a1a1aa!important}` +
	`.rs{color:#4ade80!important}` +
	`.dt{background:#27272a!important;color:#fafafa!important}` +
	`.ln{color:#4ade80!important}` +
	`}` +
	`@media (max-width:480px){.cdp{padding:14px!important}.h1{font-size:22px!important}}` +
	`</style>`

func (l layout) html() string {
	var b strings.Builder
	b.Grow(24 << 10)
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<meta name="color-scheme" content="light dark"><meta name="supported-color-schemes" content="light dark">`)
	fmt.Fprintf(&b, `<title>%s</title>%s</head>`, esc(l.subject), styleBlock)
	fmt.Fprintf(&b, `<body class="bg" style="margin:0;padding:0;background:%s">`, cPage)
	if l.preheader != "" {
		// Hidden preview text, padded so the client doesn't run on into the
		// visible body after it.
		fmt.Fprintf(&b, `<div style="display:none;max-height:0;overflow:hidden;opacity:0;mso-hide:all">%s%s</div>`,
			esc(l.preheader), strings.Repeat("&#8199;&#847;", 30))
	}
	fmt.Fprintf(&b, `<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" border="0" class="bg" style="background:%s"><tr><td align="center" style="padding:24px 12px">`, cPage)
	fmt.Fprintf(&b, `<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" border="0" style="max-width:600px;font-family:%s;color:%s;line-height:1.4">`, fontStack, cText)

	// Header: logo and name.
	fmt.Fprintf(&b, `<tr><td style="padding:0 4px 20px"><a href="%s"><img src="%s" width="40" height="40" alt="ConcertFinder" style="display:inline-block;vertical-align:middle;border:0;border-radius:9px"></a>`+
		`<span class="tx" style="display:inline-block;vertical-align:middle;padding-left:10px;font-size:18px;font-weight:700;color:%s">ConcertFinder</span></td></tr>`,
		esc(l.to.feedURL()), esc(l.to.logoURL()), cText)
	fmt.Fprintf(&b, `<tr><td style="padding:0 4px 8px"><div class="tx h1" style="font-size:26px;font-weight:800;color:%s">%s</div>`+
		`<div class="mu" style="font-size:15px;color:%s;padding-top:4px">%s</div></td></tr>`,
		cText, esc(l.title), cMuted, esc(l.intro))

	for _, sec := range l.sections {
		if sec.title != "" {
			fmt.Fprintf(&b, `<tr><td class="tx" style="padding:26px 4px 0;font-size:19px;font-weight:800;color:%s">%s</td></tr>`, cText, esc(sec.title))
		}
		for _, g := range sec.months {
			fmt.Fprintf(&b, `<tr><td class="mu" style="padding:18px 4px 8px;font-size:12px;font-weight:700;letter-spacing:.08em;text-transform:uppercase;color:%s">%s</td></tr>`, cMuted, esc(g.label))
			for _, e := range g.items {
				l.card(&b, e)
			}
		}
	}

	if l.more > 0 {
		fmt.Fprintf(&b, `<tr><td class="mu" style="padding:12px 4px 0;font-size:15px;color:%s">…and %d more.</td></tr>`, cMuted, l.more)
	}
	fmt.Fprintf(&b, `<tr><td align="center" style="padding:24px 4px 8px">%s</td></tr>`,
		button(l.to.feedURL(), "See all in ConcertFinder", "15px", "13px 26px"))

	// Footer.
	fmt.Fprintf(&b, `<tr><td class="mu" style="padding:24px 4px 0;border-top:1px solid %s;font-size:12px;color:%s;text-align:center">`, cBorder, cMuted)
	fmt.Fprintf(&b, `<div style="padding-bottom:8px">%s</div>`, esc(l.footnote))
	fmt.Fprintf(&b, `<a class="mu" href="%s" style="color:%s;text-decoration:underline">Manage email preferences</a> &nbsp;·&nbsp; `+
		`<a class="mu" href="%s" style="color:%s;text-decoration:underline">Unsubscribe</a>`,
		esc(l.to.prefsURL()), cMuted, esc(l.to.UnsubscribeURL), cMuted)
	if l.to.Spotify {
		// Text only, on purpose: remote images are blocked by default in
		// most mail clients, so a logo here would attribute nothing most of
		// the time while the words always land.
		b.WriteString(`<div style="padding-top:8px">Powered by Spotify</div>`)
	}
	b.WriteString(`</td></tr></table></td></tr></table></body></html>`)
	return b.String()
}

// card renders one event: a date block, the acts, the room, the reason line
// and the ticket links.
func (l layout) card(b *strings.Builder, e concerts.Event) {
	day := e.LocalDayTime()
	fmt.Fprintf(b, `<tr><td style="padding:0 0 12px"><table role="presentation" width="100%%" cellpadding="0" cellspacing="0" border="0" class="cd" style="background:%s;border:1px solid %s;border-radius:12px">`+
		`<tr><td class="cdp" style="padding:16px"><table role="presentation" width="100%%" cellpadding="0" cellspacing="0" border="0"><tr>`,
		cCard, cBorder)
	fmt.Fprintf(b, `<td width="52" valign="top"><div class="dt" style="width:52px;background:%s;border-radius:8px;text-align:center;padding:6px 0;color:%s">`+
		`<div style="font-size:11px;font-weight:700;letter-spacing:.06em">%s</div>`+
		`<div style="font-size:22px;font-weight:800;line-height:1.1">%s</div>`+
		`<div style="font-size:11px">%s</div></div></td>`,
		cPage, cText, strings.ToUpper(day.Format("Jan")), day.Format("2"), day.Format("Mon"))
	fmt.Fprintf(b, `<td valign="top" style="padding-left:14px">`)
	fmt.Fprintf(b, `<div class="tx" style="font-size:17px;font-weight:700;color:%s">%s</div>`, cText, esc(actsLine(e.Acts)))
	if e.Name != "" {
		fmt.Fprintf(b, `<div class="mu" style="font-size:13px;color:%s;padding-top:2px">%s</div>`, cMuted, esc(e.Name))
	}
	fmt.Fprintf(b, `<div class="mu" style="font-size:14px;color:%s;padding-top:2px">%s</div>`, cMuted, esc(place(e)))
	if r := reasonLine(e.Acts); r != "" {
		fmt.Fprintf(b, `<div class="rs" style="font-size:13px;font-weight:600;color:%s;padding-top:4px">%s</div>`, cReason, esc(r))
	}
	if primary, others, ok := ticketLinks(e.Links); ok {
		fmt.Fprintf(b, `<div style="padding-top:12px">%s`, button(primary.URL, "Get tickets", "14px", "9px 16px"))
		if len(others) > 0 {
			fmt.Fprintf(b, `<span class="mu" style="font-size:13px;color:%s;padding-left:10px">Also on `, cMuted)
			for i, o := range others {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(b, `<a class="ln" href="%s" style="color:%s;text-decoration:underline">%s</a>`, esc(o.URL), cReason, esc(sourceLabel(o.Source)))
			}
			b.WriteString(`</span>`)
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</td></tr></table></td></tr></table></td></tr>`)
}

// button is a padded link styled as a button. A plain <a> with padding
// renders in every client that matters here; Outlook desktop shows it as a
// text link, which still works.
func button(href, label, size, pad string) string {
	return fmt.Sprintf(`<a href="%s" style="display:inline-block;background:%s;color:#ffffff;font-size:%s;font-weight:700;padding:%s;border-radius:8px;text-decoration:none">%s</a>`,
		esc(href), cButton, size, pad, esc(label))
}

func esc(s string) string { return html.EscapeString(s) }
