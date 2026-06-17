// PageStub is a placeholder used while a page is being built out. Pages replace
// it in their own phase.
export function PageStub({ title }: { title: string }) {
  return (
    <div className="p-8">
      <h2 className="text-xl font-semibold mb-2">{title}</h2>
      <p className="text-sm text-muted-foreground">This view is under construction.</p>
    </div>
  );
}
