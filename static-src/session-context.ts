// Session context: the ambient model the user is currently composing with, and the reasoning-effort
// level they last picked. Separated from app.ts so multiple modules can read/write without going
// through the orchestrator.

import { patchSettings } from "./persist.js";

class SessionContextController {
  private currentModel = "auto";
  private lastModelCache = "auto";
  /** A model with no entry has nothing for a new chat to open with and falls through to that model's
   *  own default tier. */
  private lastEffortByModel: Record<string, string> = {};

  getCurrentModel(): string {
    return this.currentModel;
  }
  setCurrentModel(id: string): void {
    this.currentModel = id;
  }

  getLastModel(): string {
    return this.lastModelCache;
  }
  setLastModel(id: string): void {
    this.lastModelCache = id;
    void patchSettings({ last_model: id });
  }

  restoreLastModel(id: string | undefined): void {
    if (id !== undefined) {
      this.lastModelCache = id;
    }
  }

  getLastEffortFor(model: string): string {
    // A model id is arbitrary text, so an inherited member (`constructor`) must answer like an
    // absent one rather than like an entry.
    if (model === "" || !Object.hasOwn(this.lastEffortByModel, model)) {
      return "";
    }
    return this.lastEffortByModel[model] ?? "";
  }
  restoreLastEffort(byModel: Record<string, string> | undefined): void {
    if (byModel !== undefined) {
      this.lastEffortByModel = { ...byModel };
    }
  }
}

const instance = new SessionContextController();

export function getCurrentModel(): string {
  return instance.getCurrentModel();
}
export function setCurrentModel(id: string): void {
  instance.setCurrentModel(id);
}

export function getLastModel(): string {
  return instance.getLastModel();
}
export function setLastModel(id: string): void {
  instance.setLastModel(id);
}

/** Adopt last_model from settings the server holds. Cache-only: the server's value is no pick to
 *  write back, and writing it would undo a pick still on its way. */
export function restoreLastModel(id: string | undefined): void {
  instance.restoreLastModel(id);
}

/** The level remembered for `model`; "" when that model has none. The per-model lookup is what
 *  stops a tier chosen on one model overriding another model's default — the seed's two readers
 *  (this and the server's effortFor) apply the same scope or the pill lies about what the
 *  session runs. */
export function getLastEffortFor(model: string): string {
  return instance.getLastEffortFor(model);
}

/** Adopt last_effort_by_model from settings on startup and from the settings_updated SSE.
 *  Cache-only: the SEED is written by the server, inside the set_effort command that justifies
 *  it, so this side never patches. */
export function restoreLastEffort(byModel: Record<string, string> | undefined): void {
  instance.restoreLastEffort(byModel);
}
