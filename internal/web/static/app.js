(function () {
  "use strict";

  function $(id) { return document.getElementById(id); }

  function showSignedOut(message) {
    $("user-bar").hidden = true;
    $("signed-in").hidden = true;
    $("market").hidden = true;
    $("structures").hidden = true;
    $("signed-out").hidden = false;
    $("session-message").textContent = message || "";
  }

  function showSignedIn(me) {
    $("signed-out").hidden = true;
    $("user-bar").hidden = false;
    $("signed-in").hidden = false;
    $("market").hidden = false;
    $("structures").hidden = false;
    $("user-name").textContent = me.name;
    var list = $("characters");
    list.replaceChildren();
    (me.characters || []).forEach(function (c) {
      var li = document.createElement("li");
      li.textContent = c.name;
      list.appendChild(li);
    });
    var sel = $("structure-char");
    sel.replaceChildren();
    (me.characters || []).forEach(function (c) {
      var opt = document.createElement("option");
      opt.value = c.character_id;
      opt.textContent = c.name;
      sel.appendChild(opt);
    });
  }

  var orderCols = [
    ["Order", "order_id"], ["Side", function (o) { return o.is_buy_order ? "buy" : "sell"; }],
    ["Price", "price"], ["Remain", "volume_remain"], ["Total", "volume_total"],
    ["Min", "min_volume"], ["Range", "range"], ["Location", "location_id"],
    ["Days", "duration"], ["Issued", "issued"]
  ];
  var historyCols = [
    ["Date", "date"], ["Average", "average"], ["Highest", "highest"],
    ["Lowest", "lowest"], ["Orders", "order_count"], ["Volume", "volume"]
  ];

  function renderTable(cols, rows, table) {
    table = table || $("market-table");
    table.replaceChildren();
    var head = document.createElement("tr");
    cols.forEach(function (c) {
      var th = document.createElement("th");
      th.textContent = c[0];
      head.appendChild(th);
    });
    table.appendChild(head);
    rows.forEach(function (row) {
      var tr = document.createElement("tr");
      cols.forEach(function (c) {
        var td = document.createElement("td");
        td.textContent = typeof c[1] === "function" ? c[1](row) : row[c[1]];
        tr.appendChild(td);
      });
      table.appendChild(tr);
    });
  }

  // Sell orders cheapest first, then buy orders highest first.
  function sortOrders(rows) {
    return rows.slice().sort(function (a, b) {
      if (a.is_buy_order !== b.is_buy_order) { return a.is_buy_order ? 1 : -1; }
      return a.is_buy_order ? b.price - a.price : a.price - b.price;
    });
  }

  // getJSON resolves with the parsed body, null after a 401 (the page then
  // shows the sign-in card) and rejects with the API's own error message.
  function getJSON(url) {
    return fetch(url, { headers: { Accept: "application/json" } }).then(function (resp) {
      if (resp.status === 401) {
        return resp.json().catch(function () { return {}; }).then(function (body) {
          if (body.error && body.error !== "not signed in") { throw new Error(body.error); }
          showSignedOut("Your session expired.");
          return null;
        });
      }
      return resp.json().catch(function () { return {}; }).then(function (body) {
        if (!resp.ok) {
          var wait = resp.headers.get("Retry-After");
          throw new Error((body.error || "HTTP " + resp.status) + (wait ? " (retry in " + wait + "s)" : ""));
        }
        return body;
      });
    });
  }

  function loadMarket(kind) {
    var region = encodeURIComponent($("market-region").value.trim());
    var type = encodeURIComponent($("market-type").value.trim());
    var url = "/api/markets/regions/" + region + "/" + kind + "?type_id=" + type;
    if (kind === "orders") { url += "&order_type=" + encodeURIComponent($("market-order-type").value); }
    $("market-status").textContent = "Loading...";
    $("market-table").replaceChildren();
    getJSON(url).then(function (rows) {
      if (!rows) { return; }
      $("market-status").textContent = rows.length + (rows.length === 1 ? " row" : " rows");
      if (kind === "orders") { renderTable(orderCols, sortOrders(rows)); } else { renderTable(historyCols, rows); }
    }).catch(function (err) {
      $("market-status").textContent = "Error: " + err.message;
    });
  }

  var structOrderCols = [
    ["Order", "order_id"], ["Side", function (o) { return o.is_buy_order ? "buy" : "sell"; }],
    ["Type", function (o) { return o.type_name ? o.type_name + " (" + o.type_id + ")" : o.type_id; }],
    ["Price", "price"], ["Remain", "volume_remain"], ["Total", "volume_total"],
    ["Min", "min_volume"], ["Range", "range"], ["Days", "duration"], ["Issued", "issued"]
  ];

  function structStatus(text) { $("structure-status").textContent = text; }

  function loadStructureSearch() {
    var q = $("structure-q").value.trim();
    var list = $("structure-results");
    list.replaceChildren();
    $("structure-table").replaceChildren();
    if (q.length < 3) { structStatus("Type at least 3 characters to search."); return; }
    structStatus("Searching...");
    var url = "/api/structures/search?character_id=" + encodeURIComponent($("structure-char").value) +
      "&q=" + encodeURIComponent(q);
    getJSON(url).then(function (hits) {
      if (!hits) { return; }
      structStatus(hits.length + (hits.length === 1 ? " structure found" : " structures found") +
        (hits.some(function (h) { return !h.accessible; }) ? "; unnamed ones are not accessible to this character." : "."));
      hits.forEach(function (h) {
        var li = document.createElement("li");
        var b = document.createElement("button");
        b.type = "button";
        b.textContent = (h.name || "(no access)") + " [" + h.id + "]";
        b.addEventListener("click", function () { $("structure-id").value = h.id; });
        li.appendChild(b);
        list.appendChild(li);
      });
    }).catch(function (err) { structStatus("Error: " + err.message); });
  }

  function loadStructureOrders() {
    var id = $("structure-id").value.trim();
    if (!/^[0-9]+$/.test(id) || id === "0") { structStatus("Enter a valid structure id."); return; }
    var url = "/api/structures/" + encodeURIComponent(id) + "/orders?character_id=" +
      encodeURIComponent($("structure-char").value);
    var type = $("structure-type").value.trim();
    if (type) { url += "&type_id=" + encodeURIComponent(type); }
    structStatus("Loading...");
    $("structure-table").replaceChildren();
    getJSON(url).then(function (body) {
      if (!body) { return; }
      var rows = body.orders || [];
      structStatus(rows.length + (rows.length === 1 ? " order" : " orders") +
        (body.truncated ? ". Truncated: the page cap was reached, so this list is incomplete." : "."));
      renderTable(structOrderCols, rows, $("structure-table"));
    }).catch(function (err) {
      structStatus("Error: " + err.message + (/market access/.test(err.message) ?
        " Only characters that can dock at the structure can read its market." : ""));
    });
  }

  // Inline handlers are blocked by the CSP; Enter runs the matching action.
  $("structure-form").addEventListener("submit", function (e) { e.preventDefault(); loadStructureSearch(); });
  $("structure-id-form").addEventListener("submit", function (e) { e.preventDefault(); loadStructureOrders(); });
  $("structure-search").addEventListener("click", loadStructureSearch);
  $("structure-orders").addEventListener("click", loadStructureOrders);

  $("market-orders").addEventListener("click", function () { loadMarket("orders"); });
  $("market-history").addEventListener("click", function () { loadMarket("history"); });

  fetch("/api/me", { headers: { Accept: "application/json" } }).then(function (resp) {
    if (resp.status === 401) { showSignedOut(); return null; }
    if (!resp.ok) { throw new Error("HTTP " + resp.status); }
    return resp.json();
  }).then(function (me) {
    if (me) { showSignedIn(me); }
  }).catch(function (err) {
    showSignedOut("Cannot check your session: " + err.message);
  });
})();
