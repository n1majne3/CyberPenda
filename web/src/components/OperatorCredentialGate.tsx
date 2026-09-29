import { useEffect, useState, useSyncExternalStore, type FormEvent, type ReactNode } from "react";
import {
  apiGet,
  operatorCredentialIsNeeded,
  submitOperatorToken,
  subscribeOperatorCredentialState,
  takeDeniedRequestRetry,
} from "@/lib/api";
import { Logo } from "@/components/Logo";
import {
  Button,
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  Input,
  Label,
} from "@/components/ui";

// Full-page gate for the operator credential. When the daemon cannot authorize
// this browser (fixed token behind a tunnel, expired cookie), the workspace is
// replaced by the sign-in form. A successful submit flips the credential state,
// the workspace remounts, and the one denied GET is retried.
export function OperatorCredentialGate({ children }: { children: ReactNode }) {
  const needed = useSyncExternalStore(subscribeOperatorCredentialState, operatorCredentialIsNeeded);
  useEffect(() => {
    if (needed) return;
    const retry = takeDeniedRequestRetry();
    if (retry) void apiGet(retry.path).catch(() => {});
  }, [needed]);
  if (needed) return <OperatorSignInPage />;
  return <>{children}</>;
}

function OperatorSignInPage() {
  const [token, setToken] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    if (pending) return;
    setPending(true);
    setError(null);
    try {
      await submitOperatorToken(token);
    } catch (reason) {
      setError((reason as Error).message);
      setPending(false);
    }
  };

  return (
    <div className="flex min-h-svh items-center justify-center bg-background p-6 text-foreground">
      <Card size="spacious" className="w-full max-w-sm">
        <CardHeader className="items-center gap-2 text-center">
          <Logo className="h-8 w-8" spin />
          <CardTitle>Operator sign-in required</CardTitle>
          <CardDescription>
            This daemon could not authorize the browser session. Paste the operator token from the
            daemon startup URL or the configured PENTEST_AUTH_TOKEN value.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={onSubmit} className="flex flex-col gap-3">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="operator-token">Operator token</Label>
              <Input
                id="operator-token"
                type="password"
                autoComplete="off"
                autoFocus
                value={token}
                variant={error ? "invalid" : "default"}
                onChange={(event) => setToken(event.target.value)}
                placeholder="Paste the operator token"
              />
            </div>
            {error && (
              <p role="alert" className="text-sm text-destructive">
                {error}
              </p>
            )}
            <Button type="submit" disabled={pending || !token.trim()}>
              {pending ? "Signing in…" : "Sign in"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
