/**
 * Compatibility re-exports. Finding/Evidence/Report/Solution consumers use
 * @/lib/blackboardv2 semantic DTOs and key-based detail only (#120).
 */
export {
  qs,
  recordHref,
  projectBlackboardV2Base as projectBlackboardBase,
} from "@/lib/blackboardv2";
