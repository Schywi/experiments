// Pitch interactions: carousel (arrows, dots, autoplay) and the image lightbox.
//
// Loaded with <script src> rather than written inline in the markdown, because
// the HTML pipeline escapes quotes in raw script text and entities are not
// decoded inside a <script> element.
//
// The carousel is driven from here rather than by the CSS keyframes so that
// arrows, dots and autoplay all read one index. The CSS animation stays as a
// no-JS fallback.

(() => {
  const REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  const AUTOPLAY_MS = 5000;

  const wireCarousels = () => {
    document.querySelectorAll(".shots-wrap").forEach((wrap) => {
      if (wrap.dataset.wired) return;
      const track = wrap.querySelector(".track");
      if (!track) return;

      const slides = track.children.length;
      if (slides < 2) return;

      let index = 0;
      let timer = null;

      // Stop the CSS fallback animation: this script owns the transform now.
      track.style.animation = "none";
      track.style.transition = "transform 0.3s ease";

      const dots = document.createElement("div");
      dots.className = "shots-dots";
      dots.setAttribute("role", "tablist");
      dots.setAttribute("aria-label", "Slides");

      const counter = document.createElement("span");
      counter.className = "shots-counter";

      const buttons = [];
      for (let i = 0; i < slides; i += 1) {
        const dot = document.createElement("button");
        dot.type = "button";
        dot.className = "shots-dot";
        dot.setAttribute("role", "tab");
        dot.setAttribute("aria-label", `Slide ${i + 1} of ${slides}`);
        dot.addEventListener("click", () => {
          show(i);
          stop();
        });
        dots.appendChild(dot);
        buttons.push(dot);
      }
      dots.appendChild(counter);
      wrap.appendChild(dots);

      const show = (next) => {
        index = (next + slides) % slides;
        track.style.transform = `translateX(${-100 * index}%)`;
        buttons.forEach((dot, i) => {
          const active = i === index;
          dot.classList.toggle("is-active", active);
          if (active) {
            dot.setAttribute("aria-current", "true");
          } else {
            dot.removeAttribute("aria-current");
          }
        });
        counter.textContent = `${index + 1} / ${slides}`;
      };

      const start = () => {
        if (REDUCED || timer) return;
        timer = window.setInterval(() => show(index + 1), AUTOPLAY_MS);
      };
      const stop = () => {
        if (timer) {
          window.clearInterval(timer);
          timer = null;
        }
      };

      const prev = wrap.querySelector(".shots-nav.prev");
      const next = wrap.querySelector(".shots-nav.next");
      if (prev) {
        prev.addEventListener("click", () => {
          show(index - 1);
          stop();
        });
      }
      if (next) {
        next.addEventListener("click", () => {
          show(index + 1);
          stop();
        });
      }

      wrap.addEventListener("mouseenter", stop);
      wrap.addEventListener("focusin", stop);
      wrap.addEventListener("mouseleave", start);
      wrap.addEventListener("focusout", start);

      show(0);
      start();
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
