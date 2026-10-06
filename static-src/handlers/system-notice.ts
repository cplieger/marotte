import { onSSE } from "../bus.js";
import { notice } from "../toast.js";
import { named, noticeSubject } from "../notice-subject.js";

onSSE("system_notice", (chatID, p) => {
  const text = p.message.trim();
  if (text === "") {
    return;
  }
  const subject = noticeSubject(chatID, p.chat_name ?? "");
  notice(named(subject, text), p.level, subject.open);
});
