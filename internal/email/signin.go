package email

import (
	"fmt"
	"html"
)

// SignInPurpose selects the wording of RenderSignIn.
type SignInPurpose string

const (
	SignInLogin  SignInPurpose = "login"
	SignInVerify SignInPurpose = "verify"
)

// RenderSignIn builds the sign-in or address-confirmation mail: a link and
// the same token's 6-digit code. The code is for opening the mail on another
// device, and for the app when the link does not open it.
//
// It carries no List-Unsubscribe: it is transactional mail somebody asked for
// seconds ago, not a notification they subscribed to.
//
// The link lands on a confirmation page, never on the action itself, because
// mail security gateways fetch every URL in a message. The text says "open",
// not "click to sign in", so it stays true about what the link does.
func RenderSignIn(to, link, code string, purpose SignInPurpose) Message {
	subject := "Your ConcertFinder sign-in link"
	action := "sign in to ConcertFinder"
	button := "Sign in"
	if purpose == SignInVerify {
		subject = "Confirm your email address for ConcertFinder"
		action = "confirm this address for your ConcertFinder account"
		button = "Confirm address"
	}
	text := fmt.Sprintf(`To %s, open this link:

%s

Or enter this code: %s

The link and code work once and expire in 15 minutes. If you did not ask for this, you can ignore this email; nothing changes until the link is used.
`, action, link, code)
	body := fmt.Sprintf(`<!doctype html>
<html><body style="font-family:-apple-system,Helvetica,Arial,sans-serif;max-width:520px;margin:2rem auto;line-height:1.5;color:#222">
<p>To %s, open this link:</p>
<p><a href="%s" style="display:inline-block;padding:0.6rem 1.1rem;border-radius:6px;background:#1db954;color:#fff;text-decoration:none">%s</a></p>
<p>Or enter this code: <strong style="font-size:1.3em;letter-spacing:0.15em">%s</strong></p>
<p style="color:#666;font-size:0.9em">The link and code work once and expire in 15 minutes. If you did not ask for this, you can ignore this email; nothing changes until the link is used.</p>
</body></html>`, html.EscapeString(action), html.EscapeString(link), html.EscapeString(button), html.EscapeString(code))
	return Message{To: to, Subject: subject, Text: text, HTML: body}
}
