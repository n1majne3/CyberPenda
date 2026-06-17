import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

// cn merges Tailwind classes with conditional logic. shadcn/ui convention.
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
