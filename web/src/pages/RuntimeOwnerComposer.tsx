import { useEffect, useRef, type KeyboardEvent } from "react";
import { Square, ChevronsUpDown, ArrowUp, CheckCircle2, CircleX, KeyRound, ListPlus, Loader2, Flag, Paperclip, PlugZap } from "lucide-react";
import { type ModelProvider, type ProviderPermissionRequest } from "@/lib/api";
import { Button, Badge, Select, Textarea } from "@/components/ui";
import { displayReasoningEffort, REASONING_EFFORT_VALUES } from "./runtimeProfileForm";
import { conversationModeText } from "@/lib/runtimeOwner/conversationKernel";
import { type ConversationSendMode } from "@/lib/runtimeOwner/types";
import { conversationStatus, type ConversationState } from "@/lib/runtimeOwner/conversationState";

export function ProviderPermissionRequests({
  permissions,
  permissionBusy,
  onRespond,
}: {
  permissions: ProviderPermissionRequest[];
  permissionBusy: string;
  onRespond: (permissionRequestID: string, decision: "allow" | "deny") => void;
}) {
  return (
    <section className="mt-5 rounded-lg border border-warning/25 bg-warning/5 p-3" aria-label="Provider permission requests">
      <div className="space-y-2">
        <div className="flex items-center gap-2 text-xs font-medium text-warning-foreground">
          <KeyRound className="h-3.5 w-3.5" /> Provider permission
        </div>
        {permissions.map((permission) => (
          <div key={permission.permission_request_id} className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-warning/20 bg-background/70 px-3 py-2">
            <div className="min-w-0">
              <div className="text-sm">Permission request</div>
              <code className="block max-w-full truncate text-xs text-muted-foreground">{permission.permission_request_id}</code>
            </div>
            <div className="flex gap-2">
              <Button
                size="sm"
                onClick={() => onRespond(permission.permission_request_id, "allow")}
                disabled={permissionBusy !== ""}
                aria-label={`Allow provider permission ${permission.permission_request_id}`}
              >
                <CheckCircle2 className="h-4 w-4" /> Allow
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={() => onRespond(permission.permission_request_id, "deny")}
                disabled={permissionBusy !== ""}
                aria-label={`Deny provider permission ${permission.permission_request_id}`}
              >
                <CircleX className="h-4 w-4" /> Deny
              </Button>
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}

export function RuntimeOwnerComposer({
  ownerLabel,
  value,
  onChange,
  onKeyDown,
  onSend,
  onQueue,
  onStop,
  onFinish,
  finishAvailable,
  sending,
  conversationState,
  running,
  queueAvailable,
  queueDisabled,
  providerSwitchRequested,
  providerSwitchAvailable,
  sendMode,
  sendActionLabel,
  actionError,
  steerError,
  replacementRecoveryAvailable,
  replacementSelected,
  onUseReplacement,
  continuationModelProviders,
  continuationModelProvider,
  continuationModelOverride,
  continuationModelOptions,
  continuationReasoningEffort,
  onSelectProvider,
  onSelectModel,
  onSelectReasoningEffort,
  attachments,
  onAttachmentsChange,
}: {
  ownerLabel: "Task" | "Session";
  value: string;
  onChange: (value: string) => void;
  onKeyDown: (event: KeyboardEvent<HTMLTextAreaElement>) => void;
  onSend: () => void;
  onQueue: () => void;
  onStop: () => void;
  onFinish: () => void;
  finishAvailable: boolean;
  sending: boolean;
  conversationState: ConversationState;
  running: boolean;
  queueAvailable: boolean;
  queueDisabled: boolean;
  providerSwitchRequested: boolean;
  providerSwitchAvailable: boolean;
  sendMode: ConversationSendMode;
  sendActionLabel: string;
  actionError: string | null;
  steerError?: string;
  replacementRecoveryAvailable: boolean;
  replacementSelected: boolean;
  onUseReplacement: () => void;
  continuationModelProviders: ModelProvider[];
  continuationModelProvider: string;
  continuationModelOverride: string;
  continuationModelOptions: string[];
  continuationReasoningEffort: string;
  onSelectProvider: (providerID: string) => void;
  onSelectModel: (model: string) => void;
  onSelectReasoningEffort: (effort: string) => void;
  attachments?: File[];
  onAttachmentsChange?: (files: File[]) => void;
}) {
  const attachmentInputRef = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (!attachments?.length && attachmentInputRef.current) attachmentInputRef.current.value = "";
  }, [attachments]);
  // The runtime is offline but resumable when the only admitted send mode is
  // resume: explain the state and its consequence instead of a bare badge.
  const runtimeOffline = sendMode === "resume";
  const turnPhase = conversationState.runtime.turn;
  const steeringPhase = conversationState.runtime.steering;
  const stopping = conversationState.request?.operation === "stop" && sending;
  return (
    <div data-testid="task-composer" className="fixed inset-x-0 bottom-0 z-30 shrink-0 bg-background/95 px-3 py-2 shadow-[0_-8px_24px] shadow-black/15 backdrop-blur-sm sm:px-4 md:static md:z-10 md:shadow-none lg:px-0">
      <div className="mx-auto max-w-3xl space-y-2">
        <div role="status" aria-live="polite" aria-atomic="true" aria-label="Conversation status"
          data-testid="conversation-status" data-turn-phase={turnPhase} data-steering-phase={steeringPhase} data-phase={conversationState.phase}
          className="flex min-h-5 items-center gap-2 text-xs text-muted-foreground">
          <span aria-hidden="true" className={`h-1.5 w-1.5 rounded-full ${turnPhase === "busy" ? "bg-primary motion-safe:animate-pulse" : "bg-muted-foreground/50"}`} />
          {conversationStatus(conversationState, turnPhase)}
        </div>
        {actionError && <p role="alert" className="text-xs text-destructive">{actionError}</p>}
        {runtimeOffline && (
          <div className="flex items-center gap-2 rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-[hsl(28_90%_32%)]">
            <PlugZap className="h-3.5 w-3.5 flex-none" />
            <span>The Runtime is offline. Sending a message resumes this {ownerLabel} with a new Runtime.</span>
          </div>
        )}
        {steeringPhase === "action_required" && (
          <div role="alert" className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-xs">
            <span>{steerError || "Steering needs operator action."}</span>
            {replacementRecoveryAvailable && (
              <Button type="button" size="sm" variant="outline" onClick={onUseReplacement} disabled={replacementSelected}>
                {replacementSelected ? "Replacement selected" : "Use interrupt and replace"}
              </Button>
            )}
          </div>
        )}
        <div className="overflow-hidden rounded-lg border border-border/80 bg-card shadow-sm focus-within:border-ring">
          <Textarea
            aria-label={`${ownerLabel} message`}
            name="task_message"
            value={value}
            onChange={(event) => onChange(event.target.value)}
            onKeyDown={onKeyDown}
            placeholder="Focus on admin.example.com next…"
            title="Enter to send; Shift+Enter for a new line"
            rows={2}
            autoComplete="off"
            className="max-h-40 min-h-[60px] resize-y rounded-none border-0 bg-transparent px-3 py-2.5 shadow-none focus-visible:border-transparent focus-visible:ring-0"
          />
          <div className="flex flex-wrap items-center gap-1.5 px-2 py-1.5 md:flex-nowrap">
            <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2 md:flex-nowrap">
              {onAttachmentsChange && (
                <label className="inline-flex h-7 cursor-pointer items-center gap-1 rounded-md border-0 bg-transparent px-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                  <Paperclip className="h-3.5 w-3.5" />
                  <span>{attachments?.length ? `${attachments.length} attached` : "Attach"}</span>
                  <input
                    ref={attachmentInputRef}
                    type="file"
                    multiple
                    className="sr-only"
                    aria-label="Attach files to Session message"
                    onChange={(event) => onAttachmentsChange(Array.from(event.target.files ?? []))}
                  />
                </label>
              )}
              {onAttachmentsChange && <span className="mx-1 h-4 w-px bg-border" />}
              <div className="relative max-w-full sm:max-w-[13rem]">
                <Select
                  size="sm"
                  className="h-7 min-w-0 w-auto max-w-full appearance-none border-0 bg-transparent px-1.5 pr-6 text-xs shadow-none focus-visible:ring-2"
                  name="continuation_model_provider"
                  value={continuationModelProvider}
                  onChange={(event) => onSelectProvider(event.target.value)}
                  aria-label="Continuation model provider"
                >
                  <option value="">Select model provider</option>
                  {continuationModelProviders.map((provider) => (
                    <option key={provider.id} value={provider.id}>{provider.name}</option>
                  ))}
                </Select>
                <ChevronsUpDown className="pointer-events-none absolute right-1 top-1/2 h-3 w-3 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
              </div>
              <div className="relative max-w-full sm:max-w-[13rem]">
                <Select
                  size="sm"
                  className="h-7 min-w-0 w-auto max-w-full appearance-none border-0 bg-transparent px-1.5 pr-6 text-xs font-medium shadow-none focus-visible:ring-2"
                  name="continuation_model"
                  value={continuationModelOverride}
                  onChange={(event) => onSelectModel(event.target.value)}
                  aria-label="Continuation model"
                  disabled={!continuationModelProvider || continuationModelOptions.length === 0}
                >
                  {continuationModelOptions.length === 0 ? (
                    <option value="">Default model</option>
                  ) : continuationModelOptions.map((model) => (
                    <option key={model} value={model}>{model}</option>
                  ))}
                </Select>
                <ChevronsUpDown className="pointer-events-none absolute right-1 top-1/2 h-3 w-3 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
              </div>
              <div className="relative w-[5.25rem] shrink-0">
                <Select
                  size="sm"
                  className="h-7 w-full appearance-none whitespace-nowrap border-0 bg-transparent px-1.5 pr-6 text-xs shadow-none focus-visible:ring-2"
                  name="continuation_reasoning_effort"
                  value={displayReasoningEffort(continuationReasoningEffort)}
                  onChange={(event) => onSelectReasoningEffort(event.target.value)}
                  aria-label="Continuation reasoning effort"
                >
                  {REASONING_EFFORT_VALUES.map((effort) => (
                    <option key={effort} value={effort}>{effort}</option>
                  ))}
                </Select>
                <ChevronsUpDown className="pointer-events-none absolute right-1 top-1/2 h-3 w-3 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
              </div>
              {!runtimeOffline && (
                <Badge variant={sendMode === "unavailable" ? "warning" : "outline"} size="sm">
                  {providerSwitchRequested ? "switch provider" : conversationModeText(sendMode)}
                </Badge>
              )}
              {steeringPhase === "pending" && (
                <Badge variant="warning" size="sm" data-testid="steer-pending-badge">
                  Steer pending
                </Badge>
              )}
              {steeringPhase === "failed" && (
                <Badge variant="destructive" size="sm" data-testid="steer-failed-badge">
                  Steer failed
                </Badge>
              )}
              {steeringPhase === "delivered" && (
                <Badge variant="outline" size="sm" title="Accepted by Runtime; current output can continue.">
                  Sent
                </Badge>
              )}
            </div>
            <div className="ml-auto flex shrink-0 items-center gap-1">
              {running && queueAvailable && sendMode !== "queue" && (
                <Button
                  size="icon-xl"
                  variant="ghost"
                  onClick={onQueue}
                  disabled={!value.trim() || sending || queueDisabled}
                  aria-label="Queue message"
                  title={queueDisabled ? "Queue cannot switch a Session model provider" : "Queue message"}
                >
                  <ListPlus className="h-4 w-4" />
                </Button>
              )}
              {running && finishAvailable && (
                <Button
                  size="icon-xl"
                  variant="ghost"
                  onClick={onFinish}
                  disabled={sending}
                  aria-label={`Finish ${ownerLabel.toLowerCase()}`}
                  title="Finish Task: close Runtime and mark completed"
                  data-testid="finish-task-composer"
                >
                  <Flag className="h-4 w-4" />
                </Button>
              )}
              {running && (
                <Button size="icon-xl" variant="destructive" onClick={onStop} disabled={stopping} aria-label={`Stop ${ownerLabel.toLowerCase()}`} title={`Stop ${ownerLabel}`}>
                  <Square className="h-4 w-4" />
                </Button>
              )}
              <Button
                data-testid="composer-send"
                size="icon-xl"
                onClick={onSend}
                disabled={!value.trim() || sending || sendMode === "unavailable" || (providerSwitchRequested && !providerSwitchAvailable)}
                aria-label={sendActionLabel}
                title={sendActionLabel}
              >
                {sending ? <Loader2 className="h-4 w-4 animate-spin motion-reduce:animate-none" /> : <ArrowUp className="h-4 w-4" />}
              </Button>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
