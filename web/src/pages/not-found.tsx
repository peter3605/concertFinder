import { Link } from 'react-router-dom';
import { Compass } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { useDocumentTitle } from '@/lib/use-document-title';

// The catch-all route. It used to redirect to `/`, which sent a mistyped or
// dead link to the feed and left the user to work out for themselves that the
// page they asked for was never loaded — or, signed out, bounced them to the
// login screen as if that were the answer.
//
// Not eagerly public-facing in the way a server 404 is: the SPA handler
// answers 200 for any extension-free path because it cannot tell a typo from
// a route without duplicating this file's table. This is the half that tells
// the person.
export default function NotFoundPage() {
  useDocumentTitle('Page not found');
  return (
    <div className="mx-auto mt-16 max-w-md">
      <Card>
        <CardHeader className="items-center text-center">
          <div className="mb-2 inline-flex h-12 w-12 items-center justify-center rounded-full bg-muted text-muted-foreground">
            <Compass className="h-6 w-6" />
          </div>
          <CardTitle className="text-2xl">Page not found</CardTitle>
          <CardDescription>
            That link doesn&rsquo;t point at anything here — it may be out of date.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex justify-center">
          <Button asChild>
            <Link to="/">Go to your concerts</Link>
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}
