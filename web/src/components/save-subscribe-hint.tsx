import { useState } from 'react';
import { Bell, Star, X } from 'lucide-react';

// Where the dismissal is remembered. localStorage rather than the account:
// this is a one-off "you have now read the sentence", not a preference worth a
// column, a migration and a round trip on every feed load.
const DISMISSED_KEY = 'cf.hintDismissed.saveSubscribe';

function alreadyDismissed(): boolean {
  try {
    return localStorage.getItem(DISMISSED_KEY) === '1';
  } catch {
    // Safari in private mode throws on access. A hint nobody can dismiss is
    // worse than one that shows again next session, so fail towards hidden.
    return true;
  }
}

// One sentence introducing the two per-artist controls on every card. They are
// individually readable — a star and a bell — but nothing said what the
// difference was, and subscribing is the action that keeps someone coming
// back, so it is the one worth naming out loud.
//
// Dismissible and shown once ever, because it is scaffolding: it stops being
// useful the moment it has been read, and a permanent banner above the first
// card is a permanent tax on the feed.
export function SaveSubscribeHint() {
  const [dismissed, setDismissed] = useState(alreadyDismissed);
  if (dismissed) return null;

  function dismiss() {
    setDismissed(true);
    try {
      localStorage.setItem(DISMISSED_KEY, '1');
    } catch {
      // Non-fatal: the hint is gone for this session either way.
    }
  }

  return (
    <div className="flex items-start gap-3 rounded-md border border-dashed border-border p-3 text-sm text-muted-foreground">
      <p className="flex-1">
        Tap the <Star className="mb-0.5 inline h-3.5 w-3.5" aria-label="star" /> to save a show to
        your Saved list, or the <Bell className="mb-0.5 inline h-3.5 w-3.5" aria-label="bell" /> to
        get told whenever that artist announces a new one.
      </p>
      <button
        onClick={dismiss}
        aria-label="Dismiss"
        className="-m-1 shrink-0 rounded p-1 hover:bg-accent coarse:-m-2.5 coarse:p-2.5"
      >
        <X className="h-4 w-4" />
      </button>
    </div>
  );
}
