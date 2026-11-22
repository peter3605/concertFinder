import { useEffect, useState } from 'react';
import { apiFetch } from '@/lib/api';
import { useDocumentTitle } from '@/lib/use-document-title';

type SiteInfo = { contact_email: string; effective_date: string };

export default function PrivacyPage() {
  useDocumentTitle('Privacy');
  const [info, setInfo] = useState<SiteInfo>({ contact_email: '', effective_date: '' });
  useEffect(() => {
    // Public endpoint, and this page is linked from the login screen: a 401
    // here must not be read as a session that expired.
    apiFetch('/api/site-info', {}, { publicEndpoint: true })
      .then((r) => (r.ok ? r.json() : null))
      .then((j) => j && setInfo(j))
      .catch(() => {});
  }, []);
  return (
    <article className="prose prose-neutral mx-auto max-w-2xl dark:prose-invert prose-headings:font-semibold prose-a:text-primary">
      <h1>Privacy Policy</h1>
      <p className="text-sm text-muted-foreground">Effective {info.effective_date || '…'}</p>

      <p>
        ConcertFinder is a personal-scale concert-discovery tool. This page
        describes what data the service collects, how it's used, and the
        choices you have.
      </p>

      <h2>How you sign in</h2>
      <p>
        You can create an account with Sign in with Apple or with an emailed
        sign-in link. Signing in with Spotify is also available, but only to
        the small number of accounts Spotify allows this app; it is the only
        way any of your data comes from Spotify.
      </p>

      <h2>What we collect</h2>
      <ul>
        <li>
          <strong>An account identifier.</strong> An internal account ID, plus
          the identifier of each sign-in method you use: Apple's stable user
          identifier for this app, your Spotify user ID, or your email
          address.
        </li>
        <li>
          <strong>Your name.</strong> From Apple the first time you sign in
          with it (Apple sends it only once), from your Spotify display name,
          or as you type it in.
        </li>
        <li>
          <strong>Your email address.</strong> We collect it directly from
          everyone: it is how email sign-in works, and it is where the weekly
          digest and new-show alerts go. With Sign in with Apple you can choose
          Hide My Email, in which case we only ever see an Apple private relay
          address (ending in <code>privaterelay.appleid.com</code>) and Apple
          forwards our mail to you. We never try to learn the address behind
          it.
        </li>
        <li>
          <strong>Artists and genres you pick.</strong> The artists you add
          (stored as Ticketmaster artist IDs and names) and the music genres
          you follow. They decide which concerts we look for.
        </li>
        <li><strong>Your location and search radius.</strong> The city or coordinates you set for concert search. On iPhone this is a one-time, coarse fix you ask for; nothing is tracked in the background.</li>
        <li><strong>Concerts you save.</strong> A list of shows you've bookmarked.</li>
        <li><strong>Artists you subscribe to.</strong> The artists you follow for new-show alerts.</li>
        <li><strong>Email and notification preferences.</strong> Whether you want the digest, alerts and push notifications.</li>
        <li>
          <strong>A push notification token</strong> (iPhone only), kept while
          push is turned on and removed when you sign out or delete your
          account.
        </li>
        <li>
          <strong>Session and sign-in records.</strong> A random session cookie
          (or, in the iPhone app, a session token) keeps you signed in; we
          store only a one-way hash of it. Email sign-in links and codes are
          likewise stored only as hashes, work once, and expire after 15
          minutes.
        </li>
        <li>
          <strong>An encrypted Apple refresh token</strong>, if you use Sign in
          with Apple. We keep it for one reason: so that deleting your account
          can tell Apple to revoke this app's access.
        </li>
        <li>
          <strong>Fair-use records.</strong> Per-day counts of the external
          API calls your searches make, and which locations you searched
          each day, used to enforce usage limits.
        </li>
      </ul>

      <h3>If you sign in with Spotify</h3>
      <ul>
        <li>
          <strong>An encrypted Spotify refresh token.</strong> Stored using
          AES-256-GCM with a per-token nonce so we can keep reading your
          listening data after your session ends. The Apple refresh token
          above is encrypted the same way.
        </li>
        <li>
          <strong>A derived artist-affinity profile.</strong> A list of
          artists and numeric scores computed from your Spotify library
          (top artists, saved albums, saved tracks, recently played, followed
          artists, playlists you own). Cached for 24 hours and rebuilt from
          Spotify. The raw listening data it is built from is held in memory
          only and discarded once the profile is computed.
        </li>
      </ul>
      <p>
        Accounts that do not sign in with Spotify have no Spotify data at all,
        and we never contact Spotify on their behalf.
      </p>

      <h2>What we do not collect</h2>
      <ul>
        <li>Your Apple ID, Spotify or email account password. Each sign-in method proves who you are without showing it to us.</li>
        <li>Payment or billing information — we don't handle transactions.</li>
        <li>Browsing data outside this app, cross-site trackers, advertising identifiers, or analytics beacons.</li>
      </ul>

      <h2>What we don't do with your data</h2>
      <ul>
        <li>No ML training on your Spotify content, per Spotify's developer terms.</li>
        <li>No caching of the raw Spotify content itself (only the derived affinity scores).</li>
        <li>No selling, renting, or sharing with advertisers or brokers, and no tracking across other companies' apps or websites.</li>
      </ul>

      <h2>Service providers that process your data</h2>
      <p>These handle personal data for us, only to run the service:</p>
      <ul>
        <li>
          <strong>Amazon Web Services</strong> hosts the servers and database
          the data above is stored in.
        </li>
        <li>
          <strong>Amazon Simple Email Service (SES)</strong>, part of AWS,
          delivers our email: sign-in links and codes, address confirmations,
          the digest and new-show alerts. It receives your email address and
          the content of each message.
        </li>
        <li>
          <strong>Apple</strong> verifies Sign in with Apple, relays mail to
          Hide My Email addresses, and delivers push notifications through the
          Apple Push Notification service.
        </li>
        <li>
          <strong>Spotify</strong>, only if you sign in with Spotify, supplies
          your listening data through the Spotify Web API.
        </li>
      </ul>

      <h2>Third parties we contact on your behalf</h2>
      <p>
        Concert search fans out to public APIs that see the search parameters
        (a location plus an artist name, artist search text or a genre) but
        not your identity:
      </p>
      <ul>
        <li>Ticketmaster Discovery API (concert listings and the artist picker)</li>
        <li>Songkick API (optional)</li>
        <li>MusicBrainz (for artist website resolution)</li>
        <li>OpenStreetMap Nominatim (for venue geocoding)</li>
      </ul>

      <h2>Your choices and deletion</h2>
      <ul>
        <li>
          Turn the weekly digest and new-show alerts on or off in Settings,
          and push notifications in the iPhone app. The unsubscribe link in
          any email turns off both kinds of email; messages you ask for, such
          as sign-in links, still arrive.
        </li>
        <li>
          <strong>Disconnect Spotify</strong> in Settings deletes your Spotify
          token and everything derived from it while keeping the rest of your
          account; your Spotify user ID stays, so signing in with Spotify
          again restores the same account. Spotify's own record of the permission is removed at{' '}
          <a href="https://www.spotify.com/account/apps/">spotify.com/account/apps</a>.
        </li>
        <li>
          <strong>Delete account</strong> in Settings (on the web or in the
          iPhone app) permanently deletes your account and the data listed
          above, and asks Apple to revoke this app's access if you used Sign in
          with Apple. You can also email the operator below.
        </li>
        <li>Routine database backups expire within 90 days, so deleted data leaves them on that schedule.</li>
      </ul>

      <h2>Contact</h2>
      <p>
        Email: <a href={`mailto:${info.contact_email}`}>{info.contact_email || 'contact@…'}</a>
      </p>
    </article>
  );
}
