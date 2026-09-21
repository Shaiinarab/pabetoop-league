// app.ts — client enhancement layer for the youth-football platform.
//
// Progressive enhancement ONLY: the server owns all business logic and renders
// Persian HTML; htmx 4 (vendored, loaded before this script) owns requests and
// DOM swaps. This layer adds keyboard flow and polish on top. Event names are
// htmx 4 style (`htmx:after:request`, `htmx:response:error`) — the htmx 2
// camelCase names do not exist in 4.x. See DECISIONS.md D2/D3/D4.
//
// Loaded at the end of admin pages; everything is delegated listeners, so
// rows swapped in by htmx keep working without re-initialization.

/* Minimal ambient view of the parts of the htmx 4 global this file needs.
   (htmx itself is vendored at web/static/js/htmx-4.0.0.min.js and loaded
   before this bundle — we never call it, we only listen to its events.) */
declare const htmx: {
  version: string;
};

const QUICK_ROW_SELECTOR = ".quick-entry-row";
const SCORE_INPUT_SELECTOR = ".score-input";

interface HtmxRequestDetail {
  // htmx 4 request-context detail; we only need the outcome.
  // `xhr` is absent on synthetic/error paths, hence optional.
  xhr?: XMLHttpRequest;
  elt?: Element;
}

function isHtmxRequestEvent(evt: Event): evt is CustomEvent<HtmxRequestDetail> {
  return evt instanceof CustomEvent;
}

/* ---------------------------------------------------------------- *
 * 1. Quick result entry: advance focus after a successful save
 * ---------------------------------------------------------------- */

function firstEmptyScoreInput(row: Element): HTMLInputElement | null {
  const inputs = row.querySelectorAll<HTMLInputElement>(SCORE_INPUT_SELECTOR);
  for (const input of inputs) {
    if (input.value.trim() === "") {
      return input;
    }
  }
  return null;
}

function advanceFocusAfterSave(savedRow: Element): void {
  const rows = Array.from(
    document.querySelectorAll<Element>(QUICK_ROW_SELECTOR),
  );
  const savedIndex = rows.indexOf(savedRow);
  if (savedIndex === -1) {
    return;
  }
  // Next rows in DOM order; within each, first empty .score-input.
  for (let i = savedIndex + 1; i < rows.length; i++) {
    const target = firstEmptyScoreInput(rows[i]!);
    if (target) {
      target.focus();
      return;
    }
  }
  // No empty target ahead: leave focus where the admin left it.
}

document.body.addEventListener("htmx:after:request", (evt) => {
  if (!isHtmxRequestEvent(evt)) {
    return;
  }
  const row =
    evt.target instanceof Element
      ? evt.target.closest<Element>(QUICK_ROW_SELECTOR)
      : null;
  if (!row) {
    return;
  }
  // A failed request also fires after:request; do not advance on failure —
  // the response:error handler below keeps the admin on the broken row.
  const status = evt.detail.xhr?.status ?? 200;
  if (status >= 400) {
    return;
  }
  advanceFocusAfterSave(row);
});

/* ---------------------------------------------------------------- *
 * 2. Quick result entry: Enter submits the row's form
 * ---------------------------------------------------------------- */

document.body.addEventListener("keydown", (evt) => {
  if (evt.key !== "Enter") {
    return;
  }
  const target = evt.target;
  if (!(target instanceof HTMLInputElement)) {
    return;
  }
  if (!target.classList.contains("score-input")) {
    return;
  }
  // Native <select> popups own Enter themselves; score inputs are <input>,
  // so this only fires when a real input has focus.
  const form = target.closest<HTMLFormElement>("form");
  if (!form) {
    return;
  }
  evt.preventDefault();
  form.requestSubmit();
});

/* ---------------------------------------------------------------- *
 * 3. Quick result entry: on response error, refocus the broken row
 * ---------------------------------------------------------------- */

document.body.addEventListener("htmx:response:error", (evt) => {
  if (!isHtmxRequestEvent(evt)) {
    return;
  }
  const row =
    evt.target instanceof Element
      ? evt.target.closest<Element>(QUICK_ROW_SELECTOR)
      : null;
  if (!row) {
    return;
  }
  const first = row.querySelector<HTMLInputElement>(SCORE_INPUT_SELECTOR);
  if (first) {
    first.focus();
    first.select();
  }
});

/* ---------------------------------------------------------------- *
 * 4. Flash auto-dismiss (success only)
 * ---------------------------------------------------------------- */

const FLASH_LIFETIME_MS = 4000;
const FLASH_FADE_MS = 300;

function dismissFlash(flash: Element): void {
  // CSS class toggle; a matching transition in main.css makes it fade.
  flash.classList.add("flash-out");
  window.setTimeout(() => {
    flash.remove();
  }, FLASH_FADE_MS);
}

document.querySelectorAll<Element>(".flash.success").forEach((flash) => {
  window.setTimeout(() => dismissFlash(flash), FLASH_LIFETIME_MS);
});

document.body.addEventListener("htmx:after:swap", () => {
  // Rows swapped in by htmx may carry fresh success flashes.
  document
    .querySelectorAll<Element>(".flash.success:not(.flash-out)")
    .forEach((flash) => {
      window.setTimeout(() => dismissFlash(flash), FLASH_LIFETIME_MS);
    });
});

/* ---------------------------------------------------------------- *
 * 5. Confirmation for destructive buttons
 * ---------------------------------------------------------------- */

const DEFAULT_CONFIRM_MESSAGE = "آیا مطمئن هستید؟";

document.body.addEventListener(
  "click",
  (evt) => {
    const btn = evt.target;
    if (!(btn instanceof Element)) {
      return;
    }
    const danger = btn.closest<HTMLButtonElement | HTMLAnchorElement | Element>(
      ".btn.danger",
    );
    if (!danger) {
      return;
    }
    const message =
      danger instanceof HTMLElement
        ? (danger.dataset.confirm ?? DEFAULT_CONFIRM_MESSAGE)
        : DEFAULT_CONFIRM_MESSAGE;
    if (!window.confirm(message)) {
      evt.preventDefault();
      evt.stopImmediatePropagation();
    }
  },
  true, // capture: decide before any form/htmx submit handler runs
);

export {};
