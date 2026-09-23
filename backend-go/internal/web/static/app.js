// Dashboard helpers: theme toggle + copy-to-clipboard buttons.
(function () {
  "use strict";

  document.addEventListener("click", function (ev) {
    var btn = ev.target.closest("#theme-toggle, [data-copy], [data-copy-text], [data-copy-perma]");
    if (!btn) return;

    if (btn.id === "theme-toggle") {
      var root = document.documentElement;
      var dark = root.dataset.theme === "dark";
      if (dark) {
        delete root.dataset.theme;
      } else {
        root.dataset.theme = "dark";
      }
      localStorage.setItem("vq-theme", dark ? "light" : "dark");
      return;
    }

    // data-copy-perma copies the page URL; data-copy-text a literal value;
    // data-copy="#selector" the target element's text.
    var text;
    if (btn.dataset.copyPerma !== undefined) {
      text = location.href;
    } else {
      text = btn.dataset.copyText;
    }
    if (!text && btn.dataset.copy) {
      var el = document.querySelector(btn.dataset.copy);
      text = el ? el.textContent : "";
    }
    if (!text) return;
    navigator.clipboard.writeText(text).then(function () {
      var old = btn.textContent;
      btn.textContent = "Copied";
      setTimeout(function () { btn.textContent = old; }, 1200);
    });
  });
})();
