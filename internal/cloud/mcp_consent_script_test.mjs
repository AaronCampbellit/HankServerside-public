import assert from "node:assert/strict";
import test from "node:test";

import { installMCPConsentForm } from "./ui/mcp/consent.mjs";

test("consent submission preserves the decision and blocks duplicate posts", () => {
  const buttons = [
    { value: "deny", disabled: false, textContent: "Deny" },
    { value: "allow", disabled: false, textContent: "Allow" },
  ];
  const appended = [];
  let nativeSubmissions = 0;
  let listener;
  const form = {
    ownerDocument: { createElement: () => ({ setAttribute() {} }) },
    addEventListener: (_name, value) => { listener = value; },
    append: value => appended.push(value),
    querySelector: () => undefined,
    querySelectorAll: () => buttons,
    submit: () => { nativeSubmissions += 1; },
    setAttribute(name, value) { this[name] = value; },
  };

  installMCPConsentForm(form);
  let prevented = false;
  listener({ submitter: buttons[1], preventDefault: () => { prevented = true; } });

  assert.equal(prevented, true);
  assert.equal(nativeSubmissions, 1);
  assert.equal(appended[0].name, "decision");
  assert.equal(appended[0].value, "allow");
  assert.equal(form["aria-busy"], "true");
  assert.deepEqual(buttons.map(button => button.disabled), [true, true]);
  assert.equal(buttons[1].textContent, "Connecting…");

  listener({ submitter: buttons[1], preventDefault: () => { prevented = true; } });
  assert.equal(prevented, true);
  assert.equal(nativeSubmissions, 1);
  assert.equal(appended.length, 1);
});
