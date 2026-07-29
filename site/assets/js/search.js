(function () {
  const input = document.getElementById("site-search");
  const results = document.getElementById("search-results");
  const status = document.getElementById("search-status");

  if (!input || !results) {
    return;
  }

  let indexPromise;
  let timer;

  function loadIndex() {
    if (!indexPromise) {
      indexPromise = fetch(window.SpotWuSearchIndex || "search-index.json")
        .then((response) => response.ok ? response.json() : { items: [] })
        .catch(() => ({ items: [] }));
    }
    return indexPromise;
  }

  function normalize(value) {
    return String(value || "")
      .normalize("NFD")
      .replace(/[\u0300-\u036f]/g, "")
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, " ")
      .trim();
  }

  function render(items) {
    results.innerHTML = "";
    if (!items.length) {
      results.textContent = results.dataset.empty || "No matches.";
      if (status) {
        status.textContent = "No search results.";
      }
      return;
    }

    const fragment = document.createDocumentFragment();
    for (const item of items.slice(0, 12)) {
      const entry = document.createElement("a");
      entry.className = "search-result";
      entry.href = siteURL(item.url);
      entry.innerHTML = "<strong></strong><br><span></span>";
      entry.querySelector("strong").textContent = item.title;
      entry.querySelector("span").textContent = `${item.type}${item.subtitle ? " / " + item.subtitle : ""}`;
      fragment.appendChild(entry);
    }
    results.appendChild(fragment);
    if (status) {
      status.textContent = `${Math.min(items.length, 12)} search results.`;
    }
  }

  input.addEventListener("input", function () {
    clearTimeout(timer);
    timer = setTimeout(function () {
      const query = normalize(input.value);
      if (!query) {
        results.innerHTML = "";
        if (status) {
          status.textContent = "";
        }
        return;
      }

      loadIndex().then((data) => {
        const matches = (data.items || []).filter((item) => {
          const haystack = normalize([item.title, item.subtitle, ...(item.terms || [])].join(" "));
          return haystack.includes(query);
        });
        render(matches);
      });
    }, 160);
  });

  document.addEventListener("change", function (event) {
    const select = event.target.closest("[data-filter-target]");
    if (!select) {
      return;
    }
    const target = document.getElementById(select.dataset.filterTarget);
    if (!target) {
      return;
    }
    const value = select.value;
    for (const card of target.querySelectorAll("[data-category]")) {
      card.hidden = value && card.dataset.category !== value;
    }
  });

  for (const list of document.querySelectorAll("[data-random-artist-list]")) {
    const items = Array.from(list.querySelectorAll("[data-random-artist-item]"));
    const limit = Number.parseInt(list.dataset.randomLimit || "", 10) || items.length;
    shuffle(items);
    for (const [index, item] of items.entries()) {
      item.hidden = index >= limit;
      list.appendChild(item);
    }
  }

  function shuffle(items) {
    for (let index = items.length - 1; index > 0; index -= 1) {
      const swapIndex = Math.floor(Math.random() * (index + 1));
      [items[index], items[swapIndex]] = [items[swapIndex], items[index]];
    }
  }

  function siteURL(path) {
    const base = window.SpotWuBasePath || "/";
    return base.replace(/\/$/, "") + "/" + String(path || "").replace(/^\//, "");
  }
})();
