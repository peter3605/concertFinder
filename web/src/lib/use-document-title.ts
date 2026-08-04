import { useEffect } from 'react';

const BASE = 'ConcertFinder';

// Sets the document title for a route and restores the base title on
// unmount. Without this every page is just "ConcertFinder", which makes
// browser history and multiple tabs indistinguishable.
export function useDocumentTitle(title?: string) {
  useEffect(() => {
    document.title = title ? `${title} · ${BASE}` : BASE;
    return () => {
      document.title = BASE;
    };
  }, [title]);
}
