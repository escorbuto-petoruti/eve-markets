(function () {
  "use strict";

  function $(id) { return document.getElementById(id); }

  function showSignedOut(message) {
    $("user-bar").hidden = true;
    $("signed-in").hidden = true;
    $("market").hidden = true;
    $("signed-out").hidden = false;
    $("session-message").textContent = message || "";
  }

  function showSignedIn(me) {
    $("signed-out").hidden = true;
    $("user-bar").hidden = false;
    $("signed-in").hidden = false;
    $("market").hidden = false;
    $("user-name").textContent = me.name;
    var list = $("characters");
    list.replaceChildren();
    (me.characters || []).forEach(function (c) {
      var li = document.createElement("li");
      li.textContent = c.name;
      list.appendChild(li);
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

  function renderTable(cols, rows) {
    var table = $("market-table");
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

  function loadMarket(kind) {
    var region = encodeURIComponent($("market-region").value.trim());
    var type = encodeURIComponent($("market-type").value.trim());
    var url = "/api/markets/regions/" + region + "/" + kind + "?type_id=" + type;
    if (kind === "orders") { url += "&order_type=" + encodeURIComponent($("market-order-type").value); }
    $("market-status").textContent = "Loading...";
    $("market-table").replaceChildren();
    fetch(url, { headers: { Accept: "application/json" } }).then(function (resp) {
      if (resp.status === 401) { showSignedOut("Your session expired."); return null; }
      return resp.json().catch(function () { return {}; }).then(function (body) {
        if (!resp.ok) { throw new Error(body.error || "HTTP " + resp.status); }
        return body;
      });
    }).then(function (rows) {
      if (!rows) { return; }
      $("market-status").textContent = rows.length + (rows.length === 1 ? " row" : " rows");
      if (kind === "orders") { renderTable(orderCols, sortOrders(rows)); } else { renderTable(historyCols, rows); }
    }).catch(function (err) {
      $("market-status").textContent = "Error: " + err.message;
    });
  }

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
