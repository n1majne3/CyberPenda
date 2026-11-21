import { Card, CardDescription, CardTitle } from "@/components/ui";

export function SectionHeading({ title, detail }: { title: string; detail?: string }) {
  return (
    <div className="mb-2 flex flex-wrap items-baseline justify-between gap-2">
      <h3 className="text-sm font-semibold tracking-tight text-foreground">{title}</h3>
      {detail && <p className="font-mono text-[11px] text-muted-foreground">{detail}</p>}
    </div>
  );
}

export function ErrorBanner({ message }: { message: string }) {
  return (
    <Card role="alert" className="m-4 border-destructive/25">
      <CardTitle className="text-sm">Couldn't load Blackboard</CardTitle>
      <CardDescription className="mt-1">{message}</CardDescription>
    </Card>
  );
}
