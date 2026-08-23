import { useDocumentTitle } from '@/lib/use-document-title';

// The universal link the iOS OAuth callback redirects to. When the app is
// installed, iOS intercepts this URL before Safari ever loads it, so nobody
// sees this page on the happy path.
//
// It exists for the unhappy one. Without a real route here the SPA's catch-all
// sends unknown paths to '/', so a user who reached this URL in a browser —
// the app not installed, a link forwarded to a desktop, universal links not
// yet associated after an install — got a silent bounce to the feed with no
// indication that anything had gone wrong. That is the class of failure that
// is impossible to diagnose from a bug report.
//
// Deliberately says nothing about the `code` in the query string: it is
// single-use, 60-second-lived, and useless without the verifier held by the
// app that started the login. Displaying it would only invite someone to
// paste it somewhere.
export default function AppCallbackPage() {
  useDocumentTitle('Open in the app');
  return (
    <article className="prose prose-neutral mx-auto max-w-2xl dark:prose-invert prose-headings:font-semibold prose-a:text-primary">
      <h1>Finish signing in from the app</h1>

      <p>
        This page is the last step of signing in to <strong>ConcertFinder for
        iOS</strong>. It normally opens the app automatically — if you are
        reading this in a browser, the app did not pick it up.
      </p>

      <h2>What to do</h2>
      <ul>
        <li>
          If you have the app installed, open it and sign in again. This link
          expires after a minute, so starting over is the fix.
        </li>
        <li>
          If you are on a computer, sign in from your iPhone instead — this
          step only works on the device running the app.
        </li>
        <li>
          If you do not use the iOS app, you can{' '}
          <a href="/login">sign in here on the web</a> instead.
        </li>
      </ul>

      <p className="text-sm text-muted-foreground">
        Nothing has gone wrong with your account, and no sign-in was completed
        on this device.
      </p>
    </article>
  );
}
