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
func RenderDigest(displayName, toEmail string, newConcerts []concerts.Concert, unsubURL string) Message {
	subject := fmt.Sprintf("ConcertFinder digest — %d new show%s", len(newConcerts), plural(len(newConcerts)))
	text := renderDigestText(displayName, newConcerts, unsubURL)
	htmlBody := renderDigestHTML(displayName, newConcerts, unsubURL)
	return Message{To: toEmail, Subject: subject, Text: text, HTML: htmlBody}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

type monthGroup struct {
	label string
	items []concerts.Concert
}

func groupByMonth(cs []concerts.Concert) []monthGroup {
	buckets := map[string][]concerts.Concert{}
	labels := map[string]string{}
	for _, c := range cs {
		key := c.Date.Format("2006-01")
		buckets[key] = append(buckets[key], c)
		labels[key] = c.Date.Format("January 2006")
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

func renderDigestText(displayName string, cs []concerts.Concert, unsub string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hi %s,\n\n", displayName)
	fmt.Fprintf(&b, "%d new show%s in your area since your last digest:\n\n", len(cs), plural(len(cs)))
	for _, g := range groupByMonth(cs) {
		fmt.Fprintf(&b, "== %s ==\n", g.label)
		for _, c := range g.items {
			fmt.Fprintf(&b, "  %s — %s\n    %s, %s%s\n",
				c.Artist.Name,
				c.Date.Format("Mon Jan 2"),
				c.Venue, c.City, stateSuffix(c.State),
			)
			for _, l := range c.Links {
				fmt.Fprintf(&b, "    %s: %s\n", l.Source, l.URL)
			}
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\n---\nManage or unsubscribe: %s\n", unsub)
	return b.String()
}

func renderDigestHTML(displayName string, cs []concerts.Concert, unsub string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body style="font-family:-apple-system,BlinkMacSystemFont,Helvetica,Arial,sans-serif;max-width:600px;margin:2rem auto;line-height:1.5;color:#222">`)
	fmt.Fprintf(&b, `<h2 style="margin-bottom:0.2rem">ConcertFinder digest</h2>`)
	fmt.Fprintf(&b, `<p style="color:#666;margin-top:0">Hi %s — %d new show%s in your area since your last digest.</p>`,
		html.EscapeString(displayName), len(cs), plural(len(cs)))

	for _, g := range groupByMonth(cs) {
		fmt.Fprintf(&b, `<h3 style="border-bottom:1px solid #eee;padding-bottom:0.3rem;margin-top:1.5rem">%s</h3>`,
			html.EscapeString(g.label))
		b.WriteString(`<ul style="list-style:none;padding:0">`)
		for _, c := range g.items {
			fmt.Fprintf(&b,
				`<li style="padding:0.5rem 0;border-bottom:1px solid #f4f4f4">`+
					`<strong>%s</strong> · %s<br>`+
					`<span style="color:#555;font-size:0.95em">%s, %s%s</span>`,
				html.EscapeString(c.Artist.Name),
				html.EscapeString(c.Date.Format("Mon Jan 2")),
				html.EscapeString(c.Venue),
				html.EscapeString(c.City),
				html.EscapeString(stateSuffix(c.State)),
			)
			if len(c.Links) > 0 {
				b.WriteString(`<div style="margin-top:0.3rem">`)
				for _, l := range c.Links {
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
func RenderInstantNotify(displayName, toEmail string, fresh []concerts.Concert, unsubURL string) Message {
	subject := fmt.Sprintf("New show: %s", fresh[0].Artist.Name)
	if len(fresh) > 1 {
		subject = fmt.Sprintf("%d new shows for artists you follow", len(fresh))
	}
	text := renderInstantText(displayName, fresh, unsubURL)
	htmlBody := renderInstantHTML(displayName, fresh, unsubURL)
	return Message{To: toEmail, Subject: subject, Text: text, HTML: htmlBody}
}

func renderInstantText(displayName string, cs []concerts.Concert, unsub string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hi %s,\n\n", displayName)
	if len(cs) == 1 {
		fmt.Fprintf(&b, "A new show just landed for %s — an artist you're following.\n\n", cs[0].Artist.Name)
	} else {
		fmt.Fprintf(&b, "%d new shows just landed for artists you follow:\n\n", len(cs))
	}
	for _, c := range cs {
		fmt.Fprintf(&b, "  %s — %s\n    %s, %s%s\n",
			c.Artist.Name,
			c.Date.Format("Mon Jan 2, 2006"),
			c.Venue, c.City, stateSuffix(c.State),
		)
		for _, l := range c.Links {
			fmt.Fprintf(&b, "    %s: %s\n", l.Source, l.URL)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "---\nStop these notifications: %s\n", unsub)
	return b.String()
}

func renderInstantHTML(displayName string, cs []concerts.Concert, unsub string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body style="font-family:-apple-system,BlinkMacSystemFont,Helvetica,Arial,sans-serif;max-width:600px;margin:2rem auto;line-height:1.5;color:#222">`)
	b.WriteString(`<h2 style="margin-bottom:0.2rem">New show alert</h2>`)
	if len(cs) == 1 {
		fmt.Fprintf(&b, `<p style="color:#666;margin-top:0">Hi %s — a new show just landed for <strong>%s</strong>, an artist you're following.</p>`,
			html.EscapeString(displayName), html.EscapeString(cs[0].Artist.Name))
	} else {
		fmt.Fprintf(&b, `<p style="color:#666;margin-top:0">Hi %s — %d new shows just landed for artists you follow.</p>`,
			html.EscapeString(displayName), len(cs))
	}
	b.WriteString(`<ul style="list-style:none;padding:0">`)
	for _, c := range cs {
		fmt.Fprintf(&b,
			`<li style="padding:0.6rem 0;border-bottom:1px solid #f4f4f4">`+
				`<strong>%s</strong> · %s<br>`+
				`<span style="color:#555;font-size:0.95em">%s, %s%s</span>`,
			html.EscapeString(c.Artist.Name),
			html.EscapeString(c.Date.Format("Mon Jan 2, 2006")),
			html.EscapeString(c.Venue),
			html.EscapeString(c.City),
			html.EscapeString(stateSuffix(c.State)),
		)
		if len(c.Links) > 0 {
			b.WriteString(`<div style="margin-top:0.3rem">`)
			for _, l := range c.Links {
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
