import { useEffect, useState } from 'react';

type SiteInfo = { contact_email: string; effective_date: string };

export default function TermsPage() {
  const [info, setInfo] = useState<SiteInfo>({ contact_email: '', effective_date: '' });
  useEffect(() => {
    fetch('/api/site-info')
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
        <li>You need a Spotify account to log in and agree to Spotify's own terms.</li>
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
        Ticket-purchase links go to third parties (Ticketmaster, Bandsintown,
        Songkick, or an artist's official site). Those providers set prices,
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
        You can log out at any time. Accounts that abuse the service may be
        terminated without notice.
      </p>

      <h2>Contact</h2>
      <p>
        Email: <a href={`mailto:${info.contact_email}`}>{info.contact_email || 'contact@…'}</a>
      </p>
    </article>
  );
}
