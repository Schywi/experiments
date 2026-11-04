import { PageLayout, SharedLayout } from "./quartz/cfg"
import * as Component from "./quartz/components"

// components shared across all pages
export const sharedPageComponents: SharedLayout = {
  head: Component.Head(),
  header: [],
  afterBody: [],
  footer: Component.Footer({
    links: {
      GitHub: "https://github.com/Schywi/experiments",
      Roadmap: "https://github.com/Schywi/experiments/blob/cluster-evolution/ROADMAP.md",
    },
  }),
}

// components for pages that display a single page (e.g. a single note)
export const defaultContentPageLayout: PageLayout = {
  beforeBody: [
    Component.ConditionalRender({
      component: Component.Breadcrumbs(),
      condition: (page) => page.fileData.slug !== "index",
    }),
    Component.ArticleTitle(),
    // Dates come from frontmatter only. Quartz stamps the build time on any page
    // that has none, which put "Nov 04, 2026" on the landing page, the root
    // README and the ROADMAP — pages whose date means nothing.
    Component.ConditionalRender({
      component: Component.ContentMeta(),
      condition: (page) => !["index", "README", "ROADMAP"].includes(page.fileData.slug ?? ""),
    }),
    Component.TagList(),
  ],
  left: [
    Component.PageTitle(),
    Component.MobileOnly(Component.Spacer()),
    Component.Flex({
      components: [
        {
          Component: Component.Search(),
          grow: true,
        },
        { Component: Component.Darkmode() },
        { Component: Component.ReaderMode() },
      ],
    }),
    Component.Explorer(),
  ],
  right: [
    Component.Graph({
      localGraph: { depth: 2, scale: 1.1, linkDistance: 40, fontSize: 0.55, showTags: true },
      globalGraph: { scale: 0.7, linkDistance: 60, fontSize: 0.5, showTags: true, focusOnHover: true },
    }),
    Component.DesktopOnly(Component.TableOfContents()),
    Component.Backlinks(),
  ],
}

// components for pages that display lists of pages  (e.g. tags or folders)
export const defaultListPageLayout: PageLayout = {
  // No date chip on list pages: a tag or folder listing has no date of its own,
  // and the fallback was the build time.
  beforeBody: [Component.Breadcrumbs(), Component.ArticleTitle()],
  left: [
    Component.PageTitle(),
    Component.MobileOnly(Component.Spacer()),
    Component.Flex({
      components: [
        {
          Component: Component.Search(),
          grow: true,
        },
        { Component: Component.Darkmode() },
      ],
    }),
    Component.Explorer(),
  ],
  right: [],
}
