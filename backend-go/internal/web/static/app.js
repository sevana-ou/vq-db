// Dashboard helpers: theme toggle + copy-to-clipboard buttons.
(function () {
  "use strict";

  // A "Play" button becomes the stream's audio player; any other player on the
  // page is paused, so two streams of a call do not talk over each other
  // unless started on purpose.
  document.addEventListener("click", function (ev) {
    var play = ev.target.closest("[data-play-audio]");
    if (!play) return;
    document.querySelectorAll("audio").forEach(function (a) { a.pause(); });
    var audio = document.createElement("audio");
    audio.controls = true;
    audio.autoplay = true;
    audio.className = "row-audio";
    audio.src = play.dataset.playAudio;
    audio.addEventListener("error", function () {
      var note = document.createElement("span");
      note.className = "muted";
      note.textContent = "No audio";
      audio.replaceWith(note);
    });
    play.replaceWith(audio);
  });

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
