import type { Skill } from "@/lib/api";

export function profileSkillEnabled(skill: Skill) {
  if (typeof skill.profile_opted_out === "boolean") return !skill.profile_opted_out;
  return skill.enabled;
}

export function sourceLabel(skill: Skill) {
  const source = skill.source_provenance;
  if (source?.kind === "builtin") return "";
  if (!source || (!source.kind && !source.package && !source.ref)) return "manual";
  if (source.package && source.ref) return `${source.package}@${source.ref}`;
  if (source.package) return source.package;
  return source.kind ?? "manual";
}

export function displaySkillId(skill: Skill) {
  if (skill.source_provenance?.kind !== "builtin") return skill.id;
  return skill.id.replace(/^(cyberstrikeai|strix)-/, "");
}

export function displaySkillName(skill: Skill) {
  if (skill.source_provenance?.kind !== "builtin") return skill.name;
  return skill.name.replace(/^(cyberstrikeai|strix)-/, "");
}

export function displaySkillDescription(skill: Skill) {
  const description = skill.description ?? "";
  if (skill.source_provenance?.kind !== "builtin") return description;
  return stripBuiltinSourcePrefix(description, skill);
}

function stripBuiltinSourcePrefix(value: string, skill: Skill) {
  let next = value.trim();
  for (const prefix of [skill.id, "cyberstrikeai", "strix", "Ed1s0nZ/CyberStrikeAI", "usestrix/strix"]) {
    if (next.toLowerCase().startsWith(prefix.toLowerCase())) {
      next = next.slice(prefix.length).replace(/^[\s:—–-]+/, "").trim();
    }
  }
  return next;
}
