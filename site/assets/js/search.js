(function () {
  const input = document.getElementById("site-search");
  const results = document.getElementById("search-results");
  const status = document.getElementById("search-status");

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

  if (input && results) {
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
  }

  document.addEventListener("change", function (event) {
    const select = event.target.closest("[data-filter-target]");
    if (!select) {
      return;
    }
    applyFilter(select);
  });

  for (const select of document.querySelectorAll("[data-filter-target]")) {
    const category = new URLSearchParams(window.location.search).get("category");
    if (category) {
      select.value = category;
    }
    applyFilter(select);
  }

  function applyFilter(select) {
    const target = document.getElementById(select.dataset.filterTarget);
    if (!target) {
      return;
    }
    const value = select.value;
    for (const card of target.querySelectorAll("[data-category]")) {
      card.hidden = value && card.dataset.category !== value;
    }
  }

  for (const form of document.querySelectorAll("[data-artist-filters]")) {
    setupArtistFilters(form);
  }

  function setupArtistFilters(form) {
    const rows = Array.from(document.querySelectorAll("[data-artist-row]"));
    const queryInput = form.querySelector('[data-artist-filter="query"]');
    const categorySelect = form.querySelector('[data-artist-filter="category"]');
    const count = form.querySelector("[data-artist-filter-count]");
    const category = new URLSearchParams(window.location.search).get("category");
    if (category && categorySelect) {
      categorySelect.value = category;
    }

    function applyArtistFilters() {
      const query = normalize(queryInput ? queryInput.value : "");
      const categoryValue = categorySelect ? categorySelect.value : "";
      let visible = 0;

      for (const row of rows) {
        const matchesQuery = !query || normalize(row.dataset.artistText).includes(query);
        const matchesCategory = !categoryValue || row.dataset.category === categoryValue;
        const matches = matchesQuery && matchesCategory;
        row.hidden = !matches;
        if (matches) {
          visible += 1;
        }
      }

      if (count) {
        count.textContent = `${visible} of ${rows.length} artists`;
      }
    }

    form.addEventListener("input", applyArtistFilters);
    form.addEventListener("change", applyArtistFilters);
    form.addEventListener("reset", function () {
      window.setTimeout(applyArtistFilters, 0);
    });
    applyArtistFilters();
  }

  for (const form of document.querySelectorAll("[data-track-filters]")) {
    setupTrackFilters(form);
  }

  function setupTrackFilters(form) {
    const rows = Array.from(document.querySelectorAll("[data-track-row]"));
    const filters = Array.from(form.querySelectorAll("[data-track-filter]"));
    const count = form.querySelector("[data-track-filter-count]");

    function applyTrackFilters() {
      const values = Object.fromEntries(filters.map((filter) => [filter.dataset.trackFilter, filter.value]));
      const query = normalize(values.query);
      let visible = 0;

      for (const row of rows) {
        const matchesQuery = !query || normalize(row.dataset.trackText).includes(query);
        const matchesGroup = !values.group || tokenList(row.dataset.trackGroups).includes(values.group);
        const matchesYear = !values.year || tokenList(row.dataset.trackYears).includes(values.year);
        const matchesExplicit = !values.explicit || row.dataset.trackExplicit === values.explicit;
        const matches = matchesQuery && matchesGroup && matchesYear && matchesExplicit;
        row.hidden = !matches;
        if (matches) {
          visible += 1;
        }
      }

      if (count) {
        count.textContent = `${visible} of ${rows.length} tracks`;
      }
    }

    form.addEventListener("input", applyTrackFilters);
    form.addEventListener("change", applyTrackFilters);
    form.addEventListener("reset", function () {
      window.setTimeout(applyTrackFilters, 0);
    });
    applyTrackFilters();
  }

  function tokenList(value) {
    return String(value || "").trim().split(/\s+/).filter(Boolean);
  }

  for (const list of document.querySelectorAll("[data-random-artist-list]")) {
    const items = Array.from(list.querySelectorAll("[data-random-artist-item]"));
    const limit = Number.parseInt(list.dataset.randomLimit || "", 10) || items.length;
    shuffle(items);
    for (const [index, item] of items.entries()) {
      item.hidden = index >= limit;
      list.appendChild(item);
    }
  }

  for (const panel of document.querySelectorAll("[data-random-mix]")) {
    setupRandomMix(panel);
  }

  function setupRandomMix(panel) {
    const list = panel.querySelector("[data-random-list]");
    const player = panel.querySelector("[data-random-player]");
    const empty = panel.querySelector("[data-random-empty]");
    const refresh = panel.querySelector("[data-random-refresh]");
    const spotifyLink = panel.querySelector("[data-random-spotify]");
    const limit = Number.parseInt(panel.dataset.randomLimit || "", 10) || 12;
    let tracks = [];

    function renderMix() {
      const mix = shuffle(tracks.slice()).slice(0, limit);
      list.innerHTML = "";
      if (!mix.length) {
        if (empty) {
          empty.hidden = false;
        }
        player.innerHTML = "";
        return;
      }
      if (empty) {
        empty.hidden = true;
      }

      const fragment = document.createDocumentFragment();
      let firstTrack;
      for (const [index, track] of mix.entries()) {
        const item = document.createElement("li");
        item.className = "mix-track";
        item.innerHTML = '<button type="button"><span></span><small></small></button>';
        const button = item.querySelector("button");
        button.dataset.spotifyId = track.id;
        button.querySelector("span").textContent = track.title;
        button.querySelector("small").textContent = track.subtitle || "Spotify track";
        button.addEventListener("click", function () {
          playTrack(track);
        });
        fragment.appendChild(item);
        if (index === 0) {
          firstTrack = track;
        }
      }
      list.appendChild(fragment);
      if (firstTrack) {
        playTrack(firstTrack);
      }
    }

    function playTrack(track) {
      player.innerHTML = "";
      const iframe = document.createElement("iframe");
      iframe.className = "spotify-embed spotify-track-embed";
      iframe.title = `Spotify track player for ${track.title}`;
      iframe.src = `https://open.spotify.com/embed/track/${encodeURIComponent(track.id)}?utm_source=generator`;
      iframe.loading = "lazy";
      iframe.allow = "autoplay; clipboard-write; encrypted-media; fullscreen; picture-in-picture";
      player.appendChild(iframe);
      if (spotifyLink) {
        spotifyLink.href = `https://open.spotify.com/track/${encodeURIComponent(track.id)}`;
      }

      for (const button of list.querySelectorAll("button")) {
        button.classList.toggle("is-active", button.dataset.spotifyId === track.id);
      }
    }

    loadIndex().then((data) => {
      tracks = (data.items || []).filter((item) => item.type === "track" && item.id);
      renderMix();
    });

    if (refresh) {
      refresh.addEventListener("click", renderMix);
    }
  }

  function shuffle(items) {
    for (let index = items.length - 1; index > 0; index -= 1) {
      const swapIndex = Math.floor(Math.random() * (index + 1));
      [items[index], items[swapIndex]] = [items[swapIndex], items[index]];
    }
    return items;
  }

  function siteURL(path) {
    const base = window.SpotWuBasePath || "/";
    return base.replace(/\/$/, "") + "/" + String(path || "").replace(/^\//, "");
  }
})();
