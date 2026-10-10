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
    fmtJita: fmtJita,
    fmtTime: fmtTime
  };

  root.EveFormat = api;
  if (typeof module !== "undefined" && module.exports) { module.exports = api; }
})(typeof window !== "undefined" ? window : globalThis);
