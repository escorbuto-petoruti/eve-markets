// Pure formatting helpers shared by app.js. Loaded as a plain script in the
// browser (exposed as window.EveFormat) and as a CommonJS module under node
// for the tests in internal/web/jstest.
(function (root) {
  "use strict";

  var nf = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 });
  var isk = new Intl.NumberFormat("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 });

  function fmtInt(n) { return nf.format(n); }
  function fmtIsk(n) { return isk.format(n); }
  function fmtPrice(n) { return n > 0 ? fmtIsk(n) : "-"; }
  function fmtPct(n) { return n === null || n === undefined ? "-" : (n > 0 ? "+" : "") + n.toFixed(1) + "%"; }
  // fmtShare renders part as a percentage of total, "-" when total is not positive.
  function fmtShare(part, total) { return total > 0 ? (part / total * 100).toFixed(1) + "%" : "-"; }
  var m3 = new Intl.NumberFormat("en-US", { maximumFractionDigits: 2 });
  // fmtVolume renders a volume in m3, "-" when it is not positive.
  function fmtVolume(n) { return n > 0 ? m3.format(n) : "-"; }
  // fmtIskPerM3 renders value per m3 of volume, "-" without a positive volume.
  function fmtIskPerM3(value, volume) { return volume > 0 ? fmtIsk(value / volume) : "-"; }
  // groupByCategory groups demand rows by category, keeping the row order
  // inside each group. Groups are totalled and sorted by sold value, biggest
  // first, with rows lacking a category last under "Unknown".
  function groupByCategory(rows) {
    var byName = {};
    var groups = [];
    rows.forEach(function (r) {
      var name = r.category || "Unknown";
      var g = byName[name];
      if (!g) {
        g = { name: name, rows: [], sold_units: 0, sold_value: 0, sold_volume: 0, open_buy_value: 0 };
        byName[name] = g;
        groups.push(g);
      }
      g.rows.push(r);
      g.sold_units += r.sold_units;
      g.sold_value += r.sold_value;
      g.sold_volume += r.sold_volume;
      g.open_buy_value += r.open_buy_value;
    });
    return groups.sort(function (a, b) {
      if ((a.name === "Unknown") !== (b.name === "Unknown")) { return a.name === "Unknown" ? 1 : -1; }
      return b.sold_value - a.sold_value;
    });
  }
  // filterRows keeps the rows whose name, category or group contains query,
  // ignoring case and surrounding spaces.
  function filterRows(rows, query) {
    var q = String(query || "").trim().toLowerCase();
    if (!q) { return rows; }
    return rows.filter(function (r) {
      return [r.name, r.category, r.group].some(function (s) { return String(s || "").toLowerCase().indexOf(q) !== -1; });
    });
  }
  // sortRows returns a sorted copy ordered by value(row), "asc" or "desc".
  // Numbers compare numerically and strings case-insensitively; ties keep their
  // order and rows without a value (null, undefined, NaN) always go last.
  function sortRows(rows, value, dir) {
    var sign = dir === "asc" ? 1 : -1;
    function missing(v) { return v === null || v === undefined || v !== v; }
    return rows.map(function (r, i) { return { r: r, v: value(r), i: i }; }).sort(function (a, b) {
      var am = missing(a.v), bm = missing(b.v);
      if (am || bm) { return am === bm ? a.i - b.i : (am ? 1 : -1); }
      var c = typeof a.v === "string" ? a.v.toLowerCase().localeCompare(String(b.v).toLowerCase()) : a.v - b.v;
      return c !== 0 ? sign * c : a.i - b.i;
    }).map(function (x) { return x.r; });
  }
  function fmtJita(r, key, pct) {
    if (!r.jita) { return "-"; }
    return pct ? fmtPct(r.jita[key]) : fmtPrice(r.jita[key]);
  }
  function fmtTime(sec) { return sec ? new Date(sec * 1000).toLocaleString() : "-"; }

  var api = {
    fmtInt: fmtInt,
    fmtIsk: fmtIsk,
    fmtPrice: fmtPrice,
    fmtPct: fmtPct,
    fmtShare: fmtShare,
    fmtVolume: fmtVolume,
    fmtIskPerM3: fmtIskPerM3,
    groupByCategory: groupByCategory,
    filterRows: filterRows,
    sortRows: sortRows,
    fmtJita: fmtJita,
    fmtTime: fmtTime
  };

  root.EveFormat = api;
  if (typeof module !== "undefined" && module.exports) { module.exports = api; }
})(typeof window !== "undefined" ? window : globalThis);
