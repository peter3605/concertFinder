import blackLogo from '@/assets/spotify-full-logo-black.svg';
import whiteLogo from '@/assets/spotify-full-logo-white.svg';

// "Powered by [Spotify]" attribution, required on any surface showing
// Spotify-derived data. One component so a page added later inherits it rather
// than reinventing the markup — the same reason the iOS client has a single
// `SpotifyAttribution` view.
//
// Every number and choice here comes from Spotify's design guidelines, and the
// iOS component carries the same reasoning at more length:
//
//   - The full logo (icon + wordmark), not the bare icon. The icon alone is
//     only allowed where it stands in as an app icon on a device home screen.
//   - A 70px floor on the full logo's width. `min-w-[70px]` states it; the
//     default `w-[70px]` is that floor exactly.
//   - Clear space of half the mark's height on every side.
//   - Black on light, white on dark. Spotify green is restricted to black or
//     white backgrounds, and the app's surfaces are neither in either theme.
//
// Two assets rather than one recoloured with `currentColor`: the wordmark may
// not be modified, and black/white are approved colourways while a
// muted-foreground grey is not. Tailwind's `dark` variant is a class on
// <html>, so both are in the DOM and CSS picks one — no flash on first paint
// the way a JS-selected src would give.
//
// The label stops at "Powered by" because the logo already contains the
// wordmark — "Powered by Spotify" beside it would say Spotify twice. The alt
// text carries the whole phrase so assistive tech reads it once, which is why
// the visible words are aria-hidden.
export function SpotifyAttribution({
  label = 'Powered by',
  className = '',
}: {
  label?: string;
  className?: string;
}) {
  const alt = `${label} Spotify`;
  return (
    <span className={`inline-flex items-center gap-[10px] p-[10px] ${className}`}>
      <span aria-hidden="true">{label}</span>
      <img src={blackLogo} alt={alt} className="h-auto w-[70px] min-w-[70px] dark:hidden" />
      <img src={whiteLogo} alt={alt} className="hidden h-auto w-[70px] min-w-[70px] dark:inline" />
    </span>
  );
}
