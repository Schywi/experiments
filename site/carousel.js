// Carousel controller for the pitch.
//
// The slides autoplay with a CSS animation; the first arrow click stops the
// animation for that carousel and hands the track to this script, because a
// stylesheet cannot move an animated track on demand.
//
// Loaded with <script src> rather than written inline in the markdown: the HTML
// pipeline escapes quotes in raw script text, and entities are not decoded
// inside a <script> element, so inline code arrives as a syntax error.

(() => {
  const wire = () => {
    document.querySelectorAll(".shots-wrap").forEach((wrap) => {
      if (wrap.dataset.wired) return;
      const track = wrap.querySelector(".track");
      if (!track) return;

      const slides = track.children.length;
      if (slides < 2) return;

      let index = 0;
      const go = (step) => {
        track.style.animation = "none";
        track.style.transition = "transform 0.3s ease";
        index = (index + step + slides) % slides;
        track.style.transform = `translateX(${-100 * index}%)`;
      };

      const prev = wrap.querySelector(".shots-nav.prev");
      const next = wrap.querySelector(".shots-nav.next");
      if (prev) prev.addEventListener("click", () => go(-1));
      if (next) next.addEventListener("click", () => go(1));

      wrap.dataset.wired = "1";
    });
  };

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", wire);
  } else {
    wire();
  }
  // Quartz swaps pages in an SPA and fires this after each swap.
  document.addEventListener("nav", wire);
})();
