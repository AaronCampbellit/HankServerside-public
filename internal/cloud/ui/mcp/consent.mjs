export function installMCPConsentForm(form) {
  if (!form) return;

  let submitted = false;
  form.addEventListener("submit", event => {
    if (submitted) {
      event.preventDefault();
      return;
    }

    const submitter = event.submitter;
    const decision = submitter?.value;
    if (decision !== "allow" && decision !== "deny") return;
    event.preventDefault();
    submitted = true;

    let preserved = form.querySelector("[data-mcp-consent-decision]");
    if (!preserved) {
      preserved = form.ownerDocument.createElement("input");
      preserved.type = "hidden";
      preserved.name = "decision";
      preserved.setAttribute("data-mcp-consent-decision", "");
      form.append(preserved);
    }
    preserved.value = decision;

    form.setAttribute("aria-busy", "true");
    for (const button of form.querySelectorAll('button[type="submit"]')) {
      button.disabled = true;
    }
    submitter.textContent = decision === "deny" ? "Declining…" : "Connecting…";
    form.submit();
  });
}

if (typeof document !== "undefined") {
  installMCPConsentForm(document.querySelector("[data-mcp-consent-form]"));
}
