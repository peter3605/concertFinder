import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { apiFetch } from '@/lib/api';
import { useDocumentTitle } from '@/lib/use-document-title';

type SiteInfo = { contact_email: string; effective_date: string };

export default function TermsPage() {
  useDocumentTitle('Terms');
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
      <h1>Terms of Service</h1>
      <p className="text-sm text-muted-foreground">Effective {info.effective_date || '…'}</p>

      <p>
        ConcertFinder is a personal-scale, non-commercial concert-discovery
        tool. Using the service means you agree to what follows.
      </p>

      <h2>Use of the service</h2>
      <ul>
        <li>For personal, non-commercial use. Automated scraping of this service's API is not permitted.</li>
        <li>
          You sign in with Apple, with a link sent to your email address, or,
          for the small number of accounts Spotify allows this app, with
          Spotify. Using Apple or Spotify to sign in means their own terms
          apply to that account too.
        </li>
        <li>New accounts may need an invite code. Keep your email account secure: anyone who can read it can use a sign-in link sent to it.</li>
        <li>The artists and genres you pick are used only to search for concerts for you; don't use them to submit anything unlawful or abusive.</li>
        <li>Concert data is aggregated from public third-party APIs. Ticket purchases happen on those third-party sites.</li>
      </ul>

      <h2>No warranty</h2>
      <p>
        The service is provided "as is." Concert listings may be incomplete,
        out of date, or missing for reasons outside our control (upstream
        API downtime, quota exhaustion, cancellations that haven't
        propagated). Verify details with the ticket provider before making
        travel or purchase plans.
      </p>

      <h2>External providers</h2>
      <p>
        Ticket-purchase links go to third parties (Ticketmaster, Songkick, or
        an artist's official site). Those providers set prices,
        availability, and their own terms; issues with a purchase are
        between you and them.
      </p>

      <h2>Rate limits</h2>
      <p>
        Per-user daily caps on outbound API calls protect shared upstream
        quotas. If you hit a cap, results for that source will be
        temporarily incomplete; the cap resets daily.
      </p>

      <h2>Termination</h2>
      <p>
        You can sign out at any time, and delete your account in Settings on
        the web or in the iPhone app; the{' '}
        <Link to="/privacy">privacy policy</Link> describes what deletion removes.
        Accounts that abuse the service may be terminated without notice.
      </p>

      <h2>Contact</h2>
      <p>
        Email: <a href={`mailto:${info.contact_email}`}>{info.contact_email || 'contact@…'}</a>
      </p>
    </article>
  );
}
