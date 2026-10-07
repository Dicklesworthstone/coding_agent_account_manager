import type { ActivityEvent } from "./api";

/** One line describing an activity log entry. */
export function describeEvent(e: ActivityEvent): string {
  const target = `${e.tool} ${e.profile}`.trim();
  const from = typeof e.details?.from === "string" ? e.details.from : "";
  const reason = typeof e.details?.reason === "string" ? e.details.reason.replaceAll("_", " ") : "";
  switch (e.type) {
    case "activate":
      return `Activated ${target}`;
    case "switch":
      return `Switched ${e.tool} ${from ? `from ${from} ` : ""}to ${e.profile}${reason ? ` (${reason})` : ""}`;
    case "refresh":
      return `Refreshed the token of ${target}`;
    case "login":
      return `Logged in ${target}`;
    case "deactivate":
      return `Deactivated ${target}`;
    case "error": {
      const message = typeof e.details?.error === "string" ? `: ${e.details.error}` : "";
      return `Error on ${target}${message}`;
    }
    default:
      return `${e.type} ${target}`;
  }
}
