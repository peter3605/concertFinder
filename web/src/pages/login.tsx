import { Link } from 'react-router-dom';
import { Music2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { useDocumentTitle } from '@/lib/use-document-title';
import DiscoverSection from '@/pages/discover';

// Landing page for anonymous users. Explains what the app is + a single
// prominent CTA to start the Spotify OAuth flow.
export default function LoginPage() {
  useDocumentTitle('Log in');
  return (
    // Wider than the card so the discover cards below it aren't squeezed into
    // the sign-in column; the card keeps its own max-w-md.
    <div className="mx-auto mt-16 flex max-w-2xl flex-col gap-10">
      <Card className="mx-auto w-full max-w-md">
        <CardHeader className="items-center text-center">
          <div className="mb-2 inline-flex h-12 w-12 items-center justify-center rounded-full bg-primary/10 text-primary">
            <Music2 className="h-6 w-6" />
          </div>
          <CardTitle className="text-2xl">Welcome to ConcertFinder</CardTitle>
          <CardDescription>
            A personal concert feed built from your Spotify listening. Log in to
            start.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col items-center gap-3">
          <Button asChild size="lg" className="w-full">
            <a href="/api/auth/login">Log in with Spotify</a>
          </Button>
          <p className="text-center text-xs text-muted-foreground">
            We only read your library — never post, follow, or change anything.
            See{' '}
            <Link to="/privacy" className="text-primary hover:underline">
              Privacy
            </Link>{' '}
            for details.
          </p>
        </CardContent>
      </Card>
      {/* Renders nothing when the shared cache has nothing near the default
          coordinate — no empty state and no error. It is a sample of what the
          app finds, and a box explaining that there is no sample would be
          worse than the absence. */}
      <DiscoverSection />
    </div>
  );
}
