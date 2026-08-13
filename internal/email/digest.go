package email

import (
	"fmt"
	"html"
	"sort"
	"strings"

	"github.com/peterho/concertfinder/internal/concerts"
)

// RenderDigest turns a set of net-new concerts into a Message. The list is
// grouped by month for readability; artist name + venue + date are the only
// fields surfaced. unsubURL is embedded in both HTML and text bodies.
//
// Concerts are folded into events first, exactly as the web list does: a
// festival that matched six of the user's artists is six Concert rows sharing
// a date, venue, and city, and mailing it as six rows under a subject reading
// "6 new shows" over-counts one night out by six. Grouping is presentational
// only — the caller's net-new bookkeeping stays keyed on dedup_key, one per
// (artist, show), so an act added to a bill the user has already been emailed
// about still mails on its own.
func RenderDigest(displayName, toEmail string, newConcerts []concerts.Concert, unsubURL string) Message {
	events := concerts.GroupEvents(newConcerts)
	subject := fmt.Sprintf("ConcertFinder digest — %d new show%s", len(events), plural(len(events)))
	text := renderDigestText(displayName, events, unsubURL)
	htmlBody := renderDigestHTML(displayName, events, unsubURL)
	return Message{To: toEmail, Subject: subject, Text: text, HTML: htmlBody}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// maxListedActs caps how many artists a multi-act entry names before it
// summarizes the remainder, mirroring VISIBLE_ACTS in the web event card. A
// large festival can match dozens of profile artists and an email is a worse
// place than a card to dump them all.
const maxListedActs = 4

// actsLine renders an event's acts as prose: "A", "A and B", "A, B and C",
// "A, B, C, D and 3 more".
func actsLine(acts []concerts.Act) string {
	names := make([]string, 0, len(acts))
	for _, a := range acts {
		names = append(names, a.Artist.Name)
	}
	switch {
	case len(names) == 0:
		return ""
	case len(names) == 1:
		return names[0]
	case len(names) > maxListedActs:
		return strings.Join(names[:maxListedActs], ", ") +
			fmt.Sprintf(" and %d more", len(names)-maxListedActs)
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

type monthGroup struct {
	label string
	items []concerts.Event
}

func groupByMonth(es []concerts.Event) []monthGroup {
	buckets := map[string][]concerts.Event{}
	labels := map[string]string{}
	for _, e := range es {
		key := e.Date.Format("2006-01")
		buckets[key] = append(buckets[key], e)
		labels[key] = e.Date.Format("January 2006")
	}
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]monthGroup, 0, len(keys))
	for _, k := range keys {
		g := buckets[k]
		sort.Slice(g, func(i, j int) bool { return g[i].Date.Before(g[j].Date) })
		out = append(out, monthGroup{label: labels[k], items: g})
	}
	return out
}

func renderDigestText(displayName string, es []concerts.Event, unsub string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hi %s,\n\n", displayName)
	fmt.Fprintf(&b, "%d new show%s in your area since your last digest:\n\n", len(es), plural(len(es)))
	for _, g := range groupByMonth(es) {
		fmt.Fprintf(&b, "== %s ==\n", g.label)
		for _, e := range g.items {
			fmt.Fprintf(&b, "  %s — %s\n    %s, %s%s\n",
				actsLine(e.Acts),
				e.Date.Format("Mon Jan 2"),
				e.Venue, e.City, stateSuffix(e.State),
			)
			for _, l := range e.Links {
				fmt.Fprintf(&b, "    %s: %s\n", l.Source, l.URL)
			}
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\n---\nManage or unsubscribe: %s\n", unsub)
	return b.String()
}

func renderDigestHTML(displayName string, es []concerts.Event, unsub string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body style="font-family:-apple-system,BlinkMacSystemFont,Helvetica,Arial,sans-serif;max-width:600px;margin:2rem auto;line-height:1.5;color:#222">`)
	fmt.Fprintf(&b, `<h2 style="margin-bottom:0.2rem">ConcertFinder digest</h2>`)
	fmt.Fprintf(&b, `<p style="color:#666;margin-top:0">Hi %s — %d new show%s in your area since your last digest.</p>`,
		html.EscapeString(displayName), len(es), plural(len(es)))

	for _, g := range groupByMonth(es) {
		fmt.Fprintf(&b, `<h3 style="border-bottom:1px solid #eee;padding-bottom:0.3rem;margin-top:1.5rem">%s</h3>`,
			html.EscapeString(g.label))
		b.WriteString(`<ul style="list-style:none;padding:0">`)
		for _, e := range g.items {
			fmt.Fprintf(&b,
				`<li style="padding:0.5rem 0;border-bottom:1px solid #f4f4f4">`+
					`<strong>%s</strong> · %s<br>`+
					`<span style="color:#555;font-size:0.95em">%s, %s%s</span>`,
				html.EscapeString(actsLine(e.Acts)),
				html.EscapeString(e.Date.Format("Mon Jan 2")),
				html.EscapeString(e.Venue),
				html.EscapeString(e.City),
				html.EscapeString(stateSuffix(e.State)),
			)
			if len(e.Links) > 0 {
				b.WriteString(`<div style="margin-top:0.3rem">`)
				for _, l := range e.Links {
					fmt.Fprintf(&b, `<a href="%s" style="color:#1db954;margin-right:0.6rem;text-decoration:none">%s</a>`,
						html.EscapeString(l.URL), html.EscapeString(string(l.Source)))
				}
				b.WriteString(`</div>`)
			}
			b.WriteString(`</li>`)
		}
		b.WriteString(`</ul>`)
	}

	fmt.Fprintf(&b, `<p style="margin-top:2rem;font-size:0.8em;color:#888">Powered by Spotify · <a href="%s" style="color:#888">Manage or unsubscribe</a></p>`,
		html.EscapeString(unsub))
	b.WriteString(`</body></html>`)
	return b.String()
}

func stateSuffix(state string) string {
	if state == "" {
		return ""
	}
	return ", " + state
}

// RenderInstantNotify composes an email for one or more newly-discovered
// concerts by artists the user actively subscribed to. Distinct subject and
// intro from the daily digest so recipients can tell them apart at a glance
// (and set inbox rules accordingly).
//
// Grouping is safe here even though the email is per-subscription: the caller
// has already narrowed `fresh` to subscribed artists only, so a merged entry
// can only ever name artists the user asked about — it never reveals the rest
// of the bill. Without the fold, subscribing to three artists who all turn up
// at one festival announcement mails "3 new shows" for one night.
//
// fresh must be non-empty; the caller (SendInstantNotifyWorker) returns early
// when there is nothing to send.
func RenderInstantNotify(displayName, toEmail string, fresh []concerts.Concert, unsubURL string) Message {
	events := concerts.GroupEvents(fresh)
	subject := fmt.Sprintf("New show: %s", actsLine(events[0].Acts))
	if len(events) > 1 {
		subject = fmt.Sprintf("%d new shows for artists you follow", len(events))
	}
	text := renderInstantText(displayName, events, unsubURL)
	htmlBody := renderInstantHTML(displayName, events, unsubURL)
	return Message{To: toEmail, Subject: subject, Text: text, HTML: htmlBody}
}

func renderInstantText(displayName string, es []concerts.Event, unsub string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hi %s,\n\n", displayName)
	if len(es) == 1 {
		fmt.Fprintf(&b, "A new show just landed for %s — %s you're following.\n\n",
			actsLine(es[0].Acts), followingNoun(len(es[0].Acts)))
	} else {
		fmt.Fprintf(&b, "%d new shows just landed for artists you follow:\n\n", len(es))
	}
	for _, e := range es {
		fmt.Fprintf(&b, "  %s — %s\n    %s, %s%s\n",
			actsLine(e.Acts),
			e.Date.Format("Mon Jan 2, 2006"),
			e.Venue, e.City, stateSuffix(e.State),
		)
		for _, l := range e.Links {
			fmt.Fprintf(&b, "    %s: %s\n", l.Source, l.URL)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "---\nStop these notifications: %s\n", unsub)
	return b.String()
}

// followingNoun keeps the single-event intro grammatical when one show turns
// out to carry several of the user's subscriptions.
func followingNoun(acts int) string {
	if acts == 1 {
		return "an artist"
	}
	return "artists"
}

func renderInstantHTML(displayName string, es []concerts.Event, unsub string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body style="font-family:-apple-system,BlinkMacSystemFont,Helvetica,Arial,sans-serif;max-width:600px;margin:2rem auto;line-height:1.5;color:#222">`)
	b.WriteString(`<h2 style="margin-bottom:0.2rem">New show alert</h2>`)
	if len(es) == 1 {
		fmt.Fprintf(&b, `<p style="color:#666;margin-top:0">Hi %s — a new show just landed for <strong>%s</strong>, %s you're following.</p>`,
			html.EscapeString(displayName), html.EscapeString(actsLine(es[0].Acts)), followingNoun(len(es[0].Acts)))
	} else {
		fmt.Fprintf(&b, `<p style="color:#666;margin-top:0">Hi %s — %d new shows just landed for artists you follow.</p>`,
			html.EscapeString(displayName), len(es))
	}
	b.WriteString(`<ul style="list-style:none;padding:0">`)
	for _, e := range es {
		fmt.Fprintf(&b,
			`<li style="padding:0.6rem 0;border-bottom:1px solid #f4f4f4">`+
				`<strong>%s</strong> · %s<br>`+
				`<span style="color:#555;font-size:0.95em">%s, %s%s</span>`,
			html.EscapeString(actsLine(e.Acts)),
			html.EscapeString(e.Date.Format("Mon Jan 2, 2006")),
			html.EscapeString(e.Venue),
			html.EscapeString(e.City),
			html.EscapeString(stateSuffix(e.State)),
		)
		if len(e.Links) > 0 {
			b.WriteString(`<div style="margin-top:0.3rem">`)
			for _, l := range e.Links {
				fmt.Fprintf(&b, `<a href="%s" style="color:#1db954;margin-right:0.6rem;text-decoration:none">%s</a>`,
					html.EscapeString(l.URL), html.EscapeString(string(l.Source)))
			}
			b.WriteString(`</div>`)
		}
		b.WriteString(`</li>`)
	}
	b.WriteString(`</ul>`)
	fmt.Fprintf(&b, `<p style="margin-top:2rem;font-size:0.8em;color:#888">Powered by Spotify · <a href="%s" style="color:#888">Stop these notifications</a></p>`, html.EscapeString(unsub))
	b.WriteString(`</body></html>`)
	return b.String()
}
