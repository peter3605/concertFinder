import { NavLink, Outlet } from 'react-router-dom';
import { Bookmark, Calendar, Music2, Settings, Users } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { SpotifyAttribution } from '@/components/spotify-attribution';
import { ThemeToggle } from '@/components/theme-toggle';
import { useAuth } from '@/lib/auth';
import { cn } from '@/lib/utils';

const NAV = [
  { to: '/', label: 'Concerts', icon: Calendar, end: true },
  { to: '/saved', label: 'Saved', icon: Bookmark },
  { to: '/subscribe', label: 'Subscriptions', icon: Users },
  { to: '/settings', label: 'Settings', icon: Settings },
];

export function Layout() {
  const { auth, logout } = useAuth();
  return (
    <div className="min-h-screen bg-background text-foreground">
      <header className="sticky top-0 z-40 border-b border-border/60 bg-background/80 backdrop-blur">
        <div className="mx-auto flex h-14 max-w-6xl items-center gap-4 px-4">
          <NavLink to="/" className="flex items-center gap-2 font-semibold">
            <Music2 className="h-5 w-5 text-primary" />
            ConcertFinder
          </NavLink>
          {auth.kind === 'signed_in' && (
            <nav className="hidden gap-1 md:flex">
              {NAV.map(({ to, label, icon: Icon, end }) => (
                <NavLink
                  key={to}
                  to={to}
                  end={end}
                  className={({ isActive }) =>
                    cn(
                      'inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm font-medium transition-colors',
                      isActive
                        ? 'bg-accent text-accent-foreground'
                        : 'text-muted-foreground hover:bg-accent hover:text-accent-foreground',
                    )
                  }
                >
                  <Icon className="h-4 w-4" />
                  {label}
                </NavLink>
              ))}
            </nav>
          )}
          <div className="ml-auto flex items-center gap-2">
            <ThemeToggle />
            {auth.kind === 'signed_in' && (
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="ghost" size="sm">
                    {auth.me.display_name || auth.me.spotify_user_id}
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuLabel>
                    <div className="flex flex-col">
                      <span>{auth.me.display_name || auth.me.spotify_user_id}</span>
                      {auth.me.email && (
                        <span className="text-xs font-normal text-muted-foreground">
                          {auth.me.email}
                        </span>
                      )}
                    </div>
                  </DropdownMenuLabel>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem asChild>
                    <NavLink to="/settings">
                      <Settings className="h-4 w-4" /> Settings
                    </NavLink>
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem onClick={logout}>Log out</DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            )}
          </div>
        </div>
        {auth.kind === 'signed_in' && (
          <nav className="flex gap-1 border-t border-border/60 px-2 py-1 md:hidden">
            {NAV.map(({ to, label, icon: Icon, end }) => (
              <NavLink
                key={to}
                to={to}
                end={end}
                className={({ isActive }) =>
                  cn(
                    'flex flex-1 items-center justify-center gap-1 rounded-md px-2 py-1.5 text-xs',
                    isActive
                      ? 'bg-accent text-accent-foreground'
                      : 'text-muted-foreground hover:bg-accent',
                  )
                }
              >
                <Icon className="h-3.5 w-3.5" />
                {label}
              </NavLink>
            ))}
          </nav>
        )}
      </header>
      <main className="mx-auto max-w-6xl px-4 py-6">
        <Outlet />
      </main>
      <footer className="mx-auto max-w-6xl px-4 pb-8 text-xs text-muted-foreground">
        <SpotifyAttribution /> · <NavLink to="/privacy" className="hover:underline">Privacy</NavLink> ·{' '}
        <NavLink to="/terms" className="hover:underline">Terms</NavLink>
      </footer>
    </div>
  );
}
