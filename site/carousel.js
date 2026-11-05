// Pitch interactions: carousel arrows and the image lightbox.
//
// Loaded with <script src> rather than written inline in the markdown, because
// the HTML pipeline escapes quotes in raw script text and entities are not
// decoded inside a <script> element.

(() => {
  // ── carousel ──────────────────────────────────────────────────────────────
  // Slides autoplay with a CSS animation; the first arrow click stops it for
  // that carousel and hands the track to this script, because a stylesheet
  // cannot move an animated track on demand.
  const wireCarousels = () => {
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

  // ── lightbox ──────────────────────────────────────────────────────────────
  // One overlay reused for every figure. Click an image to inspect it, click
  // anywhere or press Escape to close.
  const BOX_ID = "quartz-lightbox";

  const ensureLightbox = () => {
    let box = document.getElementById(BOX_ID);
    if (box) return box;

    box = document.createElement("div");
    box.id = BOX_ID;
    box.className = "lightbox";
    box.setAttribute("role", "dialog");
    box.setAttribute("aria-modal", "true");
    box.setAttribute("aria-label", "Image preview");

    const big = document.createElement("img");
    // Clicking the picture must not close the overlay it sits in.
    big.addEventListener("click", (event) => event.stopPropagation());
    box.appendChild(big);

    const close = () => {
      box.classList.remove("open");
      box.setAttribute("aria-hidden", "true");
      big.removeAttribute("src");
    };

    box.addEventListener("click", close);
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") close();
    });

    document.body.appendChild(box);
    return box;
  };

  const wireLightbox = () => {
    const box = ensureLightbox();
    const big = box.querySelector("img");

    document.querySelectorAll(".center figure img").forEach((img) => {
      if (img.dataset.zoom) return;
      img.dataset.zoom = "1";
      img.addEventListener("click", () => {
        big.src = img.currentSrc || img.src;
        big.alt = img.alt || "";
        box.classList.add("open");
        box.setAttribute("aria-hidden", "false");
      });
    });
  };

  const wire = () => {
    wireCarousels();
    wireLightbox();
  };

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", wire);
  } else {
    wire();
  }
  // Quartz swaps pages in an SPA and fires this after each swap.
  document.addEventListener("nav", wire);
})();
