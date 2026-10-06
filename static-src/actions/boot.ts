// Wires @cplieger/actions to marotte's toast, api-client and transport layers. Import once at
// startup (app.ts) before any action dispatch.

import { configure, configureTransport } from "@cplieger/actions";
import { actionNotice, subjectName } from "../notice-subject.js";
import { configureSubjectNotice, takeSubject } from "./subject.js";
import { send as transportSend } from "../transport.js";

export function initActions(): void {
  configure({
    success: (msg) => {
      const { subject, name } = takeSubject();
      actionNotice(subject, msg, "success", undefined, name);
    },
    error: (msg, retry) => {
      const { subject, name } = takeSubject();
      actionNotice(subject, msg, "error", retry, name);
    },
  });
  configureSubjectNotice(actionNotice, subjectName);

  configureTransport(async (cmd, { signal }) => {
    const r = await transportSend(cmd as Parameters<typeof transportSend>[0], {
      signal,
      reportSendState: false,
    });
    return r;
  });
}
