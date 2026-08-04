import { X } from 'lucide-react';

// Inline banner for a failed optimistic action (saving a show, subscribing
// to an artist). Those mutations flip the UI immediately and roll back if
// the request fails; without a message the rollback looks like the app
// undoing the user's click for no reason.
//
// role="alert" so assistive tech announces it, since the visual change is
// otherwise the only signal.
export function ActionError({
  message,
  onDismiss,
}: {
  message: string | null;
  onDismiss: () => void;
}) {
  if (!message) return null;
  return (
    <div
      role="alert"
      className="flex items-start gap-3 rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive"
    >
      <span className="flex-1">{message}</span>
      <button
        onClick={onDismiss}
        aria-label="Dismiss"
        className="rounded p-0.5 hover:bg-destructive/10"
      >
        <X className="h-4 w-4" />
      </button>
    </div>
  );
}
