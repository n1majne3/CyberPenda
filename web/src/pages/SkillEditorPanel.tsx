import { Download, Plus, X } from "lucide-react";
import { buttonVariants } from "@/components/buttonVariants";
import { Button, Input, Label, Textarea } from "@/components/ui";
import type { Skill } from "@/lib/api";
import { cn } from "@/lib/utils";

export type SkillForm = {
  id: string;
  storage_id?: string;
  name: string;
  description: string;
  instruction: string;
  extra_files: string;
  source_provenance?: Skill["source_provenance"];
};

export type FormMode = "idle" | "create" | "edit";

export function SkillEditorPanel({
  form,
  formMode,
  archiveFile,
  importOpen,
  importPackage,
  saving,
  cancelForm,
  importArchive,
  importSkill,
  publishSkill,
  setArchiveFile,
  setForm,
  setImportPackage,
  toggleImportOpen,
  startCreate,
}: {
  form: SkillForm;
  formMode: FormMode;
  archiveFile: File | null;
  importOpen: boolean;
  importPackage: string;
  saving: boolean;
  cancelForm: () => void;
  importArchive: () => Promise<void>;
  importSkill: () => Promise<void>;
  publishSkill: () => Promise<void>;
  setArchiveFile: (file: File | null) => void;
  setForm: (form: SkillForm) => void;
  setImportPackage: (source: string) => void;
  toggleImportOpen: () => void;
  startCreate: () => void;
}) {
  return (
    <aside
      data-testid="skills-form-panel"
      className="flex min-w-0 flex-col gap-4 lg:min-h-0 lg:overflow-y-auto lg:overscroll-contain"
    >
      <div className="rounded-lg border border-border bg-card p-4 shadow-sm">
        <div className="flex items-center justify-between gap-3">
          <h3 className="text-sm font-medium">
            {formMode === "edit" ? "Edit Skill" : "Upload / edit Skill"}
          </h3>
          {formMode !== "idle" && (
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={cancelForm}
              aria-label="Cancel skill form"
            >
              <X className="h-3.5 w-3.5" />
            </Button>
          )}
        </div>
        <p className="mt-1 text-xs text-muted-foreground">
          Publish one canonical bundle atomically. Reusing a Skill ID updates it.
        </p>

        {formMode === "idle" ? (
          <div className="mt-4 space-y-3">
            <div className="space-y-3 rounded-md border border-border bg-muted/20 p-3">
              <div>
                <Label>Skill bundle archive</Label>
                <div className="mt-1.5 flex h-9 min-w-0 items-center gap-2 rounded-lg border border-input bg-background px-1.5">
                  <input
                    id="skill-bundle-archive"
                    name="skill_bundle_archive"
                    type="file"
                    accept=".zip,.tar,.tar.gz,.tgz,application/zip,application/x-tar,application/gzip"
                    aria-label="Skill bundle archive"
                    onChange={(event) => setArchiveFile(event.target.files?.[0] ?? null)}
                    className="peer sr-only"
                  />
                  <label
                    htmlFor="skill-bundle-archive"
                    className={cn(
                      buttonVariants({ variant: "outline", size: "sm" }),
                      "h-7 shrink-0 cursor-pointer px-2.5 text-xs peer-focus-visible:ring-2 peer-focus-visible:ring-ring peer-focus-visible:ring-offset-2",
                    )}
                  >
                    Choose file
                  </label>
                  <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
                    {archiveFile?.name ?? "No file selected"}
                  </span>
                </div>
                <p className="mt-1 text-xs text-muted-foreground">
                  Upload one .zip, .tar, .tar.gz, or .tgz bundle containing SKILL.md and optional scripts.
                </p>
              </div>
              <Button
                variant="outline"
                onClick={importArchive}
                disabled={saving || !archiveFile}
                className="w-full"
              >
                <Download className="h-4 w-4" /> Upload archive
              </Button>
            </div>
            <Button onClick={startCreate} className="w-full justify-start">
              <Plus className="h-4 w-4" /> New skill
            </Button>
          </div>
        ) : (
          <>
            <div className="mt-4 space-y-3">
              <div>
                <Label htmlFor="skill-id">Skill ID</Label>
                <Input
                  id="skill-id"
                  name="skill_id"
                  value={form.id}
                  onChange={(e) =>
                    setForm({
                      ...form,
                      id: e.target.value,
                      storage_id: undefined,
                      source_provenance: undefined,
                    })
                  }
                  placeholder="recon-helper…"
                  autoComplete="off"
                  spellCheck={false}
                />
              </div>
              <div>
                <Label htmlFor="skill-name">Name</Label>
                <Input
                  id="skill-name"
                  name="skill_name"
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="Recon Helper…"
                  autoComplete="off"
                />
              </div>
              <div>
                <Label htmlFor="skill-description">Description</Label>
                <Input
                  id="skill-description"
                  name="skill_description"
                  value={form.description}
                  onChange={(e) => setForm({ ...form, description: e.target.value })}
                  autoComplete="off"
                />
              </div>
              <div>
                <Label htmlFor="skill-instruction">SKILL.md</Label>
                <Textarea
                  id="skill-instruction"
                  name="skill_instruction"
                  value={form.instruction}
                  onChange={(e) => setForm({ ...form, instruction: e.target.value })}
                  autoComplete="off"
                  spellCheck={false}
                  size="lg"
                  className="font-mono text-xs leading-relaxed"
                />
              </div>
              <div>
                <Label htmlFor="skill-extra-files">Additional files JSON</Label>
                <Textarea
                  id="skill-extra-files"
                  name="skill_extra_files"
                  value={form.extra_files}
                  onChange={(e) => setForm({ ...form, extra_files: e.target.value })}
                  placeholder={'{"scripts/probe.sh":"#!/bin/sh\\n"}…'}
                  autoComplete="off"
                  spellCheck={false}
                  className="font-mono text-xs"
                />
              </div>
            </div>
            <div className="mt-4 flex gap-2">
              <Button
                onClick={publishSkill}
                disabled={saving || !form.id.trim() || !form.name.trim()}
                className="flex-1"
              >
                Publish Skill
              </Button>
              <Button variant="outline" onClick={cancelForm} disabled={saving}>
                Cancel
              </Button>
            </div>
          </>
        )}
      </div>

      <div className="rounded-lg border border-dashed border-border p-4">
        <button
          type="button"
          className="flex w-full items-center gap-2 text-left text-sm font-medium"
          onClick={toggleImportOpen}
          aria-expanded={importOpen}
        >
          <Download className="h-4 w-4" /> Import skill from URL or repo
        </button>
        <p className="mt-1.5 text-xs text-muted-foreground">
          Structured imports let the daemon parse the source itself and never accept raw shell commands.
        </p>
        {importOpen && (
          <ImportForm
            importPackage={importPackage}
            saving={saving}
            onPackageChange={setImportPackage}
            onImport={importSkill}
          />
        )}
      </div>
    </aside>
  );
}

function ImportForm({
  importPackage,
  saving,
  onPackageChange,
  onImport,
}: {
  importPackage: string;
  saving: boolean;
  onPackageChange: (value: string) => void;
  onImport: () => void;
}) {
  return (
    <div className="space-y-3 border-t border-border pt-3">
      <p className="text-xs text-muted-foreground">
        Structured import only; the daemon resolves the source itself and never accepts raw shell commands from this form.
      </p>
      <div>
        <Label htmlFor="import-package">Skill source</Label>
        <Input
          id="import-package"
          name="import_package"
          value={importPackage}
          onChange={(e) => onPackageChange(e.target.value)}
          placeholder="https://example.com/skills/@acme/skill  •  owner/repo  •  github.com/owner/repo"
          autoComplete="off"
          spellCheck={false}
        />
      </div>
      <Button onClick={onImport} disabled={saving || !importPackage.trim()}>
        <Download className="h-4 w-4" /> Import
      </Button>
    </div>
  );
}
