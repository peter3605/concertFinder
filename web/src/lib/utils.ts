import { type ClassValue, clsx } from 'clsx';
import { twMerge } from 'tailwind-merge';

// Standard shadcn/ui helper: merge className strings while resolving Tailwind
// utility collisions (later `p-4` overrides earlier `p-2`, etc.).
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
