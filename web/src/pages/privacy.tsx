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

      <h2>What we collect</h2>
      <ul>
        <li><strong>Spotify identity.</strong> Your Spotify user ID and public display name.</li>
        <li>
          <strong>An encrypted Spotify refresh token.</strong> Stored using
          AES-256-GCM with a per-token nonce so we can keep querying your
          listening data after your browser session ends.
        </li>
        <li>
          <strong>A derived artist-affinity profile.</strong> A list of
          artists and numeric scores computed from your Spotify library
          (top artists, saved albums, saved tracks, recently played, followed
          artists, playlists you own). Cached for 24 hours and rebuilt from
          Spotify.
        </li>
        <li><strong>Your location and search radius.</strong> The city / coordinates you set for concert search.</li>
        <li><strong>Concerts you save.</strong> A list of shows you've bookmarked.</li>
        <li><strong>Artists you subscribe to.</strong> The list of artists you follow for instant notifications.</li>
        <li><strong>Session metadata.</strong> A random session cookie so you stay logged in.</li>
      </ul>

      <h2>What we do not collect</h2>
      <ul>
        <li>Your email address (unless you grant the email scope for notifications).</li>
        <li>Your Spotify password (OAuth means we never see it).</li>
        <li>Payment or billing information — we don't handle transactions.</li>
        <li>Browsing data outside this app, cross-site trackers, or analytics beacons.</li>
      </ul>

      <h2>What we don't do with your data</h2>
      <ul>
        <li>No ML training on your Spotify content, per Spotify's developer terms.</li>
        <li>No caching of the raw Spotify content itself (only the derived affinity scores).</li>
        <li>No selling, renting, or sharing with advertisers or brokers.</li>
      </ul>

      <h2>Third parties we contact on your behalf</h2>
      <p>
        Concert search fans out to public APIs that see the search parameters
        (your location + an artist name) but not your identity:
      </p>
      <ul>
        <li>Ticketmaster Discovery API</li>
        <li>Songkick API (optional)</li>
        <li>MusicBrainz (for artist website resolution)</li>
        <li>OpenStreetMap Nominatim (for venue geocoding)</li>
        <li>Spotify Web API (for your listening data)</li>
      </ul>

      <h2>Deletion</h2>
      <p>
        Log out via the menu in the header to end your session. To delete
        your account and all associated data, email the operator below.
      </p>

      <h2>Contact</h2>
      <p>
        Email: <a href={`mailto:${info.contact_email}`}>{info.contact_email || 'contact@…'}</a>
      </p>
    </article>
  );
}
